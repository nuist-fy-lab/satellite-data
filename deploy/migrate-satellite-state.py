#!/usr/bin/env python3
"""Copy the stopped satellite SQLite state into a separate PostgreSQL database.

Uses constant-memory PostgreSQL COPY batches and preserves every source row.
The original SQLite database is never written or deleted. The final cutover
is a separate operator action, contingent on a complete reconciliation.
Requires the target schema to have been initialized by:
  satellited -init-state-only (with SATELLITED_POSTGRES_DSN configured).

PostgreSQL auth uses an existing local Unix socket/peer identity. No password
is accepted as a command-line argument.
"""
import argparse
import concurrent.futures
import os
import re
import sqlite3
import subprocess
import sys
import time

PSQL_DEFAULT = 'psql'
IDENTIFIER = re.compile(r'^[a-z_][a-z0-9_]*$')
SPECIAL = ('official_assets', 'official_slots')
ORDER = [
    'products', 'catalog_sources', 'official_days', 'official_work',
    'archive_day_cursor', 'local_files', 'local_file_origins', 'local_slots',
    'day_summary', 'coverage_day_cache', 'local_scan_state',
    'verified_archive_files', 'archive_scan_status', 'link_samples',
    'download_monitor_samples', 'download_requests', 'download_groups',
    'download_group_members', 'download_request_tasks', 'era5_order_inputs',
    'era5_order_parts', 'era5_result_manifest', 'era5_result_slices',
]


def quote(value):
    if not IDENTIFIER.fullmatch(value):
        raise ValueError('unsupported SQL identifier')
    return '"' + value + '"'


def psql(argv, statement, stdin=None):
    command = [argv.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-h',
               argv.socket, '-d', argv.database]
    if stdin is None:
        command += ['-c', statement]
    else:
        command += ['-f', '-']
    process = subprocess.run(command, input=stdin, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE)
    if process.returncode:
        raise RuntimeError('PostgreSQL migration operation failed: ' +
                           process.stderr.decode('utf-8', 'replace')[-800:])
    return process.stdout.decode('utf-8', 'replace').strip()


def copy_escape(value):
    if value is None:
        return r'\N'
    if isinstance(value, bytes):
        raise ValueError('binary SQLite field requires an explicit binary schema')
    data = str(value)
    if '\x00' in data:
        raise ValueError('PostgreSQL does not permit NUL in text; migration halted without conversion')
    return data.replace('\\', '\\\\').replace('\t', r'\t').replace('\r', r'\r').replace('\n', r'\n')


