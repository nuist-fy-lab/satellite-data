"""Cloud CI coverage for concurrent MD5-checked resumable downloads."""
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import types
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / 'download-research-manifest.py'
SPEC = importlib.util.spec_from_file_location('research_downloader', str(SCRIPT))
DOWNLOADER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DOWNLOADER)


class ParallelResearchDownloadTest(unittest.TestCase):
    def test_resume_md5_and_parallelism(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            output = root / 'data'
            output.mkdir()
            data = {}
            items = []
            for index in range(4):
                name = 'file_{}.csv'.format(index)
                raw = (('abc-{}-'.format(index)).encode('ascii') * 500)
                data[name] = raw
                items.append({
                    'year': 2015, 'name': name, 'size': len(raw),
                    'md5': hashlib.md5(raw).hexdigest(),
                    'url': 'https://example.invalid/data/' + name})
            manifest = root / 'manifest.json'
            manifest.write_text(json.dumps(items), encoding='utf8')
            (output / '2015').mkdir()
            (output / '2015' / 'file_0.csv.part').write_bytes(data['file_0.csv'][:47])
            (output / '2015' / 'file_3.csv').write_bytes(data['file_3.csv'])
            lock = threading.Lock()
            concurrent = {'now': 0, 'peak': 0, 'calls': 0}

            def simulate_curl(argv, **kwargs):
                self.assertIn('--http1.1', argv)
                self.assertIn('--continue-at', argv)
                destination = Path(argv[argv.index('--output') + 1])
                name = destination.name[:-5]
                with lock:
                    concurrent['now'] += 1
                    concurrent['calls'] += 1
                    concurrent['peak'] = max(concurrent['now'], concurrent['peak'])
                try:
                    time.sleep(.02)
                    previous = destination.stat().st_size if destination.exists() else 0
                    with destination.open('ab') as file:
                        file.write(data[name][previous:])
                    return types.SimpleNamespace(returncode=0,
                                                 stdout=b'200 10000 2048',
                                                 stderr=b'')
                finally:
                    with lock:
                        concurrent['now'] -= 1

            with patch.object(subprocess, 'run', side_effect=simulate_curl):
                with patch('sys.argv', [str(SCRIPT), '--manifest', str(manifest),
                                        '--output-dir', str(output), '--workers', '3']):
                    self.assertEqual(DOWNLOADER.main(), 0)
            self.assertGreaterEqual(concurrent['peak'], 2)
            self.assertEqual(concurrent['calls'], 3)
            for name, content in data.items():
                self.assertEqual((output / '2015' / name).read_bytes(), content)
                self.assertFalse((output / '2015' / (name + '.part')).exists())
            # Restart: each verified file is skipped, with no new connection.
            with patch.object(subprocess, 'run', side_effect=AssertionError('unneeded transfer')):
                with patch('sys.argv', [str(SCRIPT), '--manifest', str(manifest),
                                        '--output-dir', str(output), '--workers', '3']):
                    self.assertEqual(DOWNLOADER.main(), 0)

    def test_reject_path_escape_and_duplicate_destinations(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            manifest = root / 'manifest.json'
            for names in [['../escape.csv'], ['valid.csv', 'valid.csv']]:
                manifest.write_text(json.dumps([
                    {'year': 2015, 'name': name, 'size': 10,
                     'md5': 'a' * 32, 'url': 'https://example.invalid/data'}
                    for name in names]), encoding='utf8')
                with self.assertRaises(ValueError):
                    DOWNLOADER.parse_manifest(manifest, root)

    def test_existing_corruption_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            target = root / '2015' / 'bad.csv'
            target.parent.mkdir()
            target.write_bytes(b'corrupt')
            expected = b'correct'
            args = types.SimpleNamespace(attempts=2, curl_timeout=5)
            item = (1, target, len(expected), hashlib.md5(expected).hexdigest(),
                    'https://example.invalid/data')
            with patch.object(subprocess, 'run', side_effect=AssertionError('should not fetch')):
                with self.assertRaises(ValueError):
                    DOWNLOADER.download_one(item, 1, args)
            self.assertEqual(target.read_bytes(), b'corrupt')


if __name__ == '__main__':
    unittest.main()
