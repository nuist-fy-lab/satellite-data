import hashlib
import http.server
import importlib.util
import os
import sqlite3
import socketserver
import tempfile
import threading
import unittest
from unittest import mock


SCRIPT = os.path.join(os.path.dirname(__file__), "..", "verify-earthcare-archive.py")
SPEC = importlib.util.spec_from_file_location("verify_earthcare_archive", SCRIPT)
verify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(verify)


class HistoricalVerificationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = os.path.join(self.temp.name, "EarthCare")
        directory = os.path.join(self.root, "ATL_CTH_2A", "20260707")
        os.makedirs(directory)
        self.name = "ECA_EXBC_ATL_CTH_2A_20260707T194739Z_20260725T103715Z_11979C.h5"
        self.path = os.path.join(directory, self.name)
        self.content = b"\x89HDF\r\n\x1a\n" + b"example"
        with open(self.path, "wb") as output:
            output.write(self.content)
        self.size = len(self.content)
        self.mtime = os.stat(self.path).st_mtime_ns
        self.index = sqlite3.connect(os.path.join(self.temp.name, "index.sqlite3"))
        self.addCleanup(self.index.close)
        self.index.executescript("""
            CREATE TABLE local_files(path TEXT PRIMARY KEY,filename TEXT,size INTEGER,mtime_ns INTEGER,state TEXT);
            CREATE TABLE products(product_key TEXT PRIMARY KEY,source_id TEXT,data_type TEXT);
            CREATE TABLE official_assets(product_key TEXT,granule_id TEXT,filename TEXT,size INTEGER,url TEXT,slot_ts TEXT);
            CREATE TABLE verified_archive_files(path TEXT PRIMARY KEY,size INTEGER,mtime_ns INTEGER,sha256 TEXT,
                source_id TEXT,official_collection TEXT,official_item TEXT,official_asset TEXT,verified_at TEXT);
        """)
        self.index.execute("INSERT INTO local_files VALUES(?,?,?,?,'unverified')", (self.path, self.name, self.size, self.mtime))
        self.item = "earthcare:" + self.name[:-3]
        self.url = "https://catalog.maap.eo.esa.int/data/earthcare/example.h5"
        for tier in ("L2Validated_MAAP", "L2InstChecked_MAAP"):
            key = "earthcare:EarthCARE%s:ATL_CTH_2A" % tier
            self.index.execute("INSERT INTO products VALUES(?,'earthcare','ATL_CTH_2A')", (key,))
            self.index.execute("INSERT INTO official_assets VALUES(?,?,?,?,?,?)",
                               (key, self.item, self.name, self.size, self.url, "2026-07-07T19:47:39Z"))
        self.index.commit()
        self.progress = verify.progress_db(os.path.join(self.temp.name, "progress.sqlite3"))
        self.addCleanup(self.progress.close)

    def test_duplicate_access_collections_point_to_one_asset(self):
        collection, item, url = verify.official_asset(self.index, self.path, self.name, self.size, self.root)
        self.assertIn("Validated_MAAP", collection)
        self.assertEqual((item, url), (self.item, self.url))

    def test_verified_content_is_recorded_and_resumed(self):
        expected = hashlib.sha256(self.content).hexdigest()
        with mock.patch.object(verify, "official_digest", return_value=expected) as upstream:
            result = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
            self.assertEqual(result["verified"], 1)
            self.assertEqual(self.index.execute("SELECT sha256 FROM verified_archive_files").fetchone()[0], expected)
            verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
            self.assertEqual(upstream.call_count, 1)

    def test_mismatch_does_not_upgrade_and_is_audited(self):
        with mock.patch.object(verify, "official_digest", return_value="incorrect"):
            result = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.assertEqual(result["failed"], 1)
        self.assertEqual(self.index.execute("SELECT count(*) FROM verified_archive_files").fetchone()[0], 0)
        self.assertEqual(self.progress.execute("SELECT state FROM attempts").fetchone()[0], "pending_review")
        with mock.patch.object(verify, "official_digest") as upstream:
            paused = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.assertGreater(paused["paused_until"], 0)
        upstream.assert_not_called()

    def test_ambiguous_url_never_fetches_or_records(self):
        self.index.execute("INSERT INTO official_assets VALUES(?,?,?,?,?,?)", (
            "earthcare:EarthCAREL2Validated_MAAP:ATL_CTH_2A", self.item, self.name,
            self.size, "https://catalog.maap.eo.esa.int/data/earthcare/other.h5", "2026-07-07T19:47:39Z"))
        self.index.commit()
        with mock.patch.object(verify, "official_digest") as upstream:
            result = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.assertEqual(result["unmatched"], 1)
        upstream.assert_not_called()

    def test_path_and_acquisition_day_must_match(self):
        wrong = os.path.join(self.root, "ATL_CTH_2A", "20260708", self.name)
        self.assertIsNone(verify.official_asset(self.index, wrong, self.name, self.size, self.root))

    def test_unix_centre_stream_requires_full_200_response(self):
        payload = self.content

        class Handler(http.server.BaseHTTPRequestHandler):
            code = 200
            body = payload

            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                self.send_response(self.code)
                self.send_header("Content-Length", str(len(self.body)))
                self.end_headers()
                self.wfile.write(self.body)

            def log_message(self, *_):
                pass

        socket_path = os.path.join(self.temp.name, "centre.sock")
        server = socketserver.UnixStreamServer(socket_path, Handler)
        self.addCleanup(server.server_close)
        worker = threading.Thread(target=server.serve_forever)
        worker.daemon = True
        worker.start()
        self.addCleanup(server.shutdown)
        self.assertEqual(verify.official_digest(socket_path, self.url, self.size),
                         hashlib.sha256(payload).hexdigest())
        Handler.code = 401
        with self.assertRaises(ValueError):
            verify.official_digest(socket_path, self.url, self.size)
        Handler.code = 200
        Handler.body = payload[:-1]
        with self.assertRaises(ValueError):
            verify.official_digest(socket_path, self.url, self.size)


if __name__ == "__main__":
    unittest.main()
