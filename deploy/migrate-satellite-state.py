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
               argv.socket, '-d', argv.database, '-c', statement]
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
    count = 0
    while True:
        batch = cursor.fetchmany(5000)
        if not batch:
            break
        process = subprocess.Popen(
            [argv.psql, '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-h',
             argv.socket, '-d', argv.database, '-c', statement],
            stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            for record in batch:
                line = '\t'.join(copy_escape(value) for value in record) + '\n'
                process.stdin.write(line.encode('utf-8'))
            process.stdin.close()
            process.stdin = None
            stderr = process.communicate()[1]
            if process.returncode:
                raise RuntimeError('COPY failed: ' + stderr.decode('utf-8', 'replace')[-800:])
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
        count += len(batch)
        if count % 100000 == 0:
            print('COPY_PROGRESS', source_name, count, flush=True)
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


def normalize_assets(argv):
    for name in ('official_assets', 'official_slots'):
        if not psql(argv, "SELECT source_rows FROM satellite_migration_log WHERE source_table='" + name + "'"):
            raise RuntimeError('original index import is incomplete: ' + name)
    if psql(argv, "SELECT source_rows FROM satellite_migration_log WHERE source_table='normalized_assets'"):
        print('NORMALIZATION_PREVIOUSLY_COMPLETED', flush=True)
        return
    print('NORMALIZING_FULL_GRANULE_IDENTITIES', flush=True)
    psql(argv, """
INSERT INTO pg_official_granule_keys(product_key,slot_ts,granule_id)
SELECT product_key,slot_ts,granule_id FROM migration_official_slots
UNION
SELECT product_key,slot_ts,granule_id FROM migration_official_assets
ON CONFLICT DO NOTHING;
""")
    print('NORMALIZING_SLOT_DETAILS', flush=True)
    psql(argv, """
INSERT INTO pg_official_slot_details(granule_key,asset_count,total_size,representative_filename,refreshed_at)
SELECT k.id,s.asset_count,s.total_size,s.representative_filename,s.refreshed_at
FROM migration_official_slots s
JOIN pg_official_granule_keys k USING(product_key,slot_ts,granule_id)
ON CONFLICT(granule_key) DO NOTHING;
""")
    print('NORMALIZING_URL_DICTIONARY', flush=True)
    psql(argv, """
INSERT INTO pg_official_url_prefixes(prefix)
SELECT DISTINCT CASE WHEN strpos(reverse(url),'/')>0
  THEN left(url,length(url)-strpos(reverse(url),'/')+1) ELSE '' END
FROM migration_official_assets
ON CONFLICT(prefix) DO NOTHING;
""")
    print('NORMALIZING_FILE_ASSETS', flush=True)
    psql(argv, """
INSERT INTO pg_official_asset_details(granule_key,filename,url_prefix_id,url_suffix,size,etag,media_type)
SELECT k.id,a.filename,p.id,
       substring(a.url FROM length(p.prefix)+1),
       a.size,a.etag,a.media_type
FROM migration_official_assets a
JOIN pg_official_granule_keys k USING(product_key,slot_ts,granule_id)
JOIN pg_official_url_prefixes p ON p.prefix =
 CASE WHEN strpos(reverse(a.url),'/')>0
  THEN left(a.url,length(a.url)-strpos(reverse(a.url),'/')+1) ELSE '' END
ON CONFLICT(granule_key,filename) DO NOTHING;
""")
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
    print('VERIFYING_ALL_ASSET_FIELDS', flush=True)
    # This is exact multiset equality over all eight original columns, not
    # a sampled checksum, and reports any different or missing record.
    mismatch = psql(argv, """
SELECT (SELECT count(*) FROM (
 (SELECT product_key,slot_ts,granule_id,filename,url,size,etag,media_type
  FROM migration_official_assets
  EXCEPT ALL
  SELECT product_key,slot_ts,granule_id,filename,url,size,etag,media_type FROM official_assets)
 UNION ALL
 (SELECT product_key,slot_ts,granule_id,filename,url,size,etag,media_type FROM official_assets
  EXCEPT ALL
  SELECT product_key,slot_ts,granule_id,filename,url,size,etag,media_type FROM migration_official_assets)
 ) diff) + (SELECT count(*) FROM (
 (SELECT product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at FROM migration_official_slots
  EXCEPT ALL
  SELECT product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at FROM official_slots)
 UNION ALL
 (SELECT product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at FROM official_slots
  EXCEPT ALL
  SELECT product_key,slot_ts,granule_id,asset_count,total_size,representative_filename,refreshed_at FROM migration_official_slots)
 ) diff);
""")
    if int(mismatch):
        raise RuntimeError('full field-by-field official index reconciliation failed; mismatches=' + mismatch)
    print('EXACT_INDEX_RECONCILIATION_OK', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sqlite', required=True, help='stopped, preserved satellite SQLite state')
    parser.add_argument('--database', default='satellite_data')
    parser.add_argument('--socket', default='/var/run/postgresql')
    parser.add_argument('--psql', default=PSQL_DEFAULT)
    parser.add_argument('--stage', choices=('import', 'normalize', 'verify'), required=True)
    args = parser.parse_args()
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
