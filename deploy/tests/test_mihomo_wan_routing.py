import importlib.util
from pathlib import Path
import unittest


path = Path(__file__).parents[1] / 'mihomo_wan_routing.py'
spec = importlib.util.spec_from_file_location('mihomo_wan_routing', path)
routing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(routing)


class WANPolicyTest(unittest.TestCase):
    def test_fast_paths_share_slots_with_a_single_probe(self):
        policy = routing.WANPolicy()
        nodes = {'a': ('vwan1', 'vless', 100), 'b': ('vwan7', 'hysteria2', 200),
                 'c': ('vwan6', 'vless', 50), 'd': ('vwan8', 'vless', 50),
                 'down': ('vwan9', 'vless', None)}
        policy.observe({'a': 100, 'b': 90, 'c': 10, 'down': 1000}, 100)
        selected = policy.choose(nodes, 'legacy', 101)
        self.assertEqual(len(selected), 6)
        self.assertIn('a', selected)
        self.assertIn('b', selected)
        self.assertEqual(sum(n in ('c', 'd') for n in selected), 1)
        self.assertNotIn('down', selected)
        self.assertNotEqual(policy.choose(nodes, 'legacy', 162)[0], selected[0])
        self.assertEqual(policy.choose({'down': nodes['down']}, 'legacy', 165), ['legacy'] * 6)

    def test_move_only_one_healthy_slot_but_replace_dead_slots_immediately(self):
        current = {'s1': 'lost', 's2': 'a', 's3': 'b', 's4': 'lost'}
        self.assertEqual(routing.slot_changes(current, ['a'] * 4, {'a', 'b'}, 'legacy'),
                         {'s4': 'a', 's1': 'a', 's3': 'a'})
        self.assertEqual(len(routing.slot_changes(current, ['x'] * 4, {'a', 'b', 'lost', 'x'}, 'legacy')), 1)

    def test_build_follows_existing_rule_and_does_not_rewrite_fallback(self):
        base = '''mixed-port: 17890
proxy-providers:
  original-provider:
    type: http
    path: ./provider.yaml
proxy-groups:
  - name: east-hpc
    type: load-balance
    use: [original-provider]
rules:
  - MATCH,east-hpc
'''
        cfg, providers = routing.build_config(base, [1, 7])
        self.assertEqual(providers, ['sat-wan-1', 'sat-wan-7'])
        self.assertIn('path: ./provider.yaml\n  sat-wan-1:', cfg)
        self.assertIn('interface-name: macvlan7', cfg)
        self.assertIn('  - name: east-hpc\n    type: load-balance', cfg)
        self.assertIn('  - MATCH,sat-wan-outbound', cfg)
        self.assertIn('    proxies: ["sat-wan-adaptive", "east-hpc"]', cfg)
        self.assertEqual(cfg.count('default-selected: east-hpc'), 6)
