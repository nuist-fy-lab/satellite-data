#!/usr/bin/env python3
"""Bounded, resumable verification of historical EarthCARE files against ESA assets."""

import argparse
import email.utils
import fcntl
import hashlib
import http.client
import json
import math
import os
import random
import re
import socket
import sqlite3
import stat
import threading
import time
from collections import OrderedDict
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from datetime import datetime, timezone


ACQUISITION = re.compile(r"_(\d{8})T\d{6}Z")
CHUNK = 128 * 1024
MAX_OFFICIAL_SECONDS = 600
OFFICIAL_IDLE_SECONDS = 60
MAX_TRANSIENT_ATTEMPTS = 4
MAX_RETRY_DELAY = 1800
DEFERRED_RETRY_SECONDS = 6 * 3600
MIN_GAP_MS = 1000
MAX_GAP_MS = 12000
MIN_BATCH_FILES = 5


class RetryableOfficialError(ValueError):
    def __init__(self, message, retry_after=0):
        super().__init__(message)
        self.retry_after = retry_after


def retry_after_seconds(value):
    if not value:
        return 0
    value = value.strip()
    if re.fullmatch(r"[0-9]+", value):
        return 86400 if len(value) > 5 else min(int(value), 86400)
    try:
        date = email.utils.parsedate_to_datetime(value)
        return min(86400, max(0, math.ceil(date.timestamp() - time.time())))
    except (TypeError, ValueError, OverflowError):
        return 0


def retry_delay(attempt, retry_after=0):
    ceiling = min(MAX_RETRY_DELAY, 60 * (2 ** (attempt - 1)))
    return max(int(random.uniform(ceiling / 2, ceiling)), retry_after, 1)


def transient_retry(attempt, retry_after=0):
    if attempt < MAX_TRANSIENT_ATTEMPTS:
        return "retry_scheduled", retry_delay(attempt, retry_after)
    # Exhausted short retries become sparse probes, not permanent failures.
    ceiling = min(86400, DEFERRED_RETRY_SECONDS * (2 ** min(attempt - MAX_TRANSIENT_ATTEMPTS, 2)))
    return "retry_deferred", max(int(random.uniform(ceiling * 0.8, ceiling)), retry_after)


def database_busy(exc):
    return isinstance(exc, sqlite3.OperationalError) and str(exc) in (
        "database is locked", "database is busy", "database table is locked")


def adjust_pacing(gap_ms, file_limit, error_rate, streak, success, max_files, recovery_streak=8):
    error_rate = error_rate * 0.8 + (0 if success else 0.2)
    if not success:
        streak = 0
        if error_rate >= 0.2:
            gap_ms = min(MAX_GAP_MS, max(MIN_GAP_MS, int(gap_ms * 1.5)))
            file_limit = max(min(MIN_BATCH_FILES, max_files), file_limit // 2)
    else:
        streak += 1
        if streak >= recovery_streak and error_rate < 0.1:
            gap_ms = max(MIN_GAP_MS, int(gap_ms * 0.8))
            file_limit = min(max_files, file_limit + 2)
            streak = 0
    return gap_ms, file_limit, error_rate, streak


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, socket_path):
        super().__init__("unix", timeout=OFFICIAL_IDLE_SECONDS)
        self.socket_path = socket_path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(OFFICIAL_IDLE_SECONDS)
        self.sock.connect(self.socket_path)


def candidates(db, root, cursor, max_file_bytes, limit=128):
    return db.execute(
        "SELECT path,filename,size,mtime_ns FROM local_files "
        "WHERE path>? AND path LIKE ? AND state='unverified' AND size BETWEEN 1 AND ? "
        "ORDER BY path LIMIT ?", (cursor, root + "/%", max_file_bytes, limit)
    ).fetchall()


def archive_product_day(path, name, root):
    rel = os.path.relpath(path, root).split(os.sep)
    match = ACQUISITION.search(name)
    if len(rel) not in (3, 4) or rel[-1] != name or not match or rel[0].startswith((".", "_")):
        return None
    try:
        day = datetime.strptime(match.group(1), "%Y%m%d").strftime("%Y%m%d")
    except ValueError:
        return None
    if day != match.group(1):
        return None
    if len(rel) == 3 and rel[1] != day:
        return None
    if len(rel) == 4 and rel[1:3] != [day[:4], day[4:6]]:
        return None
    return rel[0], day


def asset_reference(rows, day):
    refs = {(row[1], row[2]) for row in rows if row[3][:10] == day[:4] + "-" + day[4:6] + "-" + day[6:]}
    if len(refs) != 1:
        return None
    item, url = refs.pop()
    if not url.startswith("https://catalog.maap.eo.esa.int/data/"):
        return None
    # Several access-class collections can expose the same physical asset.
    matching = sorted(row[0] for row in rows if row[1] == item and row[2] == url)
    preferred = next((key for key in matching if "Validated_MAAP:" in key), matching[0])
    return preferred, item, url


def official_asset(db, path, name, size, root):
    identity = archive_product_day(path, name, root)
    if identity is None:
        return None
    product, day = identity
    rows = db.execute(
        "SELECT a.product_key,a.granule_id,a.url,a.slot_ts FROM official_assets a "
        "JOIN products p ON p.product_key=a.product_key "
        "WHERE a.filename=? AND a.size=? AND p.source_id='earthcare' AND p.data_type=?",
        (name, size, product),
    ).fetchall()
    return asset_reference(rows, day)