def copy_table(argv, sqlite, source_name, target_name):
    table = quote(source_name)
    target = quote(target_name)
    columns = [item[1] for item in sqlite.execute('PRAGMA table_info(' + table + ')')]
    if not columns:
        raise RuntimeError('source table has no columns: ' + source_name)
    statement = 'COPY ' + target + '(' + ','.join(map(quote, columns)) + ') FROM STDIN'
    cursor = sqlite.execute('SELECT ' + ','.join(map(quote, columns)) + ' FROM ' + table)
    # One COPY session per table, rather than launching psql once per 5,000
    # rows. SQLite is still read in bounded batches; no table-sized buffer.
    process = subprocess.Popen(
        [argv.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-h',
         argv.socket, '-d', argv.database, '-c', statement],
        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    count = 0
    try:
        while True:
            batch = cursor.fetchmany(5000)
            if not batch:
                break
            for record in batch:
                line = '\t'.join(copy_escape(value) for value in record) + '\n'
                process.stdin.write(line.encode('utf-8'))
            count += len(batch)
            if count % 100000 == 0:
                print('COPY_PROGRESS', source_name, count, flush=True)
        process.stdin.close()
        process.stdin = None
        stderr = process.communicate()[1]
        if process.returncode:
            raise RuntimeError('COPY failed: ' + stderr.decode('utf-8', 'replace')[-800:])
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
    return count


def create_staging(argv):
    psql(argv, """
CREATE UNLOGGED TABLE IF NOT EXISTS migration_official_slots(
 product_key TEXT NOT NULL,slot_ts TEXT NOT NULL,granule_id TEXT NOT NULL,
 asset_count BIGINT NOT NULL,total_size BIGINT NOT NULL,
 representative_filename TEXT NOT NULL,refreshed_at TEXT NOT NULL);
CREATE UNLOGGED TABLE IF NOT EXISTS migration_official_assets(
 product_key TEXT NOT NULL,slot_ts TEXT NOT NULL,granule_id TEXT NOT NULL,
 filename TEXT NOT NULL,url TEXT NOT NULL,size BIGINT NOT NULL,
 etag TEXT NOT NULL,media_type TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS satellite_migration_log (
 source_table TEXT PRIMARY KEY,source_rows BIGINT NOT NULL,
 imported_at TIMESTAMPTZ NOT NULL DEFAULT now());
""")


def copy_source_table(argv, sqlite, name, destination):
    finished = psql(argv, "SELECT source_rows FROM satellite_migration_log WHERE source_table='" + name + "'")
    if finished:
        count = int(finished)
        current = int(psql(argv, 'SELECT count(*) FROM ' + quote(destination)))
        if current != count:
            raise RuntimeError('previous table count differs: ' + name)
        print('ALREADY_IMPORTED', name, count, flush=True)
        return
    count = int(psql(argv, 'SELECT count(*) FROM ' + quote(destination)))
    if count:
        raise RuntimeError('destination has unverified partial data: ' + destination +
                           '; use a fresh isolated PostgreSQL database')
    started = time.monotonic()
    print('IMPORT_START', name, flush=True)
    imported = copy_table(argv, sqlite, name, destination)
    target_rows = int(psql(argv, 'SELECT count(*) FROM ' + quote(destination)))
    if imported != target_rows:
        raise RuntimeError('COPY row mismatch for ' + name)
    psql(argv, 'INSERT INTO satellite_migration_log(source_table,source_rows) VALUES (' +
         "'" + name + "'," + str(imported) + ')')
    print('IMPORT_DONE', name, imported, 'elapsed_seconds',
          round(time.monotonic() - started, 2), flush=True)


def sql_literal(value):
    return "'" + value.replace("'", "''") + "'"


def official_days(argv):
    # Hex-encoded names preserve punctuation without depending on psql's
    # delimiter or the output encoding of a particular terminal. Each stage
    # table is ordered by (product_key, substr(slot_ts,1,10)). A loose
    # index walk seeks to the NEXT distinct key rather than scanning and
    # grouping every one of the ~26 million source rows. Both sides are
    # walked independently so even orphan assets/days remain included.
    output = psql(argv, """
WITH RECURSIVE asset_days AS (
 (SELECT product_key, substr(slot_ts,1,10) AS day
  FROM migration_official_assets
  ORDER BY product_key,substr(slot_ts,1,10) LIMIT 1)
 UNION ALL
 SELECT next_key.product_key,next_key.day
 FROM asset_days previous
 CROSS JOIN LATERAL (
  SELECT product_key, substr(slot_ts,1,10) AS day
  FROM migration_official_assets
  WHERE (product_key,substr(slot_ts,1,10)) >
        (previous.product_key,previous.day)
  ORDER BY product_key,substr(slot_ts,1,10) LIMIT 1
 ) next_key
), slot_days AS (
 (SELECT product_key, substr(slot_ts,1,10) AS day
  FROM migration_official_slots
  ORDER BY product_key,substr(slot_ts,1,10) LIMIT 1)
 UNION ALL
 SELECT next_key.product_key,next_key.day
 FROM slot_days previous
 CROSS JOIN LATERAL (
  SELECT product_key, substr(slot_ts,1,10) AS day
  FROM migration_official_slots
  WHERE (product_key,substr(slot_ts,1,10)) >
        (previous.product_key,previous.day)
  ORDER BY product_key,substr(slot_ts,1,10) LIMIT 1
 ) next_key
)
SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM (
 SELECT product_key,day FROM asset_days
 UNION
 SELECT product_key,day FROM slot_days
) days ORDER BY 1,2;
""")
    return [(bytes.fromhex(encoded).decode('utf8'), day)
            for encoded, day in (line.split('|', 1) for line in output.splitlines())]


def prepare_normalization(argv):
    # Stage tables are unlogged, whereas the normalized target tables remain
    # durable. Only temporary lookup indexes are built over the stage data.
    psql(argv, """
ALTER TABLE pg_official_asset_details ALTER COLUMN url_suffix DROP NOT NULL;
CREATE OR REPLACE VIEW official_assets AS
 SELECT k.product_key,k.slot_ts,k.granule_id,a.filename,
        COALESCE(p.prefix,'') || COALESCE(a.url_suffix,a.filename) AS url,
        a.size,a.etag,a.media_type
 FROM pg_official_asset_details a
 JOIN pg_official_granule_keys k ON k.id=a.granule_key
 LEFT JOIN pg_official_url_prefixes p ON p.id=a.url_prefix_id;
CREATE TABLE IF NOT EXISTS migration_normalized_days(
 product_key TEXT NOT NULL,day TEXT NOT NULL,
 PRIMARY KEY(product_key,day));
CREATE INDEX IF NOT EXISTS migration_official_assets_day_idx
 ON migration_official_assets(product_key,(substr(slot_ts,1,10)));
CREATE INDEX IF NOT EXISTS migration_official_slots_day_idx
 ON migration_official_slots(product_key,(substr(slot_ts,1,10)));
ANALYZE migration_official_assets;
ANALYZE migration_official_slots;
""")


def normalization_sql(product, day):
    product, day = sql_literal(product), sql_literal(day)
    where = "product_key=" + product + " AND substr(slot_ts,1,10)=" + day
    # All four insertions plus the progress marker commit together. One
    # interrupted day is therefore restartable without partial results.
    return """
INSERT INTO pg_official_granule_keys(product_key,slot_ts,granule_id)
SELECT product_key,slot_ts,granule_id FROM (
  SELECT product_key,slot_ts,granule_id FROM migration_official_slots WHERE {where}
  UNION
  SELECT product_key,slot_ts,granule_id FROM migration_official_assets WHERE {where}
) granules ON CONFLICT DO NOTHING;
INSERT INTO pg_official_slot_details(granule_key,asset_count,total_size,representative_filename,refreshed_at)
SELECT k.id,s.asset_count,s.total_size,s.representative_filename,s.refreshed_at
FROM migration_official_slots s
JOIN pg_official_granule_keys k USING(product_key,slot_ts,granule_id)
WHERE s.product_key={product} AND substr(s.slot_ts,1,10)={day}
ON CONFLICT(granule_key) DO NOTHING;
INSERT INTO pg_official_url_prefixes(prefix)
SELECT DISTINCT CASE WHEN strpos(reverse(url),'/')>0
  THEN left(url,length(url)-strpos(reverse(url),'/')+1) ELSE '' END
FROM migration_official_assets WHERE {where}
ORDER BY 1
ON CONFLICT(prefix) DO NOTHING;
INSERT INTO pg_official_asset_details(granule_key,filename,url_prefix_id,url_suffix,size,etag,media_type)
SELECT k.id,a.filename,p.id,
       CASE WHEN suffix.suffix=a.filename THEN NULL ELSE suffix.suffix END,
       a.size,a.etag,a.media_type
FROM migration_official_assets a
JOIN pg_official_granule_keys k USING(product_key,slot_ts,granule_id)
JOIN pg_official_url_prefixes p ON p.prefix =
 CASE WHEN strpos(reverse(a.url),'/')>0
  THEN left(a.url,length(a.url)-strpos(reverse(a.url),'/')+1) ELSE '' END
CROSS JOIN LATERAL (
 SELECT substring(a.url FROM length(p.prefix)+1) AS suffix
) suffix
WHERE a.product_key={product} AND substr(a.slot_ts,1,10)={day}
ON CONFLICT(granule_key,filename) DO NOTHING;
INSERT INTO migration_normalized_days(product_key,day) VALUES({product},{day})
ON CONFLICT DO NOTHING;
""".format(where=where, product=product, day=day)


def normalize_day(argv, product, day):
    # One product/day per transaction: independent workers share no granule
    # identities. Checkpoint and all inserts commit atomically.
    statement = ("BEGIN;\nSET LOCAL lock_timeout = '45s';\n" +
                 normalization_sql(product, day) + "\nCOMMIT;\n")
    psql(argv, '', statement.encode('utf8'))


def normalize_parallel(argv, pending):
    # Bound both database connections and the number of queued jobs. Ordered
    # prefix insertion prevents lock-order inversions between unrelated days.
    # Avoid rerunning a day after success: every successful transaction writes
    # its own durable migration_normalized_days checkpoint.
    workers = argv.workers
    submitted, completed = 0, 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as executor:
        futures = {}
        it = iter(pending)
        try:
            while True:
                while len(futures) < workers * 2:
                    try:
                        product, day = next(it)
                    except StopIteration:
                        break
                    futures[executor.submit(normalize_day, argv, product, day)] = (product, day)
                    submitted += 1
                if not futures:
                    break
                finished, _ = concurrent.futures.wait(
                    futures, return_when=concurrent.futures.FIRST_COMPLETED)
                for future in finished:
                    product, day = futures.pop(future)
                    try:
                        future.result()
                    except Exception as exc:
                        raise RuntimeError('normalization failed at ' +
                                           repr(product) + ' ' + day + ': ' + str(exc))
                    completed += 1
                    if completed % 64 == 0 or completed == len(pending):
                        print('PARALLEL_NORMALIZATION_PROGRESS', completed, '/',
                              len(pending), 'workers', workers, flush=True)
        except Exception:
            for future in futures:
                future.cancel()
            raise


def normalize_assets(argv):
    for name in ('official_assets', 'official_slots'):
        if not psql(argv, "SELECT source_rows FROM satellite_migration_log WHERE source_table='" + name + "'"):
            raise RuntimeError('original index import is incomplete: ' + name)
    if psql(argv, "SELECT source_rows FROM satellite_migration_log WHERE source_table='normalized_assets'"):
        print('NORMALIZATION_PREVIOUSLY_COMPLETED', flush=True)
        return
    print('PREPARING_BOUNDED_NORMALIZATION', flush=True)
    prepare_normalization(argv)
    if argv.worklist:
        weights = {}
        with open(argv.worklist, encoding='utf8') as planned:
            for entry in planned:
                encoded, day, weight = entry.rstrip('\n').split('\t')
                product = bytes.fromhex(encoded).decode('utf8')
                key = (product, day)
                if key in weights:
                    raise RuntimeError('duplicate product/day in worklist: ' + repr(key))
                weights[key] = int(weight)
        # The worklist is a complete enumeration of both original SQLite
        # relations made on Slurm compute. The result is validated by exact
        # source/target row counts and a later EXCEPT ALL of every field.
        # Bypass a 26-million-row PostgreSQL GROUP/UNION scan entirely.
        days = list(weights)
        print('PREPARED_SOURCE_DAYS', len(days), flush=True)
    else:
        # Fallback if no compute-generated worklist is supplied.
        days = official_days(argv)
    finished = set(psql(argv, """
SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM migration_normalized_days;
""").splitlines())
    pending = [(product, day) for product, day in days
               if product.encode('utf8').hex() + '|' + day not in finished]
    if argv.worklist:
        pending.sort(key=lambda item: weights.get(item, 0), reverse=True)
        print('PREPARED_PARALLEL_WORKLIST', len(weights), flush=True)
    print('NORMALIZATION_DAYS', len(days), 'PENDING', len(pending), flush=True)
    if argv.workers > 1:
        normalize_parallel(argv, pending)
    else:
        for i in range(0, len(pending), 8):
            chunk = pending[i:i+8]
            statement = 'BEGIN;\n' + '\n'.join(
                normalization_sql(product, day) for product, day in chunk) + '\nCOMMIT;\n'
            psql(argv, '', statement.encode('utf8'))
            if (i+len(chunk)) % 64 == 0 or i+len(chunk) == len(pending):
                print('NORMALIZATION_PROGRESS', i+len(chunk), '/', len(pending), flush=True)
    # Counts are a first gate. Full row equality is separately enforced by
    # verify_assets before any staging table can be removed.
    original = int(psql(argv, 'SELECT count(*) FROM migration_official_assets'))
    compact = int(psql(argv, 'SELECT count(*) FROM official_assets'))
    slots = int(psql(argv, 'SELECT count(*) FROM migration_official_slots'))
    compact_slots = int(psql(argv, 'SELECT count(*) FROM official_slots'))
    if (original, slots) != (compact, compact_slots):
        raise RuntimeError('granule/asset conservation failure')
    psql(argv, "INSERT INTO satellite_migration_log(source_table,source_rows) VALUES ('normalized_assets'," +
         str(compact) + ')')
    print('NORMALIZED_ROWS', compact, 'SLOT_ROWS', compact_slots, flush=True)


def verify_assets(argv):
    if not psql(argv, "SELECT source_rows FROM satellite_migration_log WHERE source_table='normalized_assets'"):
        raise RuntimeError('no normalized data to compare')
    psql(argv, """
CREATE TABLE IF NOT EXISTS migration_verified_days (
 product_key TEXT NOT NULL,day TEXT NOT NULL,PRIMARY KEY(product_key,day));
ANALYZE pg_official_granule_keys;
ANALYZE pg_official_asset_details;
""")
    rows = psql(argv, """
SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM migration_normalized_days ORDER BY 1,2;
""").splitlines()
    checked = set(psql(argv, """
SELECT encode(convert_to(product_key,'UTF8'),'hex'),day
FROM migration_verified_days;
""").splitlines())
    pending = [line for line in rows if line not in checked]
    print('RECONCILIATION_DAYS', len(rows), 'PENDING', len(pending), flush=True)
    for i, line in enumerate(pending, 1):
        encoded, day = line.split('|', 1)
        product = bytes.fromhex(encoded).decode('utf8')
        statement = reconciliation_sql(product, day)
        result = psql(argv, statement)
        if result.strip() != '1':
            raise RuntimeError('complete field-level reconciliation failed for ' +
                               repr(product) + ' on ' + day)
        if i % 64 == 0 or i == len(pending):
            print('RECONCILIATION_PROGRESS', i, '/', len(pending), flush=True)
    if int(psql(argv, 'SELECT count(*) FROM migration_verified_days')) != len(rows):
        raise RuntimeError('missing product/day verification checkpoints')
    print('EXACT_INDEX_RECONCILIATION_OK', len(rows), 'DAYS', flush=True)


def reconciliation_sql(product, day):
    product, day = sql_literal(product), sql_literal(day)
    asset_fields = 'product_key,slot_ts,granule_id,filename,url,size,etag,media_type'
    slot_fields = 'product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at'
    where = 'product_key=' + product + ' AND substr(slot_ts,1,10)=' + day
    source_assets = 'SELECT ' + asset_fields + ' FROM migration_official_assets WHERE ' + where
    target_assets = 'SELECT ' + asset_fields + ' FROM official_assets WHERE ' + where
    source_slots = 'SELECT ' + slot_fields + ' FROM migration_official_slots WHERE ' + where
    target_slots = 'SELECT ' + slot_fields + ' FROM official_slots WHERE ' + where
    return """
WITH asset_missing AS ({source_assets} EXCEPT ALL {target_assets}),
     asset_extra   AS ({target_assets} EXCEPT ALL {source_assets}),
     slot_missing  AS ({source_slots} EXCEPT ALL {target_slots}),
     slot_extra    AS ({target_slots} EXCEPT ALL {source_slots})
INSERT INTO migration_verified_days(product_key,day)
SELECT {product},{day} WHERE
  NOT EXISTS (SELECT 1 FROM asset_missing)
  AND NOT EXISTS (SELECT 1 FROM asset_extra)
  AND NOT EXISTS (SELECT 1 FROM slot_missing)
  AND NOT EXISTS (SELECT 1 FROM slot_extra)
ON CONFLICT DO NOTHING RETURNING 1;
""".format(source_assets=source_assets, target_assets=target_assets,
           source_slots=source_slots, target_slots=target_slots,
           product=product, day=day)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sqlite', required=True, help='stopped, preserved satellite SQLite state')
    parser.add_argument('--database', default='satellite_data')
    parser.add_argument('--socket', default='/var/run/postgresql')
    parser.add_argument('--psql', default=PSQL_DEFAULT)
    parser.add_argument('--stage', choices=('import', 'normalize', 'verify'), required=True)
    parser.add_argument('--workers', type=int, default=1,
                        help='parallel PostgreSQL normalization sessions (1-16)')
    parser.add_argument('--worklist',
                        help='optional computed product/day size hints; never filters records')
    args = parser.parse_args()
    if args.workers < 1 or args.workers > 16:
        raise SystemExit('--workers must be in the range 1..16')
    if not os.path.isfile(args.sqlite):
        raise SystemExit('SQLite source does not exist')
    if args.database in ('postgres', 'nsmc', 'nsmc_central', 'nsmc_web'):
        raise SystemExit('Refusing non-satellite PostgreSQL database')
    source = sqlite3.connect('file:' + args.sqlite + '?mode=ro', uri=True, timeout=20)
    try:
        if args.stage == 'import':
            create_staging(args)
            existing = set(row[0] for row in source.execute(
                "SELECT name FROM sqlite_master WHERE type='table'"))
            for name in ORDER:
                if name in existing:
                    copy_source_table(args, source, name, name)
            for name in SPECIAL:
                if name in existing:
                    copy_source_table(args, source, name, 'migration_' + name)
            # Keep generated sequence IDs strictly above historical IDs.
            for name in ('download_requests', 'download_groups'):
                psql(args, "SELECT setval(pg_get_serial_sequence('" + name +
                     "','id'),GREATEST((SELECT COALESCE(MAX(id),0) FROM " +
                     name + "),1),true)")
        elif args.stage == 'normalize':
            normalize_assets(args)
        else:
            verify_assets(args)
    finally:
        source.close()


if __name__ == '__main__':
    main()
