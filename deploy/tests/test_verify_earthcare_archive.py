import hashlib
import http.server
import importlib.util
import os
import signal
import sqlite3
import socketserver
import tempfile
import threading
import unittest
from datetime import datetime, timedelta, timezone
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

    def relocate_monthly(self):
        old = self.path
        directory = os.path.join(self.root, "ATL_CTH_2A", "2026", "07")
        os.makedirs(directory, exist_ok=True)
        self.path = os.path.join(directory, self.name)
        os.rename(old, self.path)
        self.index.execute("UPDATE local_files SET path=? WHERE path=?", (self.path, old))
        self.index.commit()
        return old

    def test_month_directory_matches_acquisition_not_production_date(self):
        self.relocate_monthly()
        self.assertEqual(verify.archive_product_day(self.path, self.name, self.root), ("ATL_CTH_2A", "20260707"))
        self.assertIsNotNone(verify.official_asset(self.index, self.path, self.name, self.size, self.root))
        for suffix in ["2026/06", "2025/07", "2026/7", "2026/07/nested", "20260708"]:
            path = os.path.join(self.root, "ATL_CTH_2A", suffix, self.name)
            self.assertIsNone(verify.official_asset(self.index, path, self.name, self.size, self.root))
        self.assertIsNone(verify.archive_product_day(os.path.join(self.root, ".rollback", "2026", "07", self.name), self.name, self.root))

    def test_relocated_evidence_requires_local_hash_and_is_idempotent(self):
        expected = hashlib.sha256(self.content).hexdigest()
        with mock.patch.object(verify, "official_digest", return_value=expected):
            verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        old = self.relocate_monthly()
        with mock.patch.object(verify, "official_digest") as upstream:
            result = verify.repair_layout_evidence(self.index, self.progress, self.root)
            self.assertEqual(result["layout_restored"], 1)
            self.assertEqual(verify.repair_layout_evidence(self.index, self.progress, self.root)["layout_already_restored"], 1)
            upstream.assert_not_called()
        self.assertEqual(self.index.execute("SELECT sha256 FROM verified_archive_files WHERE path=?", (self.path,)).fetchone()[0], expected)
        self.assertEqual(self.progress.execute("SELECT state FROM attempts WHERE path=?", (old,)).fetchone()[0], "relocated")
        self.assertEqual(self.progress.execute("SELECT state FROM attempts WHERE path=?", (self.path,)).fetchone()[0], "matched")

    def test_relocated_modified_file_is_not_promoted(self):
        expected = hashlib.sha256(self.content).hexdigest()
        with mock.patch.object(verify, "official_digest", return_value=expected):
            verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.relocate_monthly()
        with open(self.path, "wb") as output:
            output.write(self.content[:-1] + b"X")
        os.utime(self.path, ns=(self.mtime, self.mtime))
        self.assertEqual(verify.repair_layout_evidence(self.index, self.progress, self.root)["layout_rejected"], 1)
        self.assertIsNone(self.index.execute("SELECT sha256 FROM verified_archive_files WHERE path=?", (self.path,)).fetchone())
        self.assertEqual(self.progress.execute("SELECT state FROM attempts WHERE path=?", (self.path,)).fetchone()[0], "pending_review")

    def test_duplicate_relocated_candidates_are_not_promoted(self):
        expected = hashlib.sha256(self.content).hexdigest()
        with mock.patch.object(verify, "official_digest", return_value=expected):
            verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.relocate_monthly()
        alternate = os.path.join(self.root, "ATL_CTH_2A", "20260707", self.name)
        self.index.execute("INSERT INTO local_files VALUES(?,?,?,?,'unverified')", (alternate, self.name, self.size, self.mtime))
        self.index.commit()
        self.assertEqual(verify.repair_layout_evidence(self.index, self.progress, self.root)["layout_rejected"], 1)
        self.assertIsNone(self.index.execute("SELECT sha256 FROM verified_archive_files WHERE path=?", (self.path,)).fetchone())

    def test_layout_migration_resets_only_monthly_unmatched_cache_once(self):
        self.relocate_monthly()
        self.progress.execute("INSERT INTO attempts(path,size,mtime_ns,state,detail,checked_at) VALUES(?,?,?,'no_unique_official_asset','',0)", (self.path, self.size, self.mtime))
        terminal = self.path + '.terminal'
        self.progress.execute("INSERT INTO attempts(path,size,mtime_ns,state,detail,checked_at) VALUES(?,?,?,'pending_review','official content SHA-256 mismatch',0)", (terminal, self.size, self.mtime))
        self.progress.execute("INSERT INTO control VALUES(1,9999999999)")
        self.progress.commit()
        verify.repair_layout_evidence(self.index, self.progress, self.root)
        self.assertIsNone(self.progress.execute("SELECT state FROM attempts WHERE path=?", (self.path,)).fetchone())
        self.assertEqual(self.progress.execute("SELECT state FROM attempts WHERE path=?", (terminal,)).fetchone()[0], "pending_review")
        self.assertEqual(self.progress.execute("SELECT paused_until FROM control").fetchone()[0], 0)
        self.progress.execute("UPDATE control SET paused_until=123 WHERE id=1")
        self.progress.commit()
        verify.repair_layout_evidence(self.index, self.progress, self.root)
        self.assertEqual(self.progress.execute("SELECT paused_until FROM control WHERE id=1").fetchone()[0], 123)

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

    def test_transient_failure_retries_with_jitter_and_respects_retry_after(self):
        expected = hashlib.sha256(self.content).hexdigest()
        with mock.patch.object(verify.time, "time", return_value=1000), mock.patch.object(
            verify.random, "uniform", return_value=45
        ), mock.patch.object(verify, "official_digest", side_effect=[
            verify.RetryableOfficialError("official fetch HTTP 502", 90), expected
        ]) as upstream:
            first = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
            self.assertEqual((first["verified"], first["retrying"]), (0, 1))
            self.assertEqual(self.progress.execute(
                "SELECT state,retry_count,next_retry_at FROM attempts").fetchone(),
                ("retry_scheduled", 1, 1090))
            self.assertEqual(self.index.execute("SELECT count(*) FROM verified_archive_files").fetchone()[0], 0)
        with mock.patch.object(verify.time, "time", return_value=1089), mock.patch.object(
            verify, "official_digest"
        ) as upstream:
            self.assertIn("paused_until", verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60))
            upstream.assert_not_called()
        with mock.patch.object(verify.time, "time", return_value=1090), mock.patch.object(
            verify, "official_digest", return_value=expected
        ) as upstream:
            retried = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
            self.assertEqual(retried["verified"], 1)
            upstream.assert_called_once()
        self.assertEqual(self.progress.execute("SELECT state,retry_count FROM attempts").fetchone(), ("matched", 0))
        self.assertEqual(self.index.execute("SELECT count(*) FROM verified_archive_files").fetchone()[0], 1)

    def test_transient_failure_defers_after_four_attempts_then_recovers(self):
        with mock.patch.object(verify, "retry_delay", return_value=60), mock.patch.object(
            verify, "official_digest", side_effect=verify.RetryableOfficialError("official fetch HTTP 502")
        ) as upstream:
            for number in range(4):
                with mock.patch.object(verify.time, "time", return_value=1000 + number * 60):
                    result = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
                    self.assertEqual(result["retrying"], 1)
            with mock.patch.object(verify.time, "time", return_value=1300):
                verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.assertEqual(upstream.call_count, 4)
        row = self.progress.execute("SELECT state,retry_count,next_retry_at FROM attempts").fetchone()
        self.assertEqual(row[:2], ("retry_deferred", 4))
        self.assertGreaterEqual(row[2], 1180 + int(verify.DEFERRED_RETRY_SECONDS * 0.8))
        self.assertEqual(self.index.execute("SELECT count(*) FROM verified_archive_files").fetchone()[0], 0)
        with mock.patch.object(verify.time, "time", return_value=row[2]), mock.patch.object(
            verify, "official_digest", return_value=hashlib.sha256(self.content).hexdigest()
        ):
            self.assertEqual(verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)["verified"], 1)

    def test_deferred_retry_is_bounded_and_honors_retry_after(self):
        for attempt in range(4, 32):
            state, delay = verify.transient_retry(attempt)
            self.assertEqual(state, "retry_deferred")
            self.assertGreaterEqual(delay, int(verify.DEFERRED_RETRY_SECONDS * 0.8))
            self.assertLessEqual(delay, 86400)
        self.assertEqual(verify.transient_retry(4, 86400), ("retry_deferred", 86400))

    def test_real_writer_collision_retains_proof_and_does_not_refetch(self):
        holder = sqlite3.connect(os.path.join(self.temp.name, "index.sqlite3"))
        self.addCleanup(holder.close)
        holder.execute("BEGIN IMMEDIATE")
        self.index.execute("PRAGMA busy_timeout=1")
        expected = hashlib.sha256(self.content).hexdigest()
        with mock.patch.object(verify.time, "sleep"), mock.patch.object(verify, "retry_delay", return_value=60), mock.patch.object(
            verify, "official_digest", return_value=expected
        ) as upstream:
            with mock.patch.object(verify.time, "time", return_value=1000):
                first = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60, pause_seconds=3)
            self.assertEqual((first["failed"], first["retrying"]), (1, 1))
            self.assertEqual((first["gap_ms"], first["file_limit"]), (3000, 1))
            self.assertEqual(self.progress.execute("SELECT state,sha256 FROM attempts").fetchone(),
                             ("record_pending", expected))
            self.assertEqual(self.index.execute("SELECT count(*) FROM verified_archive_files").fetchone()[0], 0)
            holder.rollback()
            with mock.patch.object(verify.time, "time", return_value=1060):
                second = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60, pause_seconds=3)
            self.assertEqual((second["verified"], second["fetched"], second["bytes_requested"]), (1, 0, 0))
            self.assertEqual(upstream.call_count, 1)
            self.assertEqual(self.progress.execute("SELECT state FROM attempts").fetchone()[0], "matched")

    def test_retained_proof_rechecks_local_content_and_catalogue(self):
        proof = verify.official_asset(self.index, self.path, self.name, self.size, self.root)
        self.progress.execute("INSERT INTO attempts(path,size,mtime_ns,state,detail,checked_at,"
                              "official_collection,official_item,official_url,sha256) "
                              "VALUES(?,?,?,'record_pending','database is locked',0,?,?,?,?)",
                              (self.path, self.size, self.mtime) + proof + ("0" * 64,))
        self.progress.commit()
        with mock.patch.object(verify, "official_digest") as upstream:
            self.assertEqual(verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)["retrying"], 0)
        upstream.assert_not_called()
        self.assertEqual(self.index.execute("SELECT count(*) FROM verified_archive_files").fetchone()[0], 0)
        self.assertEqual(self.progress.execute("SELECT state,detail FROM attempts").fetchone(),
                         ("pending_review", "official content SHA-256 mismatch"))

    def test_transient_review_migration_is_once_and_preserves_terminal_failures(self):
        self.progress.execute("DELETE FROM migrations")
        details = ['database is locked', 'official stream exceeded time budget',
                   'official content SHA-256 mismatch', 'official fetch HTTP 403']
        for detail in details:
            self.progress.execute("INSERT INTO attempts(path,size,mtime_ns,state,detail,checked_at) "
                                  "VALUES(?,?,?,'pending_review',?,0)", (detail, 1, 1, detail))
        self.progress.commit()
        upgraded = verify.progress_db(os.path.join(self.temp.name, "progress.sqlite3"))
        states = dict(upgraded.execute("SELECT detail,state FROM attempts"))
        self.assertEqual(states[details[0]], "retry_scheduled")
        self.assertEqual(states[details[1]], "retry_deferred")
        self.assertEqual(states[details[2]], "pending_review")
        self.assertEqual(states[details[3]], "pending_review")
        upgraded.execute("UPDATE attempts SET state='pending_review' WHERE detail=?", (details[0],))
        upgraded.commit()
        upgraded.close()
        reopened = verify.progress_db(os.path.join(self.temp.name, "progress.sqlite3"))
        self.assertEqual(reopened.execute("SELECT state FROM attempts WHERE detail=?", (details[0],)).fetchone()[0],
                         "pending_review")
        reopened.close()

    def test_existing_502_record_is_migrated_once(self):
        path = os.path.join(self.temp.name, "legacy.sqlite3")
        legacy = sqlite3.connect(path)
        legacy.execute("CREATE TABLE attempts(path TEXT PRIMARY KEY,size INTEGER,mtime_ns INTEGER,"
                       "state TEXT,detail TEXT,checked_at INTEGER,official_collection TEXT,"
                       "official_item TEXT,official_url TEXT,sha256 TEXT)")
        legacy.execute("INSERT INTO attempts VALUES(?,?,?,'pending_review','official fetch HTTP 502',"
                       "1000,'','','','')", (self.path, self.size, self.mtime))
        legacy.commit()
        legacy.close()
        upgraded = verify.progress_db(path)
        self.assertEqual(upgraded.execute("SELECT state,retry_count,next_retry_at FROM attempts").fetchone(),
                         ("retry_scheduled", 1, 1060))
        upgraded.close()
        upgraded = verify.progress_db(path)
        self.assertEqual(upgraded.execute("SELECT state,retry_count,next_retry_at FROM attempts").fetchone(),
                         ("retry_scheduled", 1, 1060))
        upgraded.close()

    def test_retry_after_supports_delta_and_date(self):
        self.assertEqual(verify.retry_after_seconds("120"), 120)
        self.assertEqual(verify.retry_after_seconds("9" * 300), 86400)
        self.assertEqual(verify.retry_after_seconds("invalid"), 0)
        with mock.patch.object(verify.time, "time", return_value=1000):
            date = (datetime.fromtimestamp(1000, timezone.utc) + timedelta(seconds=240))
            self.assertEqual(verify.retry_after_seconds(date.strftime("%a, %d %b %Y %H:%M:%S GMT")), 240)

    def test_slow_trickle_stream_is_bounded_and_retried(self):
        response = mock.Mock(status=200)
        response.getheader.return_value = None
        response.read1.return_value = b"x"
        connection = mock.Mock()
        connection.getresponse.return_value = response
        with mock.patch.object(verify, "UnixHTTP", return_value=connection), mock.patch.object(
            verify.time, "monotonic", side_effect=[0, 0, 200]
        ):
            with self.assertRaisesRegex(verify.RetryableOfficialError, "time budget"):
                verify.official_digest("unused", self.url, 10)
        self.assertEqual(response.read1.call_count, 1)
        connection.close.assert_called_once()

    def test_hard_deadline_interrupts_blocking_official_read(self):
        response = mock.Mock(status=200)
        response.getheader.return_value = None
        response.read1.side_effect = lambda _: signal.pause()
        connection = mock.Mock()
        connection.getresponse.return_value = response
        with mock.patch.object(verify, "UnixHTTP", return_value=connection), mock.patch.object(
            verify, "MAX_OFFICIAL_SECONDS", 0.02
        ):
            with self.assertRaisesRegex(verify.RetryableOfficialError, "time budget"):
                verify.official_digest("unused", self.url, 10)
        connection.close.assert_called_once()

    def test_permanent_official_error_never_retries(self):
        with mock.patch.object(verify, "official_digest", side_effect=ValueError("official fetch HTTP 401")) as upstream:
            self.assertEqual(verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)["retrying"], 0)
            with mock.patch.object(verify.time, "time", return_value=9999999999):
                verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.assertEqual(upstream.call_count, 1)

    def test_adaptive_pacing_has_limits_and_recovers(self):
        pace = (3000, 20, 0.0, 0)
        pace = verify.adjust_pacing(*pace, False, 40)
        self.assertEqual(pace[:2], (4500, 10))
        for _ in range(12):
            pace = verify.adjust_pacing(*pace, False, 40)
        self.assertEqual(pace[:2], (12000, 5))
        for _ in range(128):
            pace = verify.adjust_pacing(*pace, True, 40)
        self.assertEqual(pace[0], 1000)
        self.assertGreater(pace[1], 20)
        self.assertLessEqual(pace[1], 40)
        for _ in range(64):
            pace = verify.adjust_pacing(*pace, True, 40)
        self.assertEqual(pace[:2], (1000, 40))

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

    def test_empty_sweep_pauses_official_requests(self):
        self.index.execute("UPDATE local_files SET state='ok'")
        self.index.commit()
        with mock.patch.object(verify, "official_digest") as upstream:
            result = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
            self.assertEqual(result["fetched"], 0)
            paused = verify.run(self.index, self.progress, self.root, "unused", 1, 1024, 60)
        self.assertGreater(paused["paused_until"], 0)
        upstream.assert_not_called()

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
                try:
                    self.wfile.write(self.body)
                except BrokenPipeError:
                    pass

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
        Handler.code = 429
        with self.assertRaises(verify.RetryableOfficialError):
            verify.official_digest(socket_path, self.url, self.size)
        Handler.code = 200
        Handler.body = payload[:-1]
        with self.assertRaises(ValueError):
            verify.official_digest(socket_path, self.url, self.size)


if __name__ == "__main__":
    unittest.main()
