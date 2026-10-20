#!/usr/bin/env python3
"""Produce a lossless product/day work-size hint on an allocated compute node.

This tool only reads the stopped SQLite source. Its output changes scheduling
order, never which records are migrated. Each process owns a read-only SQLite
connection and scans an existing product/day expression index.
"""
import argparse
import concurrent.futures
import os
import sqlite3
import tempfile


def plan_product(args):
    source, product = args
    db = sqlite3.connect('file:' + source + '?mode=ro', uri=True, timeout=30)
    try:
        counts = {}
        for table in ('official_assets', 'official_slots'):
            rows = db.execute(
                'SELECT substr(slot_ts,1,10),count(*) FROM ' + table +
                ' WHERE product_key=? GROUP BY substr(slot_ts,1,10)', (product,))
            for day, count in rows:
                counts[day] = counts.get(day, 0) + count
        return [(product.encode('utf8').hex(), day, size)
                for day, size in counts.items()]
    finally:
        db.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sqlite', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--workers', type=int, default=8)
    args = parser.parse_args()
    if not 1 <= args.workers <= 16:
        raise SystemExit('workers must be between 1 and 16')
    if not os.path.isfile(args.sqlite):
        raise SystemExit('source SQLite file missing')
    db = sqlite3.connect('file:' + args.sqlite + '?mode=ro', uri=True)
    try:
        products = [row[0] for row in db.execute(
            'SELECT product_key FROM official_assets UNION SELECT product_key FROM official_slots')]
    finally:
        db.close()
    parent = os.path.dirname(os.path.abspath(args.output))
    if not os.path.isdir(parent):
        raise SystemExit('output directory does not exist')
    fd, temp = tempfile.mkstemp(prefix='.normalization-plan-', dir=parent)
    count = 0
    try:
        with os.fdopen(fd, 'w') as stream:
            with concurrent.futures.ProcessPoolExecutor(max_workers=args.workers) as executor:
                for rows in executor.map(plan_product, ((args.sqlite, product) for product in products)):
                    for product_hex, day, size in rows:
                        stream.write('{}\t{}\t{}\n'.format(product_hex, day, size))
                        count += 1
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temp, 0o600)
        os.replace(temp, args.output)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)
    print('PREPARED_PRODUCT_DAYS', count, 'WORKERS', args.workers,
          'OUTPUT', args.output, flush=True)


if __name__ == '__main__':
    main()
