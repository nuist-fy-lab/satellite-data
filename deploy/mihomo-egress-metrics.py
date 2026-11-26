#!/usr/bin/env python3
"""Export only aggregated download counters from a local Mihomo controller."""
import argparse
import collections
from datetime import datetime, timezone
import fcntl
from http.client import HTTPConnection, HTTPException
from http.server import BaseHTTPRequestHandler
import json
import math
import os
from pathlib import Path
import socket
from socketserver import UnixStreamServer
import sys
import threading
import time
from urllib.parse import quote

from mihomo_wan_routing import DECISION_INTERVAL, GROUP, SLOTS, WANPolicy, slot_changes, wan_nodes


PROTOCOLS = ('vless', 'hysteria2', 'other')
MAX_RESPONSE = 2 << 20
SAMPLE_INTERVAL = 2


class UnixHTTPConnection(HTTPConnection):
    def __init__(self, path):
        super().__init__('mihomo', timeout=2)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def read_controller(path, endpoint):
    client = UnixHTTPConnection(path)
    try:
        client.request('GET', endpoint)
        response = client.getresponse()
        body = response.read(MAX_RESPONSE + 1)
        if response.status != 200 or len(body) > MAX_RESPONSE:
            raise ValueError('controller response unavailable')
        doc = json.loads(body)
        if not isinstance(doc, dict):
            raise ValueError('invalid controller response')
        return doc
    finally:
        client.close()


def proxy_types(doc):
    return {node['name']: node['type'].lower()
            for provider in doc['providers'].values()
            for node in (provider.get('proxies') or [])}


def reconcile(controller, policy, paths, now):
    groups = read_controller(controller, '/proxies')['proxies']
    group = groups[GROUP]
    if group['type'] != 'LoadBalance' or group['all'] != list(SLOTS):
        raise ValueError('unexpected adaptive group configuration')
    slots = [groups[slot] for slot in SLOTS]
    fallback = slots[0]['all'][0]
    if fallback in paths or any(g['type'] != 'Selector' or g['all'][0] != fallback for g in slots):
        raise ValueError('unexpected adaptive slot configuration')
    available = set.intersection(*(set(g['all']) for g in slots))
    candidates = {name: path for name, path in paths.items() if name in available}
    current = dict(zip(SLOTS, (g['now'] for g in slots)))
    healthy = {name for name, info in candidates.items() if info[2] is not None}
    planned = policy.choose(candidates, fallback, now)
    changes = slot_changes(current, planned, healthy, fallback)
    for slot, node in changes.items():
        client = UnixHTTPConnection(controller)
        try:
            data = json.dumps({'name': node}).encode()
            client.request('PUT', '/proxies/' + quote(slot, safe=''), data, {'Content-Type': 'application/json'})
            if client.getresponse().status != 204:
                raise ValueError('adaptive selector rejected an update')
            current[slot] = node
        finally:
            client.close()
    weights = collections.Counter(current.values())
    if changes:
        public = collections.Counter()
        for node, count in weights.items():
            public['/'.join(paths[node][:2]) if node in paths else 'original'] += count
        print(json.dumps({'event': 'satellite_proxy_wan_weights', 'at': datetime.now(timezone.utc).isoformat(),
                          'changed': len(changes), 'weights': dict(public)}), file=sys.stderr, flush=True)
    return weights


