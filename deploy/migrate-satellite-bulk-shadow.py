#!/usr/bin/env python3
"""Lossless satellite shadow migration: heap COPY, build indexes, compare, swap.

Use only with a stopped satellite application and preserved source SQLite.
Every stage is restartable and the live compact tables are untouched until
the independent shadow passes exact, two-way field reconciliation.
"""
import argparse
import concurrent.futures
import hashlib
import json
import os
import re
import sqlite3
import subprocess
import sys
import time

ORIGINAL_ASSETS = 22402508  # Runtime checks use source migration log, not this hint.
ALLOWED_PATH = re.compile(r'^[/a-zA-Z0-9_.-]+$')


def pg(args, sql, script=False):
    command = [args.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1',
               '-h', args.socket, '-d', args.database]
    command += ['-f', '-'] if script else ['-c', sql]
    result = subprocess.run(command, input=sql.encode('utf8') if script else None,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError('PostgreSQL rejected operation: ' +
                           result.stderr.decode('utf8', 'replace')[-1300:])
    return result.stdout.decode('utf8', 'replace').strip()


def lit(value):
    return "'" + value.replace("'", "''") + "'"


def worklist(args):
    items = []
    seen = set()
    with open(args.worklist, encoding='utf8') as stream:
        for line in stream:
            hex_name, day, weight = line.rstrip('\n').split('\t')
            product = bytes.fromhex(hex_name).decode('utf8')
            if (product, day) in seen:
                raise RuntimeError('duplicate source day')
            seen.add((product, day))
            items.append((product, day, int(weight)))
    if not items:
        raise RuntimeError('empty source worklist')
    return items


def expected_counts(args):
    output = pg(args, """SELECT source_table,source_rows
FROM satellite_migration_log
WHERE source_table IN ('official_assets','official_slots') ORDER BY source_table""")
    info = dict(row.split('|') for row in output.splitlines())
    if set(info) != {'official_assets', 'official_slots'}:
        raise RuntimeError('original SQLite import has not been completed')
    return int(info['official_assets']), int(info['official_slots'])


def init(args):
    expected_counts(args)
    pg(args, """
CREATE TABLE IF NOT EXISTS migration_bulk_assets (
 product_key TEXT NOT NULL,slot_ts TEXT NOT NULL,granule_id TEXT NOT NULL,
 filename TEXT NOT NULL,url_prefix TEXT NOT NULL,url_suffix TEXT,
 size BIGINT NOT NULL,etag TEXT NOT NULL,media_type TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS migration_bulk_slots (
 product_key TEXT NOT NULL,slot_ts TEXT NOT NULL,granule_id TEXT NOT NULL,
 asset_count BIGINT NOT NULL,total_size BIGINT NOT NULL,
 representative_filename TEXT NOT NULL,refreshed_at TEXT NOT NULL
);
-- Only the small progress metadata has an index during COPY.
CREATE TABLE IF NOT EXISTS migration_bulk_loaded_days (
 product_key TEXT NOT NULL,day TEXT NOT NULL,
 asset_rows BIGINT NOT NULL,slot_rows BIGINT NOT NULL,
 PRIMARY KEY(product_key,day)
);
CREATE TABLE IF NOT EXISTS migration_bulk_build_steps (
 step TEXT PRIMARY KEY, completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS migration_bulk_verified_days (
 product_key TEXT NOT NULL,day TEXT NOT NULL,
 PRIMARY KEY(product_key,day)
);
""")
    print('SHADOW_HEAPS_READY_NO_INDEXES', flush=True)


def verify_manifest(args, product, day):
    key = hashlib.sha256((product + '\x00' + day).encode('utf8')).hexdigest()
    base = os.path.join(args.prepared_dir, key[:2], key)
    assets, slots, manifest = base + '.assets', base + '.slots', base + '.json'
    with open(manifest, encoding='utf8') as stream:
        data = json.load(stream)
    source = os.stat(args.sqlite)
    if (data['product'] != product or data['day'] != day or
            data['source']['sqlite_size'] != source.st_size or
            data['source']['sqlite_mtime_ns'] != source.st_mtime_ns):
        raise RuntimeError('COPY shard identity or original SQLite snapshot changed')
    for kind, path in (('assets', assets), ('slots', slots)):
        digest = hashlib.sha256()
        with open(path, 'rb') as stream:
            for block in iter(lambda: stream.read(4 * 1024 * 1024), b''):
                digest.update(block)
        if (digest.hexdigest() != data[kind]['sha256'] or
                os.path.getsize(path) != data[kind]['bytes']):
            raise RuntimeError('COPY shard SHA256 or length mismatch: ' + kind)
        if not ALLOWED_PATH.fullmatch(path):
            raise RuntimeError('unsafe COPY shard path')
    return assets, slots, data


def load_one(args, product, day):
    assets, slots, data = verify_manifest(args, product, day)
    # COPY bypasses expensive live indexes, keeping the checkpoint in the
    # very same transaction for restart after crash/cancellation.
    statement = r"""
BEGIN;
SET LOCAL lock_timeout='60s';
\copy migration_bulk_assets FROM '{assets}' WITH (FORMAT text)
\copy migration_bulk_slots FROM '{slots}' WITH (FORMAT text)
INSERT INTO migration_bulk_loaded_days(product_key,day,asset_rows,slot_rows)
VALUES({product},{day},{asset_rows},{slot_rows});
COMMIT;
""".format(assets=assets, slots=slots, product=lit(product), day=lit(day),
           asset_rows=int(data['assets']['rows']),
           slot_rows=int(data['slots']['rows']))
    pg(args, statement, script=True)


def load(args):
    init(args)
    days = worklist(args)
    finished = set(pg(args, """SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM migration_bulk_loaded_days""").splitlines())
    pending = [(product, day) for product, day, _ in days
               if product.encode('utf8').hex() + '|' + day not in finished]
    print('SHADOW_COPY_SOURCE_DAYS', len(days),
          'PENDING', len(pending), 'WORKERS', args.workers, flush=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        active = {}
        it = iter(pending)
        completed = 0
        try:
            while True:
                while len(active) < args.workers * 2:
                    try:
                        product, day = next(it)
                    except StopIteration:
                        break
                    active[pool.submit(load_one, args, product, day)] = (product, day)
                if not active:
                    break
                done, _ = concurrent.futures.wait(
                    active, return_when=concurrent.futures.FIRST_COMPLETED)
                for future in done:
                    product, day = active.pop(future)
                    try:
                        future.result()
                    except Exception as exc:
                        raise RuntimeError('shadow COPY failed ' +
                                           repr((product, day)) + ': ' + str(exc))
                    completed += 1
                    if completed % 100 == 0 or completed == len(pending):
                        print('SHADOW_COPY_PROGRESS', completed, '/',
                              len(pending), flush=True)
        except Exception:
            for future in active:
                future.cancel()
            raise
    print('SHADOW_COPY_ALL_DAYS_DONE', flush=True)


def require_loaded(args):
    days = worklist(args)
    planned = {product.encode('utf8').hex() + '|' + day for product, day, _ in days}
    loaded = set(pg(args, """SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM migration_bulk_loaded_days""").splitlines())
    if planned != loaded:
        raise RuntimeError('shadow bulk missing/extra day checkpoints: ' +
                           str((len(planned), len(loaded))))
    expected_assets, expected_slots = expected_counts(args)
    result = pg(args, """SELECT
(SELECT count(*) FROM migration_bulk_assets),
(SELECT count(*) FROM migration_bulk_slots),
(SELECT sum(asset_rows) FROM migration_bulk_loaded_days),
(SELECT sum(slot_rows) FROM migration_bulk_loaded_days)""")
    a, s, ca, cs = map(int, result.split('|'))
    if (a, s, ca, cs) != (expected_assets, expected_slots,
                           expected_assets, expected_slots):
        raise RuntimeError('shadow COPY row conservation failed ' + result)
    print('SHADOW_COPY_COUNTS_EXACT', a, s, 'DAYS', len(days), flush=True)


def build_step(args, step, statement):
    done = pg(args, 'SELECT step FROM migration_bulk_build_steps WHERE step=' + lit(step))
    if done == step:
        print('SHADOW_BUILD_ALREADY_DONE', step, flush=True)
        return
    print('SHADOW_BUILD_START', step, flush=True)
    started = time.monotonic()
    pg(args, ("BEGIN;\nSET LOCAL work_mem='64MB';\n" + statement + "\n" +
              "INSERT INTO migration_bulk_build_steps(step) VALUES(" +
              lit(step) + ");\nCOMMIT;"), script=True)
    print('SHADOW_BUILD_DONE', step, 'SECONDS',
          round(time.monotonic() - started, 1), flush=True)


def build(args):
    require_loaded(args)
    build_step(args, 'keys', """
CREATE TABLE pg_official_granule_keys_next
(LIKE pg_official_granule_keys INCLUDING DEFAULTS INCLUDING IDENTITY);
INSERT INTO pg_official_granule_keys_next(id,product_key,slot_ts,granule_id)
SELECT row_number() OVER(ORDER BY product_key,slot_ts,granule_id),
       product_key,slot_ts,granule_id
FROM (
 SELECT product_key,slot_ts,granule_id FROM migration_bulk_assets
 UNION SELECT product_key,slot_ts,granule_id FROM migration_bulk_slots
) all_keys;
ALTER TABLE pg_official_granule_keys_next
 ADD CONSTRAINT bulk_granule_pk PRIMARY KEY(id),
 ADD CONSTRAINT bulk_granule_unique UNIQUE(product_key,slot_ts,granule_id);
CREATE INDEX bulk_granule_day_idx ON pg_official_granule_keys_next
 (product_key,(substr(slot_ts,1,10)));
""")
    build_step(args, 'prefixes', """
CREATE TABLE pg_official_url_prefixes_next
(LIKE pg_official_url_prefixes INCLUDING DEFAULTS INCLUDING IDENTITY);
INSERT INTO pg_official_url_prefixes_next(id,prefix)
SELECT row_number() OVER(ORDER BY prefix),prefix
FROM (SELECT DISTINCT url_prefix AS prefix FROM migration_bulk_assets) prefixes;
ALTER TABLE pg_official_url_prefixes_next
 ADD CONSTRAINT bulk_prefix_pk PRIMARY KEY(id),
 ADD CONSTRAINT bulk_prefix_unique UNIQUE(prefix);
""")
    build_step(args, 'slots', """
CREATE TABLE pg_official_slot_details_next
(LIKE pg_official_slot_details INCLUDING DEFAULTS);
INSERT INTO pg_official_slot_details_next(
 granule_key,asset_count,total_size,representative_filename,refreshed_at)
SELECT k.id,s.asset_count,s.total_size,s.representative_filename,s.refreshed_at
FROM migration_bulk_slots s
JOIN pg_official_granule_keys_next k USING(product_key,slot_ts,granule_id);
ALTER TABLE pg_official_slot_details_next
 ADD CONSTRAINT bulk_slot_pk PRIMARY KEY(granule_key);
""")
    build_step(args, 'assets', """
CREATE TABLE pg_official_asset_details_next
(LIKE pg_official_asset_details INCLUDING DEFAULTS);
INSERT INTO pg_official_asset_details_next(
 granule_key,filename,url_prefix_id,url_suffix,size,etag,media_type)
SELECT k.id,a.filename,p.id,a.url_suffix,a.size,a.etag,a.media_type
FROM migration_bulk_assets a
JOIN pg_official_granule_keys_next k USING(product_key,slot_ts,granule_id)
JOIN pg_official_url_prefixes_next p ON p.prefix=a.url_prefix;
""")
    build_step(args, 'indexes', """
-- All 22M asset rows are already loaded before this operation.
ALTER TABLE pg_official_asset_details_next
 ADD CONSTRAINT bulk_asset_pk PRIMARY KEY(granule_key,filename);
CREATE INDEX bulk_asset_filename_idx ON pg_official_asset_details_next USING HASH(filename);
ALTER TABLE pg_official_slot_details_next
 ADD CONSTRAINT bulk_slot_granule_fk FOREIGN KEY(granule_key)
 REFERENCES pg_official_granule_keys_next(id) ON DELETE CASCADE;
ALTER TABLE pg_official_asset_details_next
 ADD CONSTRAINT bulk_asset_granule_fk FOREIGN KEY(granule_key)
 REFERENCES pg_official_granule_keys_next(id) ON DELETE CASCADE,
 ADD CONSTRAINT bulk_asset_prefix_fk FOREIGN KEY(url_prefix_id)
 REFERENCES pg_official_url_prefixes_next(id);
CREATE OR REPLACE VIEW migration_bulk_shadow_slots AS
 SELECT k.product_key,k.slot_ts,k.granule_id,s.asset_count,s.total_size,
        s.representative_filename,s.refreshed_at
 FROM pg_official_slot_details_next s
 JOIN pg_official_granule_keys_next k ON k.id=s.granule_key;
CREATE OR REPLACE VIEW migration_bulk_shadow_assets AS
 SELECT k.product_key,k.slot_ts,k.granule_id,a.filename,
        COALESCE(p.prefix,'') || COALESCE(a.url_suffix,a.filename) AS url,
        a.size,a.etag,a.media_type
 FROM pg_official_asset_details_next a
 JOIN pg_official_granule_keys_next k ON k.id=a.granule_key
 LEFT JOIN pg_official_url_prefixes_next p ON p.id=a.url_prefix_id;
""")
    print('SHADOW_BUILD_ALL_STEPS_DONE', flush=True)


def require_built(args):
    steps = set(pg(args, 'SELECT step FROM migration_bulk_build_steps').splitlines())
    expected = {'keys', 'prefixes', 'slots', 'assets', 'indexes'}
    if steps != expected:
        raise RuntimeError('shadow build incomplete, finished=' + str(steps))
    a, s = expected_counts(args)
    counts = pg(args, """SELECT
 (SELECT count(*) FROM migration_bulk_shadow_assets),
 (SELECT count(*) FROM migration_bulk_shadow_slots)""")
    if list(map(int, counts.split('|'))) != [a, s]:
        raise RuntimeError('shadow final table row counts do not match source')
    print('SHADOW_FINAL_COUNTS_EXACT', a, s, flush=True)


def verify_one(args, product, day):
    product, day = lit(product), lit(day)
    where = 'product_key=' + product + ' AND substr(slot_ts,1,10)=' + day
    asset_fields = 'product_key,slot_ts,granule_id,filename,url,size,etag,media_type'
    slot_fields = 'product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at'
    source_assets = 'SELECT ' + asset_fields + ' FROM migration_official_assets WHERE ' + where
    shadow_assets = 'SELECT ' + asset_fields + ' FROM migration_bulk_shadow_assets WHERE ' + where
    source_slots = 'SELECT ' + slot_fields + ' FROM migration_official_slots WHERE ' + where
    shadow_slots = 'SELECT ' + slot_fields + ' FROM migration_bulk_shadow_slots WHERE ' + where
    sql = """
WITH missing_assets AS ({a} EXCEPT ALL {b}),
     extra_assets AS ({b} EXCEPT ALL {a}),
     missing_slots AS ({c} EXCEPT ALL {d}),
     extra_slots AS ({d} EXCEPT ALL {c})
INSERT INTO migration_bulk_verified_days(product_key,day)
SELECT {product},{day}
WHERE NOT EXISTS(SELECT 1 FROM missing_assets)
  AND NOT EXISTS(SELECT 1 FROM extra_assets)
  AND NOT EXISTS(SELECT 1 FROM missing_slots)
  AND NOT EXISTS(SELECT 1 FROM extra_slots)
ON CONFLICT DO NOTHING RETURNING 1;
""".format(a=source_assets,b=shadow_assets,c=source_slots,d=shadow_slots,
           product=product,day=day)
    if pg(args, sql) != '1':
        raise RuntimeError('exact shadow field reconciliation failed for ' +
                           product + ' / ' + day)


def verify(args):
    require_built(args)
    days = worklist(args)
    existing = set(pg(args, """SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM migration_bulk_verified_days""").splitlines())
    pending = [(product, day) for product, day, _ in days
               if product.encode('utf8').hex() + '|' + day not in existing]
    print('SHADOW_EXACT_VERIFY_DAYS', len(days), 'PENDING', len(pending), flush=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        tasks = {}
        it = iter(pending)
        completed = 0
        while True:
            while len(tasks) < args.workers * 2:
                try:
                    product, day = next(it)
                except StopIteration:
                    break
                tasks[pool.submit(verify_one, args, product, day)] = (product, day)
            if not tasks:
                break
            ready, _ = concurrent.futures.wait(
                tasks, return_when=concurrent.futures.FIRST_COMPLETED)
            for future in ready:
                tasks.pop(future)
                future.result()
                completed += 1
                if completed % 100 == 0 or completed == len(pending):
                    print('SHADOW_EXACT_VERIFY_PROGRESS', completed, '/',
                          len(pending), flush=True)
    if int(pg(args, 'SELECT count(*) FROM migration_bulk_verified_days')) != len(days):
        raise RuntimeError('shadow exact verified days incomplete')
    print('SHADOW_EXACT_RECONCILIATION_OK', len(days), 'DAYS', flush=True)


def cutover(args):
    require_built(args)
    total = len(worklist(args))
    if int(pg(args, 'SELECT count(*) FROM migration_bulk_verified_days')) != total:
        raise RuntimeError('cannot switch: exact reconciliation incomplete')
    if pg(args, """SELECT to_regclass('public.pg_official_asset_details_next') IS NOT NULL""") != 't':
        raise RuntimeError('shadow already switched or unavailable')
    # All index and FK work is completed before this transaction. Prior
    # physical tables remain available for rollback, without deletion.
    pg(args, """
BEGIN;
SET LOCAL lock_timeout='60s';
LOCK TABLE pg_official_granule_keys,pg_official_slot_details,
           pg_official_url_prefixes,pg_official_asset_details
 IN ACCESS EXCLUSIVE MODE;
DO $guard$
BEGIN
 IF (SELECT count(*) FROM migration_bulk_verified_days) <>
    (SELECT count(*) FROM migration_bulk_loaded_days)
    OR (SELECT count(*) FROM migration_bulk_loaded_days) = 0
 THEN RAISE EXCEPTION 'verified shadow day set differs from loaded day set'; END IF;
END
$guard$;
ALTER TABLE pg_official_asset_details RENAME TO pg_official_asset_details_pre_bulk;
ALTER TABLE pg_official_slot_details RENAME TO pg_official_slot_details_pre_bulk;
ALTER TABLE pg_official_granule_keys RENAME TO pg_official_granule_keys_pre_bulk;
ALTER TABLE pg_official_url_prefixes RENAME TO pg_official_url_prefixes_pre_bulk;
ALTER TABLE pg_official_granule_keys_next RENAME TO pg_official_granule_keys;
ALTER TABLE pg_official_url_prefixes_next RENAME TO pg_official_url_prefixes;
ALTER TABLE pg_official_slot_details_next RENAME TO pg_official_slot_details;
ALTER TABLE pg_official_asset_details_next RENAME TO pg_official_asset_details;
CREATE OR REPLACE VIEW official_assets AS
 SELECT k.product_key,k.slot_ts,k.granule_id,a.filename,
        COALESCE(p.prefix,'') || COALESCE(a.url_suffix,a.filename) AS url,
        a.size,a.etag,a.media_type
 FROM pg_official_asset_details a
 JOIN pg_official_granule_keys k ON k.id=a.granule_key
 LEFT JOIN pg_official_url_prefixes p ON p.id=a.url_prefix_id;
CREATE OR REPLACE VIEW official_slots AS
 SELECT k.product_key,k.slot_ts,k.granule_id,
        s.asset_count,s.total_size,s.representative_filename,s.refreshed_at
 FROM pg_official_slot_details s
 JOIN pg_official_granule_keys k ON k.id=s.granule_key;
SELECT setval(pg_get_serial_sequence('pg_official_granule_keys','id'),
              (SELECT max(id) FROM pg_official_granule_keys),true);
SELECT setval(pg_get_serial_sequence('pg_official_url_prefixes','id'),
              (SELECT max(id) FROM pg_official_url_prefixes),true);
COMMIT;
""", script=True)
    print('SHADOW_ATOMIC_SWAP_COMPLETE; OLD_TABLES_PRESERVED; SERVICES_NOT_STARTED',
          flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage', choices=('init','load','build','verify','cutover'),
                        required=True)
    parser.add_argument('--sqlite', required=True)
    parser.add_argument('--database', default='satellite_data')
    parser.add_argument('--socket', default='/run/user/3012/nsmc-postgresql')
    parser.add_argument('--psql', default='psql')
    parser.add_argument('--worklist', required=True)
    parser.add_argument('--prepared-dir', required=True)
    parser.add_argument('--workers', type=int, default=3)
    args = parser.parse_args()
    if not 1 <= args.workers <= 8:
        raise SystemExit('workers must be between 1 and 8')
    if not os.path.isfile(args.sqlite):
        raise SystemExit('original SQLite source missing')
    stage = {'init':init,'load':load,'build':build,
             'verify':verify,'cutover':cutover}[args.stage]
    stage(args)


if __name__ == '__main__':
    main()
