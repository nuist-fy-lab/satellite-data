#!/usr/bin/env python3
"""Bounded, resumable verification of historical EarthCARE files against ESA assets."""

import argparse
import fcntl
import hashlib
import http.client
import json
import os
import re
import socket
import sqlite3
import stat
import time
from datetime import datetime, timezone


ACQUISITION = re.compile(r"_(\d{8})T\d{6}Z")
CHUNK = 128 * 1024


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
            raise ValueError("official stream size differs from catalog")
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
    db.commit()
    os.chmod(path, 0o600)
    return db


def run(index, progress, root, socket_path, max_files, max_bytes, max_seconds,
        max_file_bytes=None, start_after="", pause_seconds=0):
    paused = progress.execute("SELECT paused_until FROM control WHERE id=1").fetchone()
    if paused and paused[0] > time.time():
        return {"inspected": 0, "fetched": 0, "verified": 0, "skipped": 0,
                "unmatched": 0, "failed": 0, "bytes_requested": 0, "paused_until": paused[0]}
    cursor_row = progress.execute("SELECT path FROM cursor WHERE id=1").fetchone()
    cursor = cursor_row[0] if cursor_row else start_after
    max_file_bytes = max_file_bytes or max_bytes
    started = time.monotonic()
    matched = fetched = skipped = inspected = used = failed = unmatched = 0
    while inspected < 1000 and fetched < max_files and time.monotonic() - started < max_seconds:
        rows = candidates(index, root, cursor, max_file_bytes, 32)
        if not rows:
            progress.execute("INSERT OR REPLACE INTO cursor(id,path) VALUES(1,'')")
            progress.commit()
            break
        exhausted = False
        stop_after_failure = False
        for path, name, size, mtime in rows:
            if inspected >= 1000 or fetched >= max_files or time.monotonic() - started >= max_seconds:
                exhausted = True
                break
            cursor = path
            inspected += 1
            prior = progress.execute("SELECT size,mtime_ns,state,checked_at FROM attempts WHERE path=?", (path,)).fetchone()
            if prior and prior[:2] == (size, mtime) and (prior[2] == "matched" or time.time() - prior[3] < 86400):
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
                    try:
                        remote = official_digest(socket_path, url, size)
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
                        progress.execute("INSERT OR REPLACE INTO control(id,paused_until) VALUES(1,?)",
                                         (int(time.time()) + 900,))
                    if pause_seconds:
                        time.sleep(pause_seconds)
                progress.execute("INSERT OR REPLACE INTO attempts VALUES(?,?,?,?,?,?,?,?,?,?)",
                                 (path, size, mtime, state, detail, int(time.time())) + proof)
            progress.execute("INSERT OR REPLACE INTO cursor(id,path) VALUES(1,?)", (cursor,))
            progress.commit()
            if stop_after_failure:
                break
        if stop_after_failure:
            break
        if len(rows) < 32 and not exhausted:
            progress.execute("INSERT OR REPLACE INTO cursor(id,path) VALUES(1,'')")
            progress.commit()
            break
    return {"inspected": inspected, "fetched": fetched, "verified": matched, "skipped": skipped,
            "unmatched": unmatched, "failed": failed, "bytes_requested": used}


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
            print(json.dumps(result, sort_keys=True))
            if result["failed"]:
                raise SystemExit(1)
        finally:
            progress.close()
    finally:
        os.close(lock_fd)
        index.close()


if __name__ == "__main__":
    main()
