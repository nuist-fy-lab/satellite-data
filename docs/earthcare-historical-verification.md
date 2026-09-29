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

Each invocation is serial and defaults to at most 20 official requests,
512 MiB of expected assets and 15 minutes, with a two-second pause between
requests. A cursor and per-file results allow subsequent runs to resume.
On the user-level systemd timer, a new batch starts about 15 seconds after
the previous one finishes, with a three-second pause between requests; the service is configured in
`deploy/systemd/earthcare-verification.{service,timer}`. A verification
error stops that batch and pauses new requests for 15 minutes, while the
progress database records the failure for review. This is not a deployment
or a source-data mutation job.
If a complete sweep reaches the end without any new official content to
fetch, the job pauses requests for an hour before rechecking the catalogue.
Failures and absent/ambiguous matches remain unverified; review the progress
database instead of treating a filename/size match as a success. A failed
official comparison exits nonzero. `--max-file-bytes` can bound each asset;
`--start-after` applies only when the progress database has no saved cursor.

The inaccessible `AC_CLP_2B` directory cannot be checked by this service
account. Existing ERA5 files without their original CDS request and job
result remain outside this verifier and must not be counted as verified.
