# EarthCARE historical archive verification

The existing shared archive is read-only to this job. Verification does not
move, rename or delete files. The centre SQLite index and a separate `0600`
progress database are the only writes. Run the script as the service user
against the local centre IPC socket, without exporting provider credentials.

```sh
python3 verify-earthcare-archive.py \
  --state-db "$HOME/.local/state/satellite-data/state.sqlite3" \
  --progress-db "$HOME/.local/state/satellite-data/earthcare-verification.sqlite3" \
  --root /misc/nuist_data1/public_sharing/EarthCare \
  --socket "$HOME/.local/state/satellite-data/central.sock"
```

For each file, match the product directory, acquisition date, filename,
size, official item and one physical asset URL. Access-class aliases that
reference the same item and URL count as one asset. Stream the official
asset through the centre, compare its entire SHA-256 with the local file,
and record evidence only if the indexed file is unchanged. The audit database
retains the official URL and digest. The centre's normal scan reflects new
evidence in the calendar; the source file is never changed.

Each invocation uses four bounded waiting slots by default (`--waiting-slots`,
1-8). Content workers stream official SHA-256 and then hash the local file;
the coordinator alone accesses SQLite and records evidence as each slot
finishes. One slow slot does not block completed proofs or admission into
other slots. At most 20 official requests, 512 MiB of expected assets and
15 minutes are admitted by default. Every admitted stream shares the batch
deadline and retains its per-file deadline. Inflight claims are committed
before the cursor advances; after interruption they are recovered before
new work. No source files or partial downloads are written by this job.
On the user-level systemd timer, a new batch starts about 15 seconds after
the previous one finishes, with an initial two-second gap between admissions; the
service is configured in `deploy/systemd/earthcare-verification.{service,timer}`.
The pacing database starts at a 2-second gap and a 20-file batch. After four
low-error official fetches it reduces the gap and increases the batch limit;
on high error rate it halves the batch limit and lengthens the gap. Bounds are
1-12 seconds and 5-40 files per batch. A pipeline batch admits at least twice
the slot count, unless its explicit request or byte limit is smaller. Each
batch remains capped at 512 MiB and 15 minutes. A bounded LRU caches up to
16 product/date catalogue groups (`--match-cache-size`, 1-128), with a maximum
of 4,096 asset rows per group. Larger groups fall back to exact lookups.
External index commits invalidate the cache via SQLite `data_version`.
Evidence writes always check the live catalogue and file metadata again;
the cache contains no credentials, payload hashes or failed verification proofs.
Temporary official HTTP errors (408, 429, 5xx), interrupted streams and
connection failures schedule that file for automatic retry without stopping
other slots or pausing the whole archive. Up to four total attempts use exponential backoff with jitter (initially
30-60 seconds, then 60-120 and 120-240 seconds); a valid `Retry-After` takes
precedence, capped at one day. The timer keeps checking the persisted due time,
and due retries take priority even after the scan cursor has passed them. A
fourth transient failure enters `retry_deferred`: probes are spaced by
approximately 5-6 hours, then 10-12 hours, and finally 19-24 hours, with jitter.
This file's long cooldown does not stop the whole archive; source-level
HTTP 429 and `Retry-After` stop new admissions while existing slots finish.
Authentication rejection also stops admission and pauses the source rather
than repeatedly launching rejected requests. Success resets its retry count. Permanent
HTTP errors, changed local files and hash mismatches stay `pending_review`
without automatic retry. A one-time progress migration restores only known
historical transient failures to a due queue, without upgrading their status.
Official reads also have a 60-second idle timeout and a per-file hard deadline
of 3-10 minutes scaled by file size; a slow trickle stream cannot block
a batch indefinitely and uses the same bounded retry queue. Thread-safe
deadline timers interrupt blocked sockets; process-wide signals are not used.
If the centre database is busy after both hashes agree, the audit retains
the official reference and digest as `record_pending`. Its due retry rechecks
the reference, local file metadata and full local hash, and retries only the
evidence write; it does not refetch ESA bytes or pollute ESA's adaptive error
rate. Busy writes back off with jitter, capped at 30 minutes. Other database
errors remain review-only. Older lock failures lack proof, so their migration
requires a fresh official content comparison.

The centre uses an independent eight-connection reader pool and a single
writer connection. All centre writes queue on that connection with context
cancellation; the external verifier remains protected by SQLite's writer
lock and its 30-second busy timeout. Full local scan results are staged in
the connection-local TEMP database before acquiring the main writer lock;
updating the real index and coverage remains one atomic transaction.
This is not a deployment or a source-data mutation job.
If a complete sweep reaches the end without any new official content to
fetch, the job pauses requests for an hour before rechecking the catalogue.
Failures and absent/ambiguous matches remain unverified; review the progress
database instead of treating a filename/size match as a success. Aggregate
logs include waiting-slot occupancy, LRU hits/misses/evictions and time spent
on official streams, local hashing and evidence writes. Worker times can
overlap and are not wall-clock throughput measurements. Terminal
failures exit nonzero, whereas scheduled transient retries retain their due
time across restarts. `--max-file-bytes` can bound each asset;
`--start-after` applies only when the progress database has no saved cursor.

The inaccessible `AC_CLP_2B` directory cannot be checked by this service
account. Existing ERA5 files without their original CDS request and job
result remain outside this verifier and must not be counted as verified.
