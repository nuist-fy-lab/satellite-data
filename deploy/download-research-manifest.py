#!/usr/bin/env python3
"""Concurrent, lossless download of published research-data manifests.

Accepted manifest: [{year, name, size, md5, url}, ...]. Every file has an
independent .part resume target. All finalized files pass an MD5 check.
"""
import argparse
import concurrent.futures
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import threading
import time
from urllib.parse import urlparse


PRINT_LOCK = threading.Lock()


def log(message):
    now = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    with PRINT_LOCK:
        print('[{}] {}'.format(now, message), flush=True)


def md5_file(path):
    digest = hashlib.md5()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(8 * 1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def parse_manifest(path, root):
    manifest = json.loads(path.read_text(encoding='utf8'))
    if not isinstance(manifest, list) or not manifest:
        raise ValueError('expected a nonempty list of files')
    records = []
    seen = set()
    for i, item in enumerate(manifest, 1):
        year = str(item.get('year') or 'metadata')
        name = item['name']
        size = item['size']
        digest = item['md5']
        url = item['url']
        parsed = urlparse(url)
        if not re.fullmatch(r'(?:[0-9]{4}|metadata)', year):
            raise ValueError('invalid year at record {}'.format(i))
        if (not isinstance(name, str) or not name or
                name != Path(name).name or name in ('.', '..') or
                '/' in name or '\\' in name or '\x00' in name):
            raise ValueError('unsafe filename at record {}'.format(i))
        if type(size) is not int or size < 0:
            raise ValueError('invalid byte count at record {}'.format(i))
        if not isinstance(digest, str) or not re.fullmatch(r'[0-9a-fA-F]{32}', digest):
            raise ValueError('invalid MD5 at record {}'.format(i))
        if parsed.scheme != 'https' or not parsed.hostname:
            raise ValueError('only HTTPS download URLs are accepted')
        relative = str(Path(year) / name)
        if relative in seen:
            raise ValueError('duplicate destination: ' + relative)
        seen.add(relative)
        records.append((i, root / relative, int(size), digest.lower(), url))
    return records


def download_one(entry, total, args):
    i, target, want, expected_md5, url = entry
    tmp = target.with_name(target.name + '.part')
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        if not target.is_file() or target.stat().st_size != want:
            raise ValueError('existing finalized file has unexpected size: ' + str(target))
        log('VERIFY EXISTING {}/{} {}'.format(i, total, target.name))
        if md5_file(target) != expected_md5:
            raise ValueError('existing finalized file fails MD5: ' + str(target))
        log('SKIP VERIFIED {}/{} {}'.format(i, total, target.name))
        return ('verified', want)
    def verify_partial():
        log('CHECKSUM {}/{} {} bytes={}'.format(i, total, target.name, want))
        if md5_file(tmp) != expected_md5:
            raise ValueError('partial file fails MD5; preserving partial: ' + str(tmp))
        os.replace(str(tmp), str(target))
        log('VERIFIED {}/{} {}'.format(i, total, target.name))
        return ('downloaded', want)

    for attempt in range(1, args.attempts + 1):
        got = tmp.stat().st_size if tmp.exists() else 0
        if got > want:
            raise ValueError('partial larger than manifest: ' + str(tmp))
        if got == want:
            return verify_partial()
        log('DOWNLOAD {}/{} attempt={} partial={}/{} {}'.format(
            i, total, attempt, got, want, target.name))
        argv = [
            'curl', '--http1.1', '--fail', '--silent', '--show-error', '--location',
            '--continue-at', '-', '--connect-timeout', '25',
            '--speed-limit', '2048', '--speed-time', '240',
            '--retry', '2', '--retry-delay', '10',
            '--output', str(tmp),
            '--write-out', '%{http_code} %{speed_download} %{size_download}',
            url]
        try:
            result = subprocess.run(argv, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, timeout=args.curl_timeout)
            report = result.stdout.decode('utf8', 'replace').strip()
            code = result.returncode
        except subprocess.TimeoutExpired:
            report, code = '', 124
        except OSError as exc:
            report, code = '', 125
        if tmp.exists() and tmp.stat().st_size == want:
            # A completed response must pass MD5 even if the transfer exited
            # with an error after writing the final bytes.
            return verify_partial()
        if code:
            log('RETRYABLE {} curl={} status={} attempt={}'.format(
                target.name, code, report, attempt))
        if attempt < args.attempts:
            delay = min(240, 15 * attempt)
            if code:
                log('BACKOFF {} {}s'.format(target.name, delay))
                time.sleep(delay)
        elif code:
            raise RuntimeError('retry limit reached: {} curl={}'.format(target.name, code))
    raise RuntimeError('retry limit reached without MD5 success: ' + target.name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--workers', type=int, default=3)
    parser.add_argument('--attempts', type=int, default=50)
    parser.add_argument('--curl-timeout', type=int, default=14400)
    args = parser.parse_args()
    if not 1 <= args.workers <= 8 or not 1 <= args.attempts <= 100:
        parser.error('--workers must be 1..8 and --attempts 1..100')
    records = parse_manifest(args.manifest, args.output_dir)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    lock_path = args.output_dir / '.parallel-download.lock'
    with lock_path.open('a+') as lock:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            log('ERROR another manifest downloader holds the lock')
            return 7
        # Prioritize existing partials, then undiscovered files; verify
        # previously finalized files after transfers have started.
        records.sort(key=lambda item: (
            2 if item[1].exists() else
            0 if item[1].with_name(item[1].name + '.part').exists() else 1,
            item[0]))
        expected = sum(row[2] for row in records)
        log('START {} files expected_bytes={} workers={} protocol=HTTP/1.1'.format(
            len(records), expected, args.workers))
        errors = []
        completed = 0
        bytes_ok = 0
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
            futures = {pool.submit(download_one, item, len(records), args): item
                       for item in records}
            for future in concurrent.futures.as_completed(futures):
                item = futures[future]
                try:
                    _, bytes_verified = future.result()
                    completed += 1
                    bytes_ok += bytes_verified
                except Exception as exc:
                    errors.append((item[1].name, str(exc)))
                    log('FILE_ERROR {} {}'.format(item[1].name, str(exc)))
                log('SUMMARY verified_files={}/{} verified_bytes={} errors={}'.format(
                    completed, len(records), bytes_ok, len(errors)))
        if errors:
            log('FINISHED_WITH_ERRORS {} files; partials preserved'.format(len(errors)))
            return 2 if any('MD5' in reason or 'existing finalized' in reason
                            for _, reason in errors) else 5
        log('COMPLETE ALL {} files checked by MD5'.format(len(records)))
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError) as error:
        log('FATAL {}'.format(error))
        sys.exit(2)