class OfficialAssetLRU:
    """Cache bounded daily catalogues; evidence writes always query live references."""

    def __init__(self, db, root, capacity=16, max_group_rows=4096):
        self.db, self.root = db, root
        self.capacity, self.max_group_rows = capacity, max_group_rows
        self.entries = OrderedDict()
        self.version = None
        self.hits = self.misses = self.evictions = 0

    def lookup(self, path, name, size):
        identity = archive_product_day(path, name, self.root)
        if identity is None:
            return None
        version = self.db.execute("PRAGMA data_version").fetchone()[0]
        if version != self.version:
            self.entries.clear()
            self.version = version
        if identity in self.entries:
            self.hits += 1
            self.entries.move_to_end(identity)
            group = self.entries[identity]
        else:
            self.misses += 1
            product, day = identity
            stamp = day[:4] + "-" + day[4:6] + "-" + day[6:]
            rows = self.db.execute(
                "SELECT a.filename,a.size,a.product_key,a.granule_id,a.url,a.slot_ts "
                "FROM official_assets a JOIN products p ON p.product_key=a.product_key "
                "WHERE p.source_id='earthcare' AND p.data_type=? AND a.slot_ts LIKE ? LIMIT ?",
                (product, stamp + "%", self.max_group_rows + 1)).fetchall()
            if len(rows) > self.max_group_rows:
                return official_asset(self.db, path, name, size, self.root)
            group = {}
            for filename, length, *reference in rows:
                group.setdefault((filename, length), []).append(reference)
            self.entries[identity] = group
            if len(self.entries) > self.capacity:
                self.entries.popitem(last=False)
                self.evictions += 1
        return asset_reference(group.get((name, size), []), identity[1])


def local_digest(path, root, expected_size, expected_mtime):
    if os.path.commonpath((os.path.realpath(path), os.path.realpath(root))) != os.path.realpath(root):
        raise ValueError("file resolves outside archive root")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_size != expected_size or before.st_mtime_ns != expected_mtime:
            raise ValueError("local file changed before verification")
        digest = hashlib.sha256()
        with os.fdopen(fd, "rb", closefd=False) as source:
            for chunk in iter(lambda: source.read(CHUNK), b""):
                digest.update(chunk)
        after = os.fstat(fd)
        if (before.st_size, before.st_mtime_ns, before.st_ino) != (after.st_size, after.st_mtime_ns, after.st_ino):
            raise ValueError("local file changed during verification")
        return digest.hexdigest()
    finally:
        os.close(fd)


