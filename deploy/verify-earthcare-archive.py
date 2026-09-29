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
import time
from datetime import datetime, timezone


ACQUISITION = re.compile(r"_(\d{8})T\d{6}Z")
CHUNK = 128 * 1024
MAX_TRANSIENT_ATTEMPTS = 4
MAX_RETRY_DELAY = 1800
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
    if value.isascii() and value.isdigit():
        return 86400 if len(value) > 5 else min(int(value), 86400)
    try:
        date = email.utils.parsedate_to_datetime(value)
        return min(86400, max(0, math.ceil(date.timestamp() - time.time())))
    except (TypeError, ValueError, OverflowError):
        return 0


def retry_delay(attempt, retry_after=0):
    ceiling = min(MAX_RETRY_DELAY, 60 * (2 ** (attempt - 1)))
    return max(int(random.uniform(ceiling / 2, ceiling)), retry_after, 1)


def adjust_pacing(gap_ms, file_limit, error_rate, streak, success, max_files):
    error_rate = error_rate * 0.8 + (0 if success else 0.2)
    if not success:
        streak = 0
        if error_rate >= 0.2:
            gap_ms = min(MAX_GAP_MS, max(MIN_GAP_MS, int(gap_ms * 1.5)))
            file_limit = max(min(MIN_BATCH_FILES, max_files), file_limit // 2)
    else:
        streak += 1
        if streak >= 8 and error_rate < 0.1:
            gap_ms = max(MIN_GAP_MS, int(gap_ms * 0.8))
            file_limit = min(max_files, file_limit + 2)
            streak = 0
    return gap_ms, file_limit, error_rate, streak


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, socket_path):
        super().__init__("unix", timeout=180)
        self.socket_path = socket_path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(180)
        self.sock.connect(self.socket_path)


def candidates(db, root, cursor, max_file_bytes, limit=128):
    return db.execute(
        "SELECT path,filename,size,mtime_ns FROM local_files "
        "WHERE path>? AND path LIKE ? AND state='unverified' AND size BETWEEN 1 AND ? "
        "ORDER BY path LIMIT ?", (cursor, root + "/%", max_file_bytes, limit)
    ).fetchall()


def official_asset(db, path, name, size, root):
    rel = os.path.relpath(path, root).split(os.sep)
    match = ACQUISITION.search(name)
    if len(rel) != 3 or rel[-1] != name or not match or rel[1] != match.group(1):
        return None
    try:
        day = datetime.strptime(rel[1], "%Y%m%d").strftime("%Y%m%d")
    except ValueError:
        return None
    if day != rel[1]:
        return None
    rows = db.execute(
        "SELECT a.product_key,a.granule_id,a.url,a.slot_ts FROM official_assets a "
        "JOIN products p ON p.product_key=a.product_key "
        "WHERE a.filename=? AND a.size=? AND p.source_id='earthcare' AND p.data_type=?",
        (name, size, rel[0]),
    ).fetchall()
    refs = {(row[1], row[2]) for row in rows if row[3][:10] == rel[1][:4] + "-" + rel[1][4:6] + "-" + rel[1][6:]}
    if len(refs) != 1:
        return None
    item, url = refs.pop()
    if not url.startswith("https://catalog.maap.eo.esa.int/data/"):
        return None
    # Several access-class collections can expose the same physical asset.
    matching = sorted(row[0] for row in rows if row[1] == item and row[2] == url)
    preferred = next((key for key in matching if "Validated_MAAP:" in key), matching[0])
    return preferred, item, url


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


def official_digest(socket_path, url, size):
    connection = UnixHTTP(socket_path)
    try:
        payload = json.dumps({"source_id": "earthcare", "url": url}).encode("utf-8")
        connection.request("POST", "/v1/fetch", payload, {"Content-Type": "application/json"})
        response = connection.getresponse()
        if response.status in (408, 429) or 500 <= response.status < 600:
            raise RetryableOfficialError("official fetch HTTP %d" % response.status,
                                         retry_after_seconds(response.getheader("Retry-After")))
        if response.status != 200:
            raise ValueError("official fetch HTTP %d" % response.status)
        if response.getheader("Content-Length") and int(response.getheader("Content-Length")) != size:
            raise ValueError("official length differs from catalog")
        digest = hashlib.sha256()
        count = 0
        while count <= size:
            chunk = response.read(min(CHUNK, size + 1 - count))
            if not chunk:
                break
            count += len(chunk)
            digest.update(chunk)
        if count != size:
            raise RetryableOfficialError("official stream size differs from catalog")
        return digest.hexdigest()
    finally:
        connection.close()


def record(db, path, name, size, mtime, digest, collection, item, url, root):
    db.execute("BEGIN IMMEDIATE")
    try:
        row = db.execute("SELECT filename,size,mtime_ns,state FROM local_files WHERE path=?", (path,)).fetchone()
        if row != (name, size, mtime, "unverified"):
            raise ValueError("indexed file changed before recording")
        if official_asset(db, path, name, size, root) != (collection, item, url):
            raise ValueError("official reference changed before recording")
        if os.path.commonpath((os.path.realpath(path), root)) != root:
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
        db.commit()
    except BaseException:
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
    db.commit()
    os.chmod(path, 0o600)
    return db


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
                               "WHERE state='retry_scheduled' AND next_retry_at<=? "
                               "AND path LIKE ? AND size<=? ORDER BY next_retry_at LIMIT 1",
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
                                              "WHERE state='retry_scheduled'").fetchone()[0]
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
            prior = progress.execute("SELECT size,mtime_ns,state,checked_at,retry_count,next_retry_at "
                                     "FROM attempts WHERE path=?", (path,)).fetchone()
            if prior and prior[:2] == (size, mtime) and (
                prior[2] in ("matched", "pending_review") or
                prior[2] == "retry_scheduled" and prior[5] > time.time() or
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
                    fetched += 1
                    used += size
                    retry_count = next_retry_at = 0
                    phase = "official"
                    try:
                        remote = official_digest(socket_path, url, size)
                        phase = "local"
                        local = local_digest(path, root, size, mtime)
                        if local != remote:
                            raise ValueError("official content SHA-256 mismatch")
                        record(index, path, name, size, mtime, local, collection, item, url, root)
                        state, detail = "matched", ""
                        proof = (collection, item, url, local)
                        matched += 1
                    except (OSError, ValueError, http.client.HTTPException, sqlite3.Error) as exc:
                        state, detail = "pending_review", str(exc)[:160]
                        failed += 1
                        stop_after_failure = True
                        if phase == "official" and isinstance(exc, (RetryableOfficialError, OSError,
                                                                     http.client.HTTPException)):
                            retry_count = (prior[4] if prior and prior[:2] == (size, mtime) else 0) + 1
                            if retry_count < MAX_TRANSIENT_ATTEMPTS:
                                state = "retry_scheduled"
                                next_retry_at = int(time.time()) + retry_delay(
                                    retry_count, getattr(exc, "retry_after", 0))
                                retrying += 1
                        progress.execute("INSERT OR REPLACE INTO control(id,paused_until) VALUES(1,?)",
                                         (next_retry_at or int(time.time()) + 900,))
                    if adaptive:
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
    args = parser.parse_args()
    if min(args.max_files, args.max_bytes, args.max_file_bytes, args.max_seconds) <= 0 or args.pause_seconds < 0:
        parser.error("limits must be positive")
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
            result = run(index, progress, root, args.socket, args.max_files, args.max_bytes, args.max_seconds,
                         args.max_file_bytes, args.start_after, args.pause_seconds)
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
