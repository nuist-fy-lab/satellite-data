import importlib.util
from pathlib import Path
import sys
import unittest


path = Path(__file__).parents[1] / 'mihomo-egress-metrics.py'
sys.path.insert(0, str(path.parent))
spec = importlib.util.spec_from_file_location('mihomo_egress_metrics', path)
metrics = importlib.util.module_from_spec(spec)
spec.loader.exec_module(metrics)


class SamplerTest(unittest.TestCase):
    def test_uses_protocol_chains_and_individual_connection_deltas(self):
        sampler = metrics.Sampler(17890)
        types = {'one': 'vless', 'two': 'hysteria2'}
        vless = self.connection('a', 'one', 1000000)
        hy2 = self.connection('b', 'two', 2000000)
        other_port = self.connection('c', 'two', 0, '18000')
        first = sampler.sample([vless, hy2, other_port], types, 1)
        self.assertEqual([node['active_connections'] for node in first], [1, 1])
        self.assertIsNone(first[0]['download_mbps'])

        vless['download'] += 1000000
        hy2['download'] += 2000000
        other_port['download'] += 5000000
        second = sampler.sample([vless, hy2, other_port], types, 3)
        self.assertEqual([node['download_mbps'] for node in second], [4, 8])

        replacement = self.connection('new-id', 'one', 500000)
        third = sampler.sample([replacement], types, 5)
        self.assertEqual([node['download_mbps'] for node in third], [2, 0])
        self.assertEqual(third[1]['active_connections'], 0)
        self.assertGreater(third[1]['smoothed_mbps'], 0)
        self.assertIsNone(sampler.sample([], types, 30)[0]['download_mbps'])

    def test_wan_stats_only_credit_the_dialed_node(self):
        sampler = metrics.Sampler(17890)
        types = {'one': 'vless', 'two': 'hysteria2'}
        paths = {'one': ('vwan1', 'vless', 200), 'two': ('vwan7', 'hysteria2', 300)}
        connections = [self.connection('a', 'one', 100), self.connection('b', 'one', 200),
                       self.connection('c', 'two', 300), self.connection('d', 'two', 400, '9090')]
        sampler.sample(connections, types, 5, paths)
        connections[0]['download'] += 500_000
        connections[2]['download'] += 1_000_000
        sampler.sample(connections, types, 7, paths)
        self.assertEqual(sampler.speeds, {'one': 250_000, 'two': 500_000})
        self.assertEqual([(r['wan'], r['active_connections'], r['download_mbps']) for r in sampler.paths],
                         [('vwan1', 2, 2), ('vwan7', 1, 4)])

    @staticmethod
    def connection(conn_id, node, downloaded, port='17890'):
        return {'id': conn_id, 'metadata': {'inboundPort': port, 'network': 'tcp'},
                'chains': ['group', node], 'download': downloaded}
