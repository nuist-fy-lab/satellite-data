import importlib.util
from pathlib import Path
import unittest


path = Path(__file__).parents[1] / 'mihomo-egress-metrics.py'
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

    @staticmethod
    def connection(conn_id, node, downloaded, port='17890'):
        return {'id': conn_id, 'metadata': {'inboundPort': port, 'network': 'tcp'},
                'chains': ['group', node], 'download': downloaded}
