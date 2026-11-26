#!/usr/bin/env python3
"""Export only aggregated download counters from a local Mihomo controller."""
import argparse
import collections
from datetime import datetime, timezone
import fcntl
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler
import json
import math
import os
from pathlib import Path
import socket
from socketserver import UnixStreamServer
import threading
import time


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


class Sampler:
    def __init__(self, inbound_port):
        self.inbound_port = str(inbound_port)
        self.last_at = None
        self.last_connections = {}
        self.smoothed = {}

    def sample(self, connections, types, now):
        peers = {}
        counts = collections.Counter()
        deltas = collections.Counter()
        interval = now - self.last_at if self.last_at is not None else 0
        valid_interval = 0 < interval <= 10
        for conn in connections:
            metadata = conn['metadata']
            if str(metadata.get('inboundPort')) != self.inbound_port or metadata.get('network') != 'tcp':
                continue
            proto = next((types[name] for name in conn['chains'] if name in types), 'other')
            if proto not in PROTOCOLS:
                proto = 'other'
            conn_id = conn['id']
            downloaded = conn['download']
            counts[proto] += 1
            peers[conn_id] = downloaded
            if valid_interval:
                deltas[proto] += max(0, downloaded - self.last_connections.get(conn_id, 0))
        nodes = []
        for proto in PROTOCOLS:
            if proto == 'other' and not counts[proto] and not self.smoothed.get(proto):
                continue
            live = deltas[proto] * 8 / interval / 1e6 if valid_interval else None
            smooth = live
            if live is not None and self.smoothed.get(proto) is not None:
                last = self.smoothed[proto]
                smooth = last + (1 - math.exp(-interval / 30)) * (live - last)
            self.smoothed[proto] = smooth
            nodes.append({'protocol': proto, 'active_connections': counts[proto],
                          'download_mbps': live, 'smoothed_mbps': smooth})
        self.last_at, self.last_connections = now, peers
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


def publish(server, controller, inbound_port):
    sampler = Sampler(inbound_port)
    types, next_refresh = {}, 0
    while True:
        now = time.monotonic()
        try:
            if now >= next_refresh:
                types = proxy_types(read_controller(controller, '/providers/proxies'))
                next_refresh = now + 30
            connections = read_controller(controller, '/connections')['connections'] or []
            nodes = sampler.sample(connections, types, time.monotonic())
            server.snapshot = json.dumps({'sampled_at': datetime.now(timezone.utc).isoformat(),
                                          'nodes': nodes}).encode()
        except (OSError, ValueError, KeyError, TypeError):
            server.snapshot = None
            sampler = Sampler(inbound_port)
            next_refresh = 0
        time.sleep(max(0, SAMPLE_INTERVAL - (time.monotonic() - now)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--controller', required=True)
    parser.add_argument('--listen', type=Path, required=True)
    parser.add_argument('--inbound-port', type=int, required=True)
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
            thread = threading.Thread(target=publish, args=(server, args.controller, args.inbound_port), daemon=True)
            thread.start()
            try:
                server.serve_forever()
            finally:
                args.listen.unlink()


if __name__ == '__main__':
    main()
