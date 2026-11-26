"""Configure and tune the dedicated iStoreOS proxy's WAN path weights."""
import argparse
from collections import Counter
import json
import math
import os
from pathlib import Path
import re


GROUP = 'sat-wan-adaptive'
OUTBOUND = 'sat-wan-outbound'
SLOTS = tuple('sat-wan-slot-' + str(i) for i in range(1, 7))
HEALTH_URL = 'https://www.gstatic.com/generate_204'
DECISION_INTERVAL = 15
PROBE_INTERVAL = 60
FRESH_INTERVAL = 120
KINDS = frozenset(('vless', 'hysteria2'))


def wan_nodes(providers):
    """Return only tagged provider variants with the expected physical binding."""
    result = {}
    for name, provider in providers.items():
        number = re.fullmatch(r'sat-wan-([1-9])', name)
        if number is None:
            continue
        wan = 'vwan' + number[1]
        for node in provider.get('proxies') or []:
            protocol = node['type'].lower()
            if protocol not in KINDS or node.get('interface') != 'macvlan' + number[1]:
                continue
            health = node.get('extra', {}).get(HEALTH_URL, {})
            history = health.get('history') or []
            delay = history[-1].get('delay', 0) if history else 0
            result[node['name']] = (wan, protocol, delay if health.get('alive') and delay > 0 else None)
    return result


class WANPolicy:
    def __init__(self):
        self.observed = {}
        self.last_probe = {}
        self.probe = None
        self.probe_until = 0

    def observe(self, speeds, now):
        self.observed = {node: sample for node, sample in self.observed.items()
                         if now - sample[0] < FRESH_INTERVAL}
        for node, speed in speeds.items():
            if speed > 0:
                at, previous = self.observed.get(node, (now, speed))
                weight = 1 - math.exp(-(now - at) / 20)
                self.observed[node] = (now, previous + weight * (speed - previous))

    def choose(self, nodes, fallback, now):
        healthy = {name: info for name, info in nodes.items() if info[2] is not None}
        self.last_probe = {key: at for key, at in self.last_probe.items() if key in nodes}
        if not healthy:
            return [fallback] * len(SLOTS)
        tested = {name: self.observed[name][1] for name in healthy
                  if name in self.observed and now - self.observed[name][0] < FRESH_INTERVAL}
        if tested:
            fastest = max(tested.values())
            scores = {name: value for name, value in tested.items() if value >= fastest * 0.60}
        else:
            best = min(healthy, key=lambda name: (healthy[name][2], name))
            scores = {best: 1}
        rest = healthy.keys() - scores.keys()
        if not rest:
            self.probe = None
        elif self.probe not in rest or now >= self.probe_until:
            self.probe = min(rest, key=lambda name: (self.last_probe.get(name, 0), healthy[name][2], name))
            self.last_probe[self.probe] = now
            self.probe_until = now + PROBE_INTERVAL
        selected = [self.probe] if self.probe else []
        paths, wans = Counter(), Counter()
        for _ in range(len(SLOTS) - len(selected)):
            best = max(scores, key=lambda name: (scores[name] /
                       (1 + 0.5 * paths[name] + 0.25 * wans[healthy[name][0]]), -healthy[name][2], name))
            selected.append(best)
            paths[best] += 1
            wans[healthy[best][0]] += 1
        return selected


def slot_changes(current, planned, healthy, fallback):
    """Replace unhealthy assignments immediately, otherwise move one slot per cycle."""
    target = Counter(planned)
    keep = Counter()
    changes = {}
    excess = []
    for slot, node in current.items():
        if keep[node] < target[node]:
            keep[node] += 1
        elif node != fallback and node not in healthy:
            excess.insert(0, (slot, True))
        else:
            excess.append((slot, False))
    deficit = list((target - keep).elements())
    moved = False
    for (slot, dead), node in zip(excess, deficit):
        if not dead and moved:
            break
        changes[slot] = node
        moved = moved or not dead
    return changes


def build_config(original, wans):
    if not wans or any(n < 1 or n > 9 for n in wans):
        raise ValueError('at least one VWAN from 1 to 9 is required')
    if GROUP in original or 'sat-wan-' in original:
        raise ValueError('adaptive WANs are already configured')
    catch_all = list(re.finditer(r'(?m)^(\s*?-\s*MATCH,)\s*([A-Za-z0-9_-]+)\s*$', original))
    if len(catch_all) != 1 or original.count('\nproxy-providers:\n') != 1 or original.count('\nproxy-groups:\n') != 1:
        raise ValueError('expected the dedicated proxy config with one final MATCH rule')
    rule = catch_all[0]
    fallback = rule[2]
    if not re.search(r'(?m)^\s*-\s*name:\s*["\']?' + re.escape(fallback) + r'["\']?\s*$', original):
        raise ValueError('existing fallback group was not found')
    result = original[:rule.start(2)] + OUTBOUND + original[rule.end(2):]
    providers = []
    for n in sorted(set(wans)):
        name = 'sat-wan-' + str(n)
        providers.append(name)
        result = result.replace('\nproxy-groups:\n', '''
  {name}:
    type: file
    path: ./wan-providers/{name}.yaml
    interval: 90
    health-check:
      enable: true
      url: {url}
      expected-status: 204
      interval: 30
      lazy: false
    override:
      interface-name: macvlan{n}
      ip-version: ipv4
      additional-prefix: "{name} / "

proxy-groups:
'''.format(name=name, url=HEALTH_URL, n=n), 1)
    groups = ['  - name: ' + OUTBOUND, '    type: fallback', '    url: ' + HEALTH_URL,
              '    expected-status: 204', '    interval: 30', '    lazy: false',
              '    proxies: ' + json.dumps((GROUP, fallback)),
              '  - name: ' + GROUP, '    type: load-balance', '    strategy: round-robin',
              '    proxies: ' + json.dumps(SLOTS)]
    for slot in SLOTS:
        groups += ['  - name: ' + slot, '    type: select', '    proxies: [' + fallback + ']',
                   '    default-selected: ' + fallback, '    use: ' + json.dumps(providers)]
    return result.replace('\nproxy-groups:\n', '\nproxy-groups:\n' + '\n'.join(groups) + '\n', 1), providers


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('wans', type=int, nargs='+')
    args = parser.parse_args()
    os.umask(0o077)
    output = args.output
    parent = output.parent
    if not parent.is_dir() or parent.stat().st_mode & 0o077:
        parser.error('output must be in a private directory')
    if output.exists():
        parser.error('output must be a new file')
    cfg, names = build_config(args.base.read_text(), args.wans)
    links = parent / 'wan-providers'
    links.mkdir(mode=0o700, exist_ok=True)
    if links.is_symlink():
        parser.error('wan-providers must be a real directory')
    for name in names:
        path = links / (name + '.yaml')
        if path.is_symlink():
            if os.readlink(str(path)) != '../provider.yaml':
                parser.error('unexpected provider link')
        else:
            path.symlink_to('../provider.yaml')
    with output.open('x') as file:
        file.write(cfg)


if __name__ == '__main__':
    main()
