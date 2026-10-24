#!/usr/bin/env python3
"""Prepare exact product/day COPY shards on a Slurm compute allocation.

Only the stopped source SQLite database is read. Each shard is durable on
shared storage and carries its source identity, row counts and SHA256 hashes.
No PostgreSQL connection or production database write occurs in this step.
"""
import argparse
import concurrent.futures
import hashlib
import json
import os
import sqlite3
import tempfile


def escape(value):
    if value is None:
        return r'\N'
    if isinstance(value, bytes):
        raise ValueError('SQLite BLOB is not representable by the destination schema')
    value = str(value)
    if '\x00' in value:
        raise ValueError('PostgreSQL text cannot preserve a NUL byte')
    return value.replace('\\', '\\\\').replace('\t', r'\t').replace('\r', r'\r').replace('\n', r'\n')


def identity(source):
    stat = os.stat(source)
    return {'sqlite_size': stat.st_size, 'sqlite_mtime_ns': stat.st_mtime_ns}


def shard_key(product, day):
    return hashlib.sha256((product + '\x00' + day).encode('utf8')).hexdigest()


def shard_files(directory, product, day):
    key = shard_key(product, day)
    folder = os.path.join(directory, key[:2])
    prefix = os.path.join(folder, key)
    return folder, prefix + '.assets', prefix + '.slots', prefix + '.json'


def checksum(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as src:
        for block in iter(lambda: src.read(4 * 1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def valid_shard(source, directory, product, day):
    _, assets, slots, manifest = shard_files(directory, product, day)
    if not all(os.path.isfile(path) for path in (assets, slots, manifest)):
        return False
    try:
        with open(manifest, encoding='utf8') as stream:
            info = json.load(stream)
        return (info.get('product') == product and info.get('day') == day and
                info.get('source') == identity(source) and
                info['assets']['sha256'] == checksum(assets) and
                info['slots']['sha256'] == checksum(slots) and
                info['assets']['bytes'] == os.path.getsize(assets) and
                info['slots']['bytes'] == os.path.getsize(slots))
    except (ValueError, KeyError, OSError, TypeError):
        return False


def produce_one(args):
    source, directory, product, day = args
    if valid_shard(source, directory, product, day):
        return ('REUSED', product, day)
    folder, assets, slots, manifest = shard_files(directory, product, day)
    os.makedirs(folder, mode=0o700, exist_ok=True)
    sqlite = sqlite3.connect('file:' + source + '?mode=ro', uri=True, timeout=30)
    before = identity(source)
    stats = {}
    try:
        for table, dest in (('official_assets', assets), ('official_slots', slots)):
            fd, tmp = tempfile.mkstemp(prefix='.copy-', dir=folder)
            count = 0
            try:
                with os.fdopen(fd, 'wb') as stream:
                    rows = sqlite.execute(
                        'SELECT * FROM ' + table +
                        ' WHERE product_key=? AND substr(slot_ts,1,10)=?',
                        (product, day))
                    for record in rows:
                        if table == 'official_assets':
                            product_key, slot_ts, granule_id, filename, url, size, etag, media = record
                            index = url.rfind('/')
                            prefix = url[:index + 1] if index >= 0 else ''
                            suffix = url[index + 1:] if index >= 0 else url
                            record = (product_key, slot_ts, granule_id, filename,
                                      prefix, None if suffix == filename else suffix,
                                      size, etag, media)
                        stream.write(('\t'.join(escape(x) for x in record) + '\n').encode('utf8'))
                        count += 1
                    stream.flush()
                    os.fsync(stream.fileno())
                os.replace(tmp, dest)
            finally:
                if os.path.exists(tmp):
                    os.unlink(tmp)
            stats['assets' if table == 'official_assets' else 'slots'] = {
                'rows': count, 'bytes': os.path.getsize(dest), 'sha256': checksum(dest)}
        if before != identity(source):
            raise RuntimeError('SQLite source changed while preparing data')
        info = {'source': before, 'product': product, 'day': day,
                'assets': stats['assets'], 'slots': stats['slots']}
        fd, tmp = tempfile.mkstemp(prefix='.manifest-', dir=folder)
        try:
            with os.fdopen(fd, 'w', encoding='utf8') as stream:
                json.dump(info, stream, ensure_ascii=False, sort_keys=True)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(tmp, manifest)
        finally:
            if os.path.exists(tmp):
                os.unlink(tmp)
    finally:
        sqlite.close()
    return ('PREPARED', product, day)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sqlite', required=True)
    parser.add_argument('--worklist', required=True)
    parser.add_argument('--output-dir', required=True)
    parser.add_argument('--workers', type=int, default=8)
    parser.add_argument('--limit-days', type=int, default=0)
    args = parser.parse_args()
    if not 1 <= args.workers <= 16 or args.limit_days < 0:
        raise SystemExit('invalid workers or day limit')
    if not os.path.isfile(args.sqlite):
        raise SystemExit('source SQLite database missing')
    os.makedirs(args.output_dir, mode=0o700, exist_ok=True)
    work = []
    seen = set()
    with open(args.worklist, encoding='utf8') as stream:
        for line in stream:
            encoded, day, weight = line.rstrip('\n').split('\t')
            product = bytes.fromhex(encoded).decode('utf8')
            key = (product, day)
            if key in seen:
                raise SystemExit('duplicate product/day in input worklist')
            seen.add(key)
            work.append((int(weight), product, day))
    work.sort(reverse=True)
    if args.limit_days:
        work = work[:args.limit_days]
    completed = 0
    with concurrent.futures.ProcessPoolExecutor(max_workers=args.workers) as executor:
        for state, product, day in executor.map(
                produce_one,
                ((args.sqlite, args.output_dir, product, day)
                 for _, product, day in work)):
            completed += 1
            if completed % 16 == 0 or completed == len(work):
                print('PREPARED_COPY_SHARDS', completed, '/', len(work),
                      'workers', args.workers, flush=True)
    print('COPY_SHARDS_READY', completed, flush=True)


if __name__ == '__main__':
    main()