def official_digest(socket_path, url, size, deadline=None):
    connection = UnixHTTP(socket_path)
    # Allow larger assets proportionally more time, but never an unbounded slow stream.
    budget = min(MAX_OFFICIAL_SECONDS, max(180, (size + (256 << 10) - 1) // (256 << 10)))
    now = time.monotonic()
    deadline = min(now + budget, deadline) if deadline is not None else now + budget
    budget = max(0.001, deadline - now)
    expired = threading.Event()
    stream_socket = [None]

    def interrupt():
        expired.set()
        active = stream_socket[0] or connection.sock
        if active is not None:
            try:
                active.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass

    timer = threading.Timer(budget, interrupt)
    timer.daemon = True
    try:
        timer.start()
        payload = json.dumps({"source_id": "earthcare", "url": url}).encode("utf-8")
        connection.request("POST", "/v1/fetch", payload, {"Content-Type": "application/json"})
        # getresponse may detach a Connection: close socket while its body is still open.
        stream_socket[0] = connection.sock
        if expired.is_set():
            raise RetryableOfficialError("official stream exceeded time budget")
        response = connection.getresponse()
        if response.status in (408, 429) or 500 <= response.status < 600:
            error = RetryableOfficialError("official fetch HTTP %d" % response.status,
                                          retry_after_seconds(response.getheader("Retry-After")))
            error.source_pause = response.status == 429 or error.retry_after > 0
            raise error
        if response.status != 200:
            raise ValueError("official fetch HTTP %d" % response.status)
        if response.getheader("Content-Length") and int(response.getheader("Content-Length")) != size:
            raise ValueError("official length differs from catalog")
        digest = hashlib.sha256()
        count = 0
        while count <= size:
            if time.monotonic() >= deadline:
                raise RetryableOfficialError("official stream exceeded time budget")
            chunk = response.read1(min(CHUNK, size + 1 - count))
            if not chunk:
                break
            count += len(chunk)
            digest.update(chunk)
        if count != size:
            if expired.is_set():
                raise RetryableOfficialError("official stream exceeded time budget")
            raise RetryableOfficialError("official stream size differs from catalog")
        return digest.hexdigest()
    except (OSError, http.client.HTTPException):
        if expired.is_set():
            raise RetryableOfficialError("official stream exceeded time budget")
        raise
    finally:
        timer.cancel()
        timer.join()
        connection.close()


def record(db, path, name, size, mtime, digest, collection, item, url, root, manage_transaction=True):
    if manage_transaction:
        db.execute("BEGIN IMMEDIATE")
    try:
        row = db.execute("SELECT filename,size,mtime_ns,state FROM local_files WHERE path=?", (path,)).fetchone()
        if row != (name, size, mtime, "unverified"):
            raise ValueError("indexed file changed before recording")
        if official_asset(db, path, name, size, root) != (collection, item, url):
            raise ValueError("official reference changed before recording")
        real_root = os.path.realpath(root)
        if os.path.commonpath((os.path.realpath(path), real_root)) != real_root:
            raise ValueError("file resolves outside archive root")
        current = os.stat(path, follow_symlinks=False)
        if not stat.S_ISREG(current.st_mode) or (current.st_size, current.st_mtime_ns) != (size, mtime):
            raise ValueError("file metadata changed before recording")
        db.execute(
            "INSERT INTO verified_archive_files "
            "(path,size,mtime_ns,sha256,source_id,official_collection,official_item,official_asset,verified_at) "
            "VALUES(?,?,?,?,'earthcare',?,?,?,?) ON CONFLICT(path) DO UPDATE SET "
            "size=excluded.size,mtime_ns=excluded.mtime_ns,sha256=excluded.sha256,"
            "official_collection=excluded.official_collection,official_item=excluded.official_item,"
            "official_asset=excluded.official_asset,verified_at=excluded.verified_at",
            (path, size, mtime, digest, collection, item, name, datetime.now(timezone.utc).isoformat()),
        )
        if manage_transaction:
            db.commit()
    except BaseException:
        if manage_transaction:
            db.rollback()
        raise


def progress_db(path):
    db = sqlite3.connect(path, timeout=30)
    db.execute("PRAGMA busy_timeout=30000")
    db.execute("CREATE TABLE IF NOT EXISTS attempts (path TEXT PRIMARY KEY, size INTEGER NOT NULL, "
               "mtime_ns INTEGER NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL, checked_at INTEGER NOT NULL, "
               "official_collection TEXT NOT NULL DEFAULT '', official_item TEXT NOT NULL DEFAULT '', "
               "official_url TEXT NOT NULL DEFAULT '', sha256 TEXT NOT NULL DEFAULT '')")
    db.execute("CREATE TABLE IF NOT EXISTS cursor (id INTEGER PRIMARY KEY CHECK(id=1), path TEXT NOT NULL)")
    db.execute("CREATE TABLE IF NOT EXISTS control (id INTEGER PRIMARY KEY CHECK(id=1), "
               "paused_until INTEGER NOT NULL DEFAULT 0)")
    db.execute("CREATE TABLE IF NOT EXISTS inflight (path TEXT PRIMARY KEY,size INTEGER NOT NULL, "
               "mtime_ns INTEGER NOT NULL)")
    db.execute("CREATE TABLE IF NOT EXISTS pacing (id INTEGER PRIMARY KEY CHECK(id=1), "
               "gap_ms INTEGER NOT NULL,file_limit INTEGER NOT NULL,error_rate REAL NOT NULL,"
               "success_streak INTEGER NOT NULL)")
    columns = {row[1] for row in db.execute("PRAGMA table_info(attempts)")}
    if "retry_count" not in columns:
        db.execute("ALTER TABLE attempts ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0")
    if "next_retry_at" not in columns:
        db.execute("ALTER TABLE attempts ADD COLUMN next_retry_at INTEGER NOT NULL DEFAULT 0")
    if "retry_count" not in columns:
        # Upgrade historical transient HTTP failures without reclassifying terminal ones on later runs.
        db.execute("UPDATE attempts SET state='retry_scheduled',retry_count=1,next_retry_at=checked_at+60 "
                   "WHERE state='pending_review' AND (detail GLOB 'official fetch HTTP 5[0-9][0-9]' "
                   "OR detail IN ('official fetch HTTP 408','official fetch HTTP 429'))")
    db.execute("CREATE INDEX IF NOT EXISTS attempts_retry_due ON attempts(state,next_retry_at)")
    db.execute("CREATE TABLE IF NOT EXISTS migrations (name TEXT PRIMARY KEY)")
    migration = "recover_transient_review_v1"
    if db.execute("SELECT 1 FROM migrations WHERE name=?", (migration,)).fetchone() is None:
        # Old lock failures did not retain proof; they must fetch and hash again.
        db.execute("UPDATE attempts SET state='retry_scheduled',retry_count=0,next_retry_at=? "
                   "WHERE state='pending_review' AND detail IN "
                   "('database is locked','database is busy','database table is locked')", (int(time.time()),))
        db.execute("UPDATE attempts SET state='retry_deferred',next_retry_at=max(?,checked_at+?) "
                   "WHERE state='pending_review' AND (detail GLOB 'official fetch HTTP 5[0-9][0-9]' "
                   "OR detail IN ('official fetch HTTP 408','official fetch HTTP 429',"
                   "'official stream exceeded time budget','official stream size differs from catalog','timed out'))",
                   (int(time.time()), DEFERRED_RETRY_SECONDS))
        db.execute("INSERT INTO migrations VALUES(?)", (migration,))
    db.commit()
    os.chmod(path, 0o600)
    return db


def restore_evidence_batch(index, progress, root, entries):
    accepted, failures = [], []
    index.execute("BEGIN IMMEDIATE")
    try:
        for entry in entries:
            oldpath, path, name, size, mtime, digest, reference, already = entry
            index.execute("SAVEPOINT relocated_file")
            try:
                if not already:
                    record(index, path, name, size, mtime, digest, *reference, root, manage_transaction=False)
                index.execute("RELEASE relocated_file")
                accepted.append(entry)
            except (OSError, ValueError, sqlite3.Error) as exc:
                index.execute("ROLLBACK TO relocated_file")
                index.execute("RELEASE relocated_file")
                failures.append((entry, str(exc)[:160]))
        index.commit()
    except BaseException:
        index.rollback()
        raise
    for oldpath, path, name, size, mtime, digest, reference, already in accepted:
        progress.execute("UPDATE attempts SET state='relocated',detail='proof restored after local SHA-256 check' "
                         "WHERE path=? AND state='matched'", (oldpath,))
        progress.execute("INSERT OR REPLACE INTO attempts "
                         "(path,size,mtime_ns,state,detail,checked_at,official_collection,official_item,official_url,sha256) "
                         "VALUES(?,?,?,'matched','',?,?,?,?,?)",
                         (path, size, mtime, int(time.time()), reference[0], reference[1], reference[2], digest))
    for entry, detail in failures:
        _, path, _, size, mtime, _, _, _ = entry
        progress.execute("INSERT OR REPLACE INTO attempts "
                         "(path,size,mtime_ns,state,detail,checked_at) VALUES(?,?,?,'pending_review',?,?)",
                         (path, size, mtime, detail, int(time.time())))
    progress.commit()
    already_count = sum(entry[-1] for entry in accepted)
    return len(accepted) - already_count, len(failures), already_count


def repair_layout_evidence(index, progress, root):
    """Reuse official proof only after hashing its unique relocated payload."""
    restored = rejected = already = 0
    pending = []
    evidence = index.execute(
        "SELECT v.path,v.size,v.sha256,v.official_collection,v.official_item "
        "FROM verified_archive_files v LEFT JOIN local_files f ON f.path=v.path "
        "WHERE v.source_id='earthcare' AND f.path IS NULL ORDER BY v.path"
    ).fetchall()
    for oldpath, size, digest, collection, item in evidence:
        # Downloader evidence stores STAC asset IDs, not necessarily filenames.
        name = os.path.basename(oldpath)
        identity = archive_product_day(oldpath, name, root)
        if identity is None or not re.fullmatch(r"[0-9a-fA-F]{64}", digest):
            rejected += 1
            continue
        product, day = identity
        matches = index.execute(
            "SELECT path,mtime_ns FROM local_files WHERE filename=? AND size=? "
            "AND path LIKE ? AND state IN ('unverified','ok')", (name, size, root + "/%")
        ).fetchall()
        matches = [(path, mtime) for path, mtime in matches
                   if archive_product_day(path, name, root) == identity]
        if len(matches) != 1:
            rejected += 1
            continue
        path, mtime = matches[0]
        previous = index.execute(
            "SELECT sha256 FROM verified_archive_files WHERE path=? AND size=? AND mtime_ns=?",
            (path, size, mtime)).fetchone()
        is_restored = bool(previous and previous[0].lower() == digest.lower())
        reference = official_asset(index, path, name, size, root)
        if reference is None or reference[:2] != (collection, item):
            rejected += 1
            continue
        try:
            if not is_restored and local_digest(path, root, size, mtime) != digest.lower():
                raise ValueError("relocated payload SHA-256 differs from official evidence")
        except (OSError, ValueError, sqlite3.Error) as exc:
            rejected += 1
            progress.execute("INSERT OR REPLACE INTO attempts "
                             "(path,size,mtime_ns,state,detail,checked_at) VALUES(?,?,?,'pending_review',?,?)",
                             (path, size, mtime, str(exc)[:160], int(time.time())))
            progress.commit()
            continue
        pending.append((oldpath, path, name, size, mtime, digest.lower(), reference, is_restored))
        if len(pending) >= 50:
            added, failed, existing = restore_evidence_batch(index, progress, root, pending)
            restored += added
            rejected += failed
            already += existing
            pending.clear()
            print(json.dumps({"layout_restored": restored, "layout_rejected": rejected,
                              "layout_already_restored": already}), flush=True)
    if pending:
        added, failed, existing = restore_evidence_batch(index, progress, root, pending)
        restored += added
        rejected += failed
        already += existing
    migration = "earthcare_month_layout_v1"
    if progress.execute("SELECT 1 FROM migrations WHERE name=?", (migration,)).fetchone() is None:
        for path, in progress.execute("SELECT path FROM attempts WHERE state='no_unique_official_asset'").fetchall():
            if len(os.path.relpath(path, root).split(os.sep)) == 4 and archive_product_day(path, os.path.basename(path), root):
                progress.execute("DELETE FROM attempts WHERE path=? AND state='no_unique_official_asset'", (path,))
        progress.execute("INSERT OR REPLACE INTO cursor VALUES(1,'')")
        progress.execute("INSERT OR REPLACE INTO control VALUES(1,0)")
        progress.execute("INSERT INTO migrations VALUES(?)", (migration,))
        progress.commit()
    return {"layout_restored": restored, "layout_already_restored": already, "layout_rejected": rejected}


def run(index, progress, root, socket_path, max_files, max_bytes, max_seconds,
        max_file_bytes=None, start_after="", pause_seconds=0):
    paused = progress.execute("SELECT paused_until FROM control WHERE id=1").fetchone()
    if paused and paused[0] > time.time():
        return {"inspected": 0, "fetched": 0, "verified": 0, "skipped": 0,
                "unmatched": 0, "failed": 0, "retrying": 0, "bytes_requested": 0,
                "paused_until": paused[0]}
    cursor_row = progress.execute("SELECT path FROM cursor WHERE id=1").fetchone()
    cursor = cursor_row[0] if cursor_row else start_after
    max_file_bytes = max_file_bytes or max_bytes
    adaptive = pause_seconds > 0
    pacing = progress.execute("SELECT gap_ms,file_limit,error_rate,success_streak FROM pacing WHERE id=1").fetchone()
    if adaptive:
        if pacing is None:
            pacing = (max(MIN_GAP_MS, min(MAX_GAP_MS, int(pause_seconds * 1000))),
                      min(20, max_files), 0.0, 0)
            progress.execute("INSERT INTO pacing VALUES(1,?,?,?,?)", pacing)
            progress.commit()
        gap_ms, file_limit, error_rate, streak = pacing
        gap_ms = max(MIN_GAP_MS, min(MAX_GAP_MS, gap_ms))
        file_limit = max(1, min(max_files, file_limit))
    else:
        gap_ms, file_limit, error_rate, streak = 0, max_files, 0.0, 0
    started = time.monotonic()
    matched = fetched = skipped = inspected = used = failed = unmatched = retrying = 0
    while inspected < 1000 and fetched < file_limit and time.monotonic() - started < max_seconds:
        due = progress.execute("SELECT path,size,mtime_ns FROM attempts "
                               "WHERE state IN ('retry_scheduled','retry_deferred','record_pending') AND next_retry_at<=? "
                               "AND path LIKE ? AND size<=? ORDER BY state='record_pending' DESC,next_retry_at LIMIT 1",
                               (int(time.time()), root + '/%', min(max_file_bytes, max_bytes - used))).fetchone()
        retry_row = None
        if due:
            retry_row = index.execute("SELECT path,filename,size,mtime_ns FROM local_files "
                                      "WHERE path=? AND size=? AND mtime_ns=? AND state='unverified'", due).fetchone()
            if retry_row is None:
                progress.execute("UPDATE attempts SET state='stale',next_retry_at=0 WHERE path=?", (due[0],))
                progress.commit()
                continue
        rows = [retry_row] if retry_row else candidates(index, root, cursor, max_file_bytes, 32)
        if not rows:
            if matched == 0 and fetched == 0:
                next_retry = progress.execute("SELECT min(next_retry_at) FROM attempts "
                                              "WHERE state IN ('retry_scheduled','retry_deferred','record_pending')").fetchone()[0]
                pause_until = min(int(time.time()) + 3600, next_retry) if next_retry else int(time.time()) + 3600
                progress.execute("INSERT OR REPLACE INTO control(id,paused_until) VALUES(1,?)",
                                 (pause_until,))
            progress.execute("INSERT OR REPLACE INTO cursor(id,path) VALUES(1,'')")
            progress.commit()
            break
        exhausted = False
        stop_after_failure = False
        for path, name, size, mtime in rows:
            if inspected >= 1000 or fetched >= file_limit or time.monotonic() - started >= max_seconds:
                exhausted = True
                break
            if not retry_row:
                cursor = path
            inspected += 1
            prior = progress.execute("SELECT size,mtime_ns,state,checked_at,retry_count,next_retry_at,"
                                     "official_collection,official_item,official_url,sha256 "
                                     "FROM attempts WHERE path=?", (path,)).fetchone()
            if prior and prior[:2] == (size, mtime) and (
                prior[2] in ("matched", "pending_review") or
                prior[2] in ("retry_scheduled", "retry_deferred", "record_pending") and prior[5] > time.time() or
                prior[2] == "no_unique_official_asset" and time.time() - prior[3] < 86400
            ):
                skipped += 1
            elif size > max_bytes - used:
                # Leave the oversized file for a later run with a larger byte limit.
                skipped += 1
            else:
                asset = official_asset(index, path, name, size, root)
                proof = ("", "", "", "")
                if asset is None:
                    state, detail = "no_unique_official_asset", ""
                    unmatched += 1
                else:
                    collection, item, url = asset
                    retry_count = next_retry_at = 0
                    phase = "local"
                    retaining_proof = prior and prior[:2] == (size, mtime) and prior[2] == "record_pending"
                    fetched_official = not retaining_proof
                    try:
                        if retaining_proof:
                            if prior[6:9] != asset or not re.fullmatch(r"[0-9a-f]{64}", prior[9]):
                                raise ValueError("saved official proof or reference changed")
                            remote = prior[9]
                        else:
                            fetched += 1
                            used += size
                            phase = "official"
                            remote = official_digest(socket_path, url, size)
                        phase = "local"
                        local = local_digest(path, root, size, mtime)
                        if local != remote:
                            raise ValueError("official content SHA-256 mismatch")
                        # Keep the compared proof even if the evidence write is busy.
                        proof = (collection, item, url, local)
                        phase = "record"
                        record(index, path, name, size, mtime, local, collection, item, url, root)
                        state, detail = "matched", ""
                        matched += 1
                    except (OSError, ValueError, http.client.HTTPException, sqlite3.Error) as exc:
                        state, detail = "pending_review", str(exc)[:160]
                        failed += 1
                        stop_after_failure = True
                        if phase == "official" and isinstance(exc, (RetryableOfficialError, OSError,
                                                                     http.client.HTTPException)):
                            retry_count = (prior[4] if prior and prior[:2] == (size, mtime) else 0) + 1
                            state, delay = transient_retry(retry_count, getattr(exc, "retry_after", 0))
                            next_retry_at = int(time.time()) + delay
                            retrying += 1
                        elif phase == "record" and database_busy(exc):
                            state = "record_pending"
                            retry_count = min(16, prior[4] + 1 if retaining_proof else 1)
                            next_retry_at = int(time.time()) + retry_delay(retry_count)
                            retrying += 1
                        progress.execute("INSERT OR REPLACE INTO control(id,paused_until) VALUES(1,?)",
                                         (int(time.time()) + max(60, getattr(exc, "retry_after", 0)) if state == "retry_deferred" else
                                          next_retry_at or int(time.time()) + 900,))
                    # A local writer collision says nothing about ESA's error rate.
                    if adaptive and fetched_official and state != "record_pending":
                        gap_ms, file_limit, error_rate, streak = adjust_pacing(
                            gap_ms, file_limit, error_rate, streak, state == "matched", max_files)
                        progress.execute("INSERT OR REPLACE INTO pacing VALUES(1,?,?,?,?)",
                                         (gap_ms, file_limit, error_rate, streak))
                    if gap_ms:
                        time.sleep(gap_ms / 1000)
                progress.execute("INSERT OR REPLACE INTO attempts "
                                 "(path,size,mtime_ns,state,detail,checked_at,official_collection,official_item,"
                                 "official_url,sha256,retry_count,next_retry_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
                                 (path, size, mtime, state, detail, int(time.time())) + proof +
                                 (retry_count if asset else 0, next_retry_at if asset else 0))
            progress.execute("INSERT OR REPLACE INTO cursor(id,path) VALUES(1,?)", (cursor,))
            progress.commit()
            if stop_after_failure:
                break
        if stop_after_failure:
            break
        if len(rows) < 32 and not exhausted and not retry_row:
            if matched == 0 and fetched == 0:
                progress.execute("INSERT OR REPLACE INTO control(id,paused_until) VALUES(1,?)",
                                 (int(time.time()) + 3600,))
            progress.execute("INSERT OR REPLACE INTO cursor(id,path) VALUES(1,'')")
            progress.commit()
            break
    return {"inspected": inspected, "fetched": fetched, "verified": matched, "skipped": skipped,
            "unmatched": unmatched, "failed": failed, "retrying": retrying, "bytes_requested": used,
            "gap_ms": gap_ms, "file_limit": file_limit}


def pipeline_content(row, asset, prior, root, socket_path, deadline):
    path, name, size, mtime = row
    retained = prior and prior[:2] == (size, mtime) and prior[2] == "record_pending"
    result = {"phase": "local", "proof": ("", "", "", ""), "error": None,
              "official_seconds": 0.0, "local_seconds": 0.0, "retained": bool(retained)}
    try:
        if retained:
            if prior[6:9] != asset or not re.fullmatch(r"[0-9a-f]{64}", prior[9]):
                raise ValueError("saved official proof or reference changed")
            remote = prior[9]
        else:
            result["phase"] = "official"
            started = time.monotonic()
            try:
                remote = official_digest(socket_path, asset[2], size, deadline)
            finally:
                result["official_seconds"] = time.monotonic() - started
        result["phase"] = "local"
        started = time.monotonic()
        try:
            local = local_digest(path, root, size, mtime)
        finally:
            result["local_seconds"] = time.monotonic() - started
        if local != remote:
            raise ValueError("official content SHA-256 mismatch")
        result["proof"] = asset + (local,)
        result["phase"] = "record"
    except (OSError, ValueError, http.client.HTTPException) as exc:
        result["error"] = exc
    return result


def run_pipeline(index, progress, root, socket_path, max_files, max_bytes, max_seconds,
                 max_file_bytes=None, start_after="", pause_seconds=2, waiting_slots=4, cache_size=16):
    if not 1 <= waiting_slots <= 8 or not 1 <= cache_size <= 128:
        raise ValueError("waiting slots must be 1-8 and LRU capacity must be 1-128")
    paused = progress.execute("SELECT paused_until FROM control WHERE id=1").fetchone()
    if paused and paused[0] > time.time():
        return {"failed": 0, "retrying": 0, "paused_until": paused[0]}
    max_file_bytes = min(max_file_bytes or max_bytes, max_bytes)
    saved = progress.execute("SELECT path FROM cursor WHERE id=1").fetchone()
    cursor = saved[0] if saved else start_after
    pacing = progress.execute("SELECT gap_ms,file_limit,error_rate,success_streak FROM pacing WHERE id=1").fetchone()
    gap_ms, file_limit, error_rate, streak = pacing or (int(pause_seconds * 1000), min(20, max_files), 0.0, 0)
    gap_ms = max(MIN_GAP_MS, min(MAX_GAP_MS, gap_ms)) if pause_seconds else 0
    batch_limit = min(max_files, max(file_limit, waiting_slots * 2))
    cache = OfficialAssetLRU(index, root, cache_size)
    deadline = time.monotonic() + max_seconds
    next_launch = time.monotonic()
    pending, seen = {}, set()
    inspected = submitted = fetched = used = verified = unmatched = skipped = failed = retrying = peak = 0
    official_seconds = local_seconds = record_seconds = 0.0
    admission_stopped = sweep_finished = False
    retry_turn = True

    def choose_row():
        nonlocal cursor, retry_turn, sweep_finished
        # Durable claims recover submissions after interruption, before advancing the scan.
        for path, size, mtime in progress.execute("SELECT path,size,mtime_ns FROM inflight ORDER BY path").fetchall():
            if path in seen:
                continue
            row = index.execute("SELECT path,filename,size,mtime_ns FROM local_files "
                                "WHERE path=? AND size=? AND mtime_ns=? AND state='unverified'",
                                (path, size, mtime)).fetchone()
            if row and row[2] <= max_file_bytes and path.startswith(root + os.sep):
                return row
            progress.execute("DELETE FROM inflight WHERE path=?", (path,))
            progress.commit()
        due = None
        for path, size, mtime in progress.execute(
                "SELECT path,size,mtime_ns FROM attempts WHERE state IN "
                "('retry_scheduled','retry_deferred','record_pending') AND next_retry_at<=? "
                "AND path LIKE ? AND size<=? ORDER BY state='record_pending' DESC,next_retry_at LIMIT 1000",
                (int(time.time()), root + '/%', max_file_bytes)).fetchall():
            if path in seen:
                continue
            due = index.execute("SELECT path,filename,size,mtime_ns FROM local_files "
                                "WHERE path=? AND size=? AND mtime_ns=? AND state='unverified'",
                                (path, size, mtime)).fetchone()
            if due:
                break
            progress.execute("UPDATE attempts SET state='stale',next_retry_at=0 WHERE path=?", (path,))
            progress.commit()
        if due and retry_turn:
            retry_turn = False
            return due
        rows = candidates(index, root, cursor, max_file_bytes, 1)
        if rows:
            retry_turn = True
            cursor = rows[0][0]
            return rows[0]
        sweep_finished = True
        return due

    def audit(row, state, detail="", proof=("", "", "", ""), count=0, due=0):
        path, name, size, mtime = row
        progress.execute("INSERT OR REPLACE INTO attempts "
                         "(path,size,mtime_ns,state,detail,checked_at,official_collection,official_item,"
                         "official_url,sha256,retry_count,next_retry_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
                         (path, size, mtime, state, detail, int(time.time())) + proof + (count, due))
        progress.execute("DELETE FROM inflight WHERE path=?", (path,))
        progress.commit()

    with ThreadPoolExecutor(max_workers=waiting_slots) as pool:
        while True:
            # Finalize whichever slot completes first; a slow first slot cannot block later proofs.
            for future in [f for f in pending if f.done()]:
                row, asset, prior = pending.pop(future)
                result = future.result()
                official_seconds += result["official_seconds"]
                local_seconds += result["local_seconds"]
                error = result["error"]
                if error is None:
                    started = time.monotonic()
                    try:
                        record(index, *row, result["proof"][3], *asset, root)
                    except (OSError, ValueError, sqlite3.Error) as exc:
                        error = exc
                    finally:
                        record_seconds += time.monotonic() - started
                state, count, due = "matched", 0, 0
                if error is None:
                    verified += 1
                else:
                    failed += 1
                    state = "pending_review"
                    if result["phase"] == "official" and isinstance(
                            error, (RetryableOfficialError, OSError, http.client.HTTPException)):
                        count = (prior[4] if prior and prior[:2] == (row[2], row[3]) else 0) + 1
                        state, delay = transient_retry(count, getattr(error, "retry_after", 0))
                        due = int(time.time()) + delay
                        retrying += 1
                    elif result["phase"] == "record" and database_busy(error):
                        state = "record_pending"
                        count = min(16, prior[4] + 1 if result["retained"] else 1)
                        due = int(time.time()) + retry_delay(count)
                        retrying += 1
                    if getattr(error, "source_pause", False) or getattr(error, "retry_after", 0) or str(error) in (
                            "official fetch HTTP 401", "official fetch HTTP 403"):
                        admission_stopped = True
                        pause = max(60, getattr(error, "retry_after", 0))
                        if state == "pending_review":
                            pause = max(pause, 900)
                        previous_pause = progress.execute("SELECT paused_until FROM control WHERE id=1").fetchone()
                        until = max(int(time.time()) + pause, previous_pause[0] if previous_pause else 0)
                        progress.execute("INSERT OR REPLACE INTO control VALUES(1,?)", (until,))
                if pause_seconds and not result["retained"] and (error is None or result["phase"] == "official"):
                    gap_ms, file_limit, error_rate, streak = adjust_pacing(
                        gap_ms, file_limit, error_rate, streak, error is None, max_files, recovery_streak=4)
                    progress.execute("INSERT OR REPLACE INTO pacing VALUES(1,?,?,?,?)",
                                     (gap_ms, file_limit, error_rate, streak))
                audit(row, state, str(error)[:160] if error else "", result["proof"], count, due)
            if (not admission_stopped and len(pending) < waiting_slots and submitted < batch_limit
                    and inspected < 1000 and time.monotonic() < deadline
                    and time.monotonic() >= next_launch):
                row = choose_row()
                if row is None:
                    admission_stopped = True
                    continue
                inspected += 1
                path, name, size, mtime = row
                if path in seen:
                    progress.execute("INSERT OR REPLACE INTO cursor VALUES(1,?)", (cursor,))
                    progress.commit()
                    continue
                seen.add(path)
                prior = progress.execute("SELECT size,mtime_ns,state,checked_at,retry_count,next_retry_at,"
                                         "official_collection,official_item,official_url,sha256 "
                                         "FROM attempts WHERE path=?", (path,)).fetchone()
                if prior and prior[:2] == (size, mtime) and (
                        prior[2] in ("matched", "pending_review") or
                        prior[2] in ("retry_scheduled", "retry_deferred", "record_pending") and prior[5] > time.time() or
                        prior[2] == "no_unique_official_asset" and time.time() - prior[3] < 86400):
                    skipped += 1
                    progress.execute("INSERT OR REPLACE INTO cursor VALUES(1,?)", (cursor,))
                    progress.commit()
                    continue
                asset = cache.lookup(path, name, size)
                if asset is None:
                    unmatched += 1
                    progress.execute("INSERT OR REPLACE INTO cursor VALUES(1,?)", (cursor,))
                    audit(row, "no_unique_official_asset")
                    continue
                retained = prior and prior[:2] == (size, mtime) and prior[2] == "record_pending"
                progress.execute("INSERT OR REPLACE INTO inflight VALUES(?,?,?)", (path, size, mtime))
                progress.execute("INSERT OR REPLACE INTO cursor VALUES(1,?)", (cursor,))
                progress.commit()
                if not retained and size > max_bytes - used:
                    admission_stopped = True
                    continue
                if not retained:
                    fetched += 1
                    used += size
                future = pool.submit(pipeline_content, row, asset, prior, root, socket_path, deadline)
                pending[future] = (row, asset, prior)
                submitted += 1
                peak = max(peak, len(pending))
                next_launch = time.monotonic() + (gap_ms / 1000 if not retained else 0)
                continue
            done_admitting = admission_stopped or submitted >= batch_limit or inspected >= 1000 or time.monotonic() >= deadline
            if not pending and done_admitting:
                break
            delay = 0.25 if done_admitting else max(0.001, min(0.25, next_launch - time.monotonic()))
            if pending:
                wait(tuple(pending), timeout=delay, return_when=FIRST_COMPLETED)
            else:
                time.sleep(delay)
    if sweep_finished:
        progress.execute("INSERT OR REPLACE INTO cursor VALUES(1,'')")
        if submitted == 0:
            due = progress.execute("SELECT min(next_retry_at) FROM attempts WHERE state IN "
                                   "('retry_scheduled','retry_deferred','record_pending')").fetchone()[0]
            progress.execute("INSERT OR REPLACE INTO control VALUES(1,?)",
                             (min(int(time.time()) + 3600, due) if due else int(time.time()) + 3600,))
        progress.commit()
    return {"inspected": inspected, "fetched": fetched, "verified": verified, "skipped": skipped,
            "unmatched": unmatched, "failed": failed, "retrying": retrying, "bytes_requested": used,
            "waiting_slots": waiting_slots, "peak_slots": peak, "gap_ms": gap_ms, "file_limit": batch_limit,
            "official_seconds": round(official_seconds, 3), "local_seconds": round(local_seconds, 3),
            "record_seconds": round(record_seconds, 3), "lru_hits": cache.hits,
            "lru_misses": cache.misses, "lru_evictions": cache.evictions}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state-db", required=True)
    parser.add_argument("--progress-db", required=True)
    parser.add_argument("--root", required=True)
    parser.add_argument("--socket", required=True)
    parser.add_argument("--max-files", type=int, default=20)
    parser.add_argument("--max-bytes", type=int, default=512 << 20)
    parser.add_argument("--max-file-bytes", type=int, default=512 << 20)
    parser.add_argument("--max-seconds", type=int, default=900)
    parser.add_argument("--start-after", default="", help="only used before the first run of a progress database")
    parser.add_argument("--pause-seconds", type=float, default=2)
    parser.add_argument("--waiting-slots", type=int, default=4, help="bounded concurrent content checks (1-8)")
    parser.add_argument("--match-cache-size", type=int, default=16, help="LRU daily catalogue groups (1-128)")
    parser.add_argument("--repair-layout-evidence", action="store_true",
                        help="rehash relocated official proof and invalidate obsolete monthly lookup failures")
    args = parser.parse_args()
    if min(args.max_files, args.max_bytes, args.max_file_bytes, args.max_seconds) <= 0 or args.pause_seconds < 0:
        parser.error("limits must be positive")
    if not 1 <= args.waiting_slots <= 8 or not 1 <= args.match_cache_size <= 128:
        parser.error("waiting slots must be 1-8 and LRU capacity must be 1-128")
    root = os.path.realpath(args.root)
    if args.start_after and not args.start_after.startswith(root + os.sep):
        parser.error("start-after must be inside shared root")
    if not os.path.isdir(root) or not os.path.exists(args.socket):
        parser.error("shared root or centre socket is unavailable")
    index = sqlite3.connect("file:" + args.state_db + "?mode=rw", uri=True, timeout=30)
    index.execute("PRAGMA busy_timeout=30000")
    lock_fd = os.open(args.progress_db + ".lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        progress = progress_db(args.progress_db)
        try:
            if args.repair_layout_evidence:
                print(json.dumps(repair_layout_evidence(index, progress, root), sort_keys=True), flush=True)
                return
            result = run_pipeline(index, progress, root, args.socket, args.max_files, args.max_bytes, args.max_seconds,
                                  args.max_file_bytes, args.start_after, args.pause_seconds,
                                  args.waiting_slots, args.match_cache_size)
            if "paused_until" not in result:
                print(json.dumps(result, sort_keys=True))
            if result["failed"] > result["retrying"]:
                raise SystemExit(1)
        finally:
            progress.close()
    finally:
        os.close(lock_fd)
        index.close()


if __name__ == "__main__":
    main()