class Sampler:
    def __init__(self, inbound_port):
        self.inbound_port = str(inbound_port)
        self.last_at = None
        self.last_connections = {}
        self.smoothed = {}

    def sample(self, connections, types, now, wan_paths=None):
        wan_paths = wan_paths or {}
        peers = {}
        counts = collections.Counter()
        deltas = collections.Counter()
        advanced = collections.Counter()
        path_deltas = collections.Counter()
        interval = now - self.last_at if self.last_at is not None else 0
        valid_interval = 0 < interval <= 10
        for conn in connections:
            metadata = conn['metadata']
            if str(metadata.get('inboundPort')) != self.inbound_port or metadata.get('network') != 'tcp':
                continue
            name = next((member for member in conn['chains'] if member in types), None)
            proto = types.get(name, 'other')
            if proto not in PROTOCOLS:
                proto = 'other'
            conn_id = conn['id']
            downloaded = conn['download']
            counts[proto] += 1
            peers[conn_id] = downloaded
            path = wan_paths.get(name, ())[:2]
            if path:
                counts[path] += 1
            if valid_interval:
                delta = max(0, downloaded - self.last_connections.get(conn_id, 0))
                deltas[proto] += delta
                if path and delta:
                    deltas[path] += delta
                    advanced[name] += 1
                    path_deltas[name] += delta
        nodes = []
        paths = []
        identities = sorted({p[:2] for p in wan_paths.values()})
        smooth_next = {}
        for key in list(PROTOCOLS) + identities:
            if key == 'other' and not counts[key] and not self.smoothed.get(key):
                continue
            live = deltas[key] * 8 / interval / 1e6 if valid_interval else None
            smooth = live
            if live is not None and self.smoothed.get(key) is not None:
                last = self.smoothed[key]
                smooth = last + (1 - math.exp(-interval / 30)) * (live - last)
            smooth_next[key] = smooth
            row = {'active_connections': counts[key], 'download_mbps': live, 'smoothed_mbps': smooth}
            if isinstance(key, tuple):
                row.update(wan=key[0], protocol=key[1])
                paths.append(row)
            else:
                row['protocol'] = key
                nodes.append(row)
        self.smoothed = smooth_next
        self.last_at, self.last_connections = now, peers
        self.paths = paths
        self.speeds = {name: path_deltas[name] / interval / n for name, n in advanced.items()}
        return nodes


class SnapshotHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = self.server.snapshot
        if self.path != '/snapshot':
            self.send_error(404)
        elif body is None:
            self.send_error(503)
        else:
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    def setup(self):
        self.request.settimeout(1)
        super().setup()

    def log_message(self, *_args):
        return


class SnapshotServer(UnixStreamServer):
    snapshot = None

    def handle_error(self, _request, _client_address):
        # A caller can disconnect after its deadline without affecting sampling.
        return


def publish(server, controller, inbound_port, adaptive):
    sampler = Sampler(inbound_port)
    policy = WANPolicy()
    types, next_refresh = {}, 0
    paths, weights, next_decision = {}, {}, 0
    while True:
        now = time.monotonic()
        try:
            if now >= next_refresh:
                providers = read_controller(controller, '/providers/proxies')
                types = proxy_types(providers)
                paths = wan_nodes(providers['providers'])
                next_refresh = now + (5 if adaptive else 30)
            connections = read_controller(controller, '/connections')['connections'] or []
            nodes = sampler.sample(connections, types, time.monotonic(), paths)
            if adaptive:
                policy.observe(sampler.speeds, now)
                if now >= next_decision:
                    next_decision = now + DECISION_INTERVAL
                    try:
                        weights = reconcile(controller, policy, paths, now)
                    except (OSError, HTTPException, ValueError, KeyError, TypeError, IndexError):
                        weights = {}
            active_weights = collections.Counter()
            healthy_paths = set()
            for name, info in paths.items():
                active_weights[info[:2]] += weights.get(name, 0)
                if info[2] is not None:
                    healthy_paths.add(info[:2])
            for row in sampler.paths:
                key = (row['wan'], row['protocol'])
                row['healthy'] = key in healthy_paths
                row['slots'] = active_weights[key]
            server.snapshot = json.dumps({'sampled_at': datetime.now(timezone.utc).isoformat(),
                                          'nodes': nodes, 'paths': sampler.paths}).encode()
        except (OSError, HTTPException, ValueError, KeyError, TypeError):
            server.snapshot = None
            sampler = Sampler(inbound_port)
            next_refresh = 0
        time.sleep(max(0, SAMPLE_INTERVAL - (time.monotonic() - now)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--controller', required=True)
    parser.add_argument('--listen', type=Path, required=True)
    parser.add_argument('--inbound-port', type=int, required=True)
    parser.add_argument('--adaptive', action='store_true', help='adjust the dedicated WAN round-robin group locally')
    args = parser.parse_args()
    os.umask(0o077)
    parent = args.listen.parent
    if not args.listen.is_absolute() or parent.stat().st_mode & 0o077 or parent.stat().st_uid != os.getuid():
        parser.error('the listen path must be absolute in a private directory owned by the collector')
    with args.listen.with_suffix('.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.listen.is_socket():
            args.listen.unlink()
        with SnapshotServer(str(args.listen), SnapshotHandler) as server:
            thread = threading.Thread(target=publish, args=(server, args.controller, args.inbound_port, args.adaptive), daemon=True)
            thread.start()
            try:
                server.serve_forever()
            finally:
                args.listen.unlink()


if __name__ == '__main__':
    main()
