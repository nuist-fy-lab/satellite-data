# Satellite Data

Unified multi-satellite scientific data discovery, download, archive and synchronization service for `nuist-fy-lab`.

The project deliberately separates provider-specific discovery from transfer.
The CLI is a thin control client, never a download worker or credential manager.
The centre's `satellited` owns official catalogues,
calendar/index state and provider-isolated discovery queues; the separate
`satellite-data-downloader` service owns resumable transfer, verification,
staging and archive promotion. Both services are centre-side components; files
are never fetched or archived by the CLI.

The two services communicate over a local Unix socket. Download discovery is
chunked and pipelined: each provider has its own discovery loop, assets are
submitted to the downloader as soon as each chunk is discovered, and every
downloader provider has independent transfer and verification worker pools.
A slow provider therefore cannot consume another provider's slots.
The centre also paces official catalog/search calls per provider: each source
starts at a 500 ms interval and at most two concurrent calls. Repeated errors
lengthen that interval up to 5 seconds and reduce concurrency to one; sustained
success recovers the interval to no less than 200 ms and concurrency to two.
Persistent official-index and download-discovery failures use bounded jittered
backoff in addition to this request pacing.
The main service samples downloader-reported throughput, active transfers,
adaptive limits and queue depth every 15 seconds for the download-page charts.
Samples are kept for eight days; missing samples remain gaps, not zero-speed
observations. `GET /api/v1/monitor/downloads?window=1h` exposes provider-isolated
history (`realtime`, `1h`, `3h`, `6h`, `12h`, `1d`, `2d`, `1w`). "Active transfers"
counts running download workers, not the host's TCP sockets.
The `ci-latest` release retains three generations of binaries: `satellite`
and `satellited` for the latest build, plus `-prev1-<commit>` and
`-prev2-<commit>` assets for the two prior builds. Previous assets are
verified before replacing the release; the oldest generation drops out.
Cloud builds also upload a two-day Actions artifact so an authenticated CLI
can publish the same binaries if the workflow token cannot create a Release.

The centre's state and index share one SQLite file, `state.sqlite3` (default:
`~/.local/state/satellite-data/state.sqlite3`); it is separate from NSMC's
`index.sqlite3` and from the satellite downloader's database. On an existing
installation, stop `satellited` before changing `SATELLITED_STATE_DB` from
`state.db`: make a restorable backup of the database and any `-wal`/`-shm`
files while stopped, rename the database and any remaining sidecars together
to `state.sqlite3`, then update the service environment and restart. The daemon
refuses to create a new `state.sqlite3` beside an unmigrated `state.db`.

### Shared EarthCARE, ERA5 and Himawari archives

On c2ln1 the new EarthCARE archive root is the existing
`/misc/nuist_data1/public_sharing/EarthCare`; files are written under
`<product-type>/<YYYYMMDD>/<official-asset-name>`. ERA5 uses the existing
`/misc/nuist_data1/public_sharing/ERA5` root and writes CDS results under
`CDS/<dataset>/<YYYY>/<MM>/<DD>/<job-id>/<result-name>`. The remaining
satellite products still use `.../public_sharing/satellite`.

Configure `SATELLITED_EARTHCARE_ARCHIVE` and `SATELLITED_ERA5_ARCHIVE` on the
centre and `SATDOWN_EARTHCARE_ARCHIVE_ROOT` and `SATDOWN_ERA5_ARCHIVE_ROOT` on
the downloader. `SATELLITED_HIMAWARI_ARCHIVE` additionally enables the existing
`/misc/nuist_data1/public_sharing/Himawari` historical archive. The centre scans
all configured roots without moving other users'
historical files. `GET /api/v1/archive/index-status` reports file counts and
unreadable directories. Shared historical files are indexed as `unverified`
until an exact official item/asset (EarthCARE) or CDS job/result (ERA5) can be
matched and the file is hashed. A valid HDF5/NetCDF header is only a format
check, not proof of official identity. On this installation the separate
EarthCARE `AC_CLP_2B` directory is owned by another account and is not readable
by the service; its missing coverage is reported instead of hidden. Prior ERA5
global and China files have no automatically inferred CDS job identity. New
archives refuse to overwrite a different file with the same name.
The Himawari historical root inventories `.bz2` payloads, with a header check
only; CERES files are not inferred to be NOAA HSD assets. Shared scans exclude
hidden rollback directories, auxiliary directories, ZIP packages, temporary
files and corrupt partials. `deploy/normalize-earthcare-packages.py` extracts
extensionless EarthCARE ZIP packages after CRC/header checks and exact SHA-256
comparison with any existing payload. It preserves the original ZIP under a
hidden rollback directory and records each operation in a private journal.
New shared archive subdirectories use setgid and group read/write/execute;
new files use group read/write. Existing directories keep their own ACLs,
including inaccessible directories. Completed downloader tasks retain a
durable archive-verification notification until the centre acknowledges it;
an inaccessible official catalogue leaves the file unverified for later retry.

## Current milestone

The first working slice implements anonymous public-cloud discovery for:

| Source | Discovery / transfer | State |
| --- | --- | --- |
| GOES-16/17/18/19 | NOAA NODD object hierarchy, Google Cloud Storage primary | implemented |
| Himawari-8/9 | NOAA NODD anonymous S3 | implemented |
| MTG / Meteosat-12 FCI | EUMETSAT Data Store OpenSearch + OAuth2 download; MQTT publication subscription / EUMETCast Terrestrial for low latency | discovery implemented; authenticated transfer next |
| MODIS Terra/Aqua | LAADS API v2 product catalog + `content/details`; LAADS Download Token + resumable HTTPS | implemented |
| VIIRS Suomi-NPP/NOAA-20/NOAA-21 | NASA CMR/LAADS + LAADS Earthdata Cloud HTTPS | implemented |
| EarthCARE | ESA MAAP STAC + OIDC-protected HTTPS science assets | discovery implemented |
| CALIPSO | NASA CMR + Earthdata Login protected ASDC HTTPS | discovery implemented |
| CloudSat | CIRA DPC THREDDS catalog + authenticated HTTPServer; OPeNDAP for subsets | discovery implemented |
| ERA5 / ERA5-Land | CDS STAC catalogue + asynchronous authenticated subset requests | catalogue, request UI and API implemented; downloader archive integration pending |

The provider choices intentionally avoid retired FTP endpoints, HTML scraping, and legacy NASA APIs.

## Layout

```text
cmd/
  satellite/             thin CLI
  satellited/            API + embedded web daemon
internal/
  app/                   provider wiring
  httpapi/               JSON API
  model/                 Source / Collection / Granule / Asset
  objectstore/           anonymous GCS + S3 listing
  provider/
    calipso/
    cloudsat/
    earthcare/
    era5/
    eumetsat/
    goes/
    himawari/
    laads/
  transfer/              resumable HTTP transfer core
web/                      embedded dependency-free UI
```

## API

The entire production portal requires a web login, including catalogue,
calendar and monitoring pages. `GET /login` is the portal entry; the same
HttpOnly session authorizes the credential page without a second password.
Provider access administration still requires the administrator role.
Existing NSMC portal usernames/passwords are reused; Satellite Key/session
tables are isolated and do not change NSMC's active Keys or sessions.
Operational API calls accept either the portal session or a web-created CLI
API Key; CLI Keys never grant access to provider credentials/accounts.

```text
GET /healthz
GET /api/v1/sources
GET /api/v1/catalog?source=goes18
GET /api/v1/search?source=goes18&collection=goes18:ABI-L1b-RadF&start=2026-09-22T09:00:00Z&end=2026-09-22T10:00:00Z
```

Interactive searches are intentionally limited to seven days. Larger historical requests will be represented as persistent backfill jobs rather than huge synchronous API responses.

### ERA5

ERA5 is ECMWF reanalysis, not satellite observations. The provider reads the
official CDS STAC metadata for hourly/monthly single levels, pressure levels,
and ERA5-Land (six collections). Temporal extent is catalogue metadata, not
proof that every variable/hour has a downloadable file. Calendar indexing and
ordinary granule searches return HTTP 409 for these on-demand datasets; they
never submit CDS jobs implicitly or fabricate official file availability.

Selecting ERA5 in the catalogue opens a subset form using the current CDS
process schema. It requests one UTC day (or one month for monthly products),
with explicit variables, times, pressure levels, format and bounding box.
Direct CDS submission and job/result access require an administrator session. CDS jobs
persist upstream; retain the returned `jobID` to query after a daemon restart.
The UI retains only the latest job ID, not secrets or signed result URLs.
There is no automatic retry of a submission with an uncertain outcome.

In Access Credentials, create one ERA5 access profile with a CDS Personal Access
Token. Previously saved but unbound CDS Tokens appear in the same list as
"pending binding" and can be attached without re-entering the Secret. A
profile saves the provider route, proxy policy and any new encrypted credential
in one transaction; existing shared credentials keep their references.
Accept the requested dataset licence in that CDS account first. All provider
credentials, accounts, proxies and authentication now belong to the centre;
Secrets are encrypted at rest and write-only. The downloader uses private
centre IPC and no longer has a Vault or token environment fallback. See
`docs/central-access.md` for coordinated migration and rollback.

```text
GET  /api/v1/era5/schema?collection=era5:reanalysis-era5-single-levels
POST /api/v1/era5/requests
GET  /api/v1/era5/requests/{jobID}
GET  /api/v1/era5/requests/{jobID}/result
```

POST takes `{"collection":"era5:reanalysis-era5-single-levels","inputs":{...}}`.
`inputs` use the official process schema (for example `variable`, `year`,
`month`, `day`, `time`, `area`, `data_format`, `download_format`). The API sends
the CDS `inputs` envelope and returns immediately rather than waiting in an
index worker. Result assets are returned only from CDS job results, with real
file size and HTTPS location. These low-level job endpoints remain available
for explicit CDS operations. For a complete automatically archived retrieval,
submit one persistent order to `/api/v1/downloads` instead. The centre splits
dates, persists CDS submission checkpoints, polls jobs and enqueues results
for resumable transfer, verification and archive promotion. ERA5 advertises
`catalog`, `retrieval` and `download`, not published-granule `search` or `subscription`.

The link monitor includes CDS API TCP reachability. A current c2ln1 network
snapshot is in `docs/era5-network-2026-09-27.md`.

## CLI

After signing into the portal, create a CLI API Key on its CLI page, then configure the thin
client with `satellite config set`. It prompts for the centre URL and reads the
Key without terminal echo. Only the centre URL and CLI Key are saved in
`~/.config/satellite/config.json` (mode `0600`). The Key is a centre-control
authorization token, not a provider credential or account. Requests send it as
`Authorization: Bearer ...`; redirects are not followed. There is no CLI login,
username/password, provider Token or Vault setup.

```text
satellite config set
satellite source list
satellite product list --search temperature --json
satellite product list --source earthcare --level L2
satellite product show --product goes18:ABI-L1b-RadF
satellite catalog list --source goes18
satellite query list \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2026-09-22T09:00:00Z \
  --end 2026-09-22T10:00:00Z
satellite download submit \
  --source goes18 --collection goes18:ABI-L1b-RadF \
  --start 2023-06-01 --end 2023-06-30 --band 6 --hourly
satellite download status --id 1 --json
satellite query list --product goes18:ABI-L1b-RadF --day 2024-01-01 --band 6
satellite download submit --product goes18:ABI-L1b-RadF --day 2024-01-01 --band 6 --hourly
satellite download submit \
  --product era5:reanalysis-era5-single-levels \
  --start 2024-01-01 --end 2024-01-02 \
  --variable 2m_temperature --area 32.25,118.5,32,118.75
satellite era5 schema --collection era5:reanalysis-era5-single-levels
satellite era5 submit \
  --collection era5:reanalysis-era5-single-levels \
  --variable 2m_temperature --product-type reanalysis \
  --year 2024 --month 01 --day 01 --time 00:00 \
  --area 32.25,-118.75,32,-118.5 \
  --data-format netcdf --download-format unarchived
satellite era5 status --id JOB_ID
satellite era5 result --id JOB_ID
```

ERA5 submission, status and result commands require an administrator-owned CLI
API Key. The provider Token remains in the centre; ordinary user Keys cannot
submit or inspect CDS jobs. For other products or multiple values, pass an
official CDS `inputs` JSON object via `--inputs-file request.json` instead of
the convenience flags. A submission is sent **once**; on an interrupted POST,
check CDS requests before repeating it. `status` polls explicitly and `result`
prints metadata without exposing the signed result URL. These low-level
`era5 submit/status/result` commands do not create centre archive orders.
Use `download submit` above for automatic scheduling and archive completion;
`download status --id NUMBER` reports that one persistent order.
Unlike the low-level CDS commands, unified `download submit --product era5:...`
accepts an ordinary user's CLI Key or portal session. Each new order is bound
to its submitter; ordinary users can list/read only their own orders, and
administrators can inspect all orders. Orders created before this ownership
migration have no owner and remain visible only to administrators.
The service does not hard-code provider-published rate, volume or concurrency
quotas as if they were guaranteed for every account; provider throttling is
separate from local downloader worker limits.

For every source, `product list` (also `catalog list`) browses the centre's stored
official catalogue, including keys, instruments, levels, versions, date metadata
and supported bands. Source is optional and inferred from a full
`--product SOURCE:PRODUCT` key; upstream IDs with additional colons are preserved.
`product show` prints full selection metadata. `query list` takes one UTC `--day`
or an exclusive `--start/--end` interval up to 31 days. The centre handles query
segmentation, deduplication and band filtering; queries never enqueue orders.
For ERA5, querying without date flags displays the official schema rather than
inventing published granules.

One `download submit` creates a persistent order for any supported product.
The CLI sends it once and can disconnect immediately. The centre owns discovery,
batching, retries, credentials and completion monitoring; the separate downloader
owns transfer, verification and archive promotion. Long orders yield between
segments so another order on the same provider can progress.
ERA5 orders accept repeatable/comma-separated `--variable` and `--pressure-level`,
optional `--area north,west,south,east`, `--time`, and `--data-format`.
Advanced unified-order `--inputs-file` selections omit year/month/day: the centre
derives dates from start/end. Hourly products split by UTC day; monthly products
require complete months and split by month. The centre supplies dataset-aware
date field types, product type and format defaults. An uncertain CDS POST fails
the persistent order safely without repeating it; an operator must reconcile
the existing CDS requests before resubmitting.

`SATELLITE_CONFIG` selects a different private config file. `SATELLITE_SERVER`
and `SATELLITE_API_KEY` can override the saved centre connection in an existing
secure environment; never pass a Key as a command argument or paste it into
shell history. The CLI's scope is download
task submission, catalogue/task/status queries and ordinary settings changes.
It has no credential/account commands, Vault, encryption key, provider login,
or local file-transfer responsibility. Provider status reports configuration status,
not account or credential references. All provider accounts and Secrets remain
in the centre and are managed through its protected administration interface.
Only web sessions can create/list/revoke their own CLI Keys; even an administrator's
Bearer Key cannot manage provider credentials/accounts or mint more Keys.
The full Key is returned only once at creation. The centre stores its hash,
tracks last use and rejects revoked Keys or Keys whose owner is disabled.
Satellite Keys use `satellite_v1_`; NSMC Keys are not interchangeable. All
catalogue/calendar/monitoring access and task submissions require a portal
session or a valid CLI Key. Keys control shared centre tasks;
this is an operator API, not a multi-tenant per-user archive.

Ordinary settings, including adding, removing or changing provider proxy routes,
must be implemented through a centre-owned settings API, not by editing local
account/Vault data. Proxy authentication is a Secret and remains centre-only;
CLI settings must not accept or return proxy passwords or credential payloads.
The current CLI command list above does not yet include proxy settings commands.

`download submit` takes inclusive UTC dates and supports repeatable `--time HH:MM` or `--hourly`.
The hourly shortcut requests all 24 nominal `HH:00` UTC slots each day.
`--band` is available only for products with
a configured band-to-filename profile. A request is persistent and may discover
and transfer files asynchronously; inspect `download status` before treating it
as complete. The existing `download create` command retains its exclusive end.
For fixed-cadence products, requested `HH:MM` is a nominal slot: scan starts
within an adaptive, bounded offset match that slot, including at UTC date
boundaries. For non-fixed products, events retain their actual timestamps and
requested times match only isolated nearby events. The year calendar uses the
same policy; it exposes actual and nominal times separately and switches to an
event axis rather than silently snapping scans outside the tolerance. Timing
policies are configured in `internal/slotaxis/profiles.json`.
The download page uses the same API with a normalized provider catalogue. Band
profiles for GOES ABI and Himawari AHI full-disk live in
`internal/downloadfilter/profiles.json`; other provider metadata
comes from their existing catalogue adapters. Download transfer and archive
promotion remain in the separate downloader service.

## Verification policy

Project builds and tests run only in GitHub Actions. Development machines and production data nodes are used for source editing, configuration, deployment and post-deployment health checks, not project builds/tests.

On the data node, the user-level units are `satellited.service` and
`satellite-data-downloader.service`. Check the exact unit names with
`systemctl --user list-unit-files '*satellite*'` before diagnosing a service;
`systemctl --user show` can report `inactive` for a unit that does not exist.
There is no `satellite-downloader.service` unit.

Linux deployment artifacts are built with `CGO_ENABLED=0` and `GOAMD64=v1` so the same CI artifact can run on older HPC userspace distributions without inheriting the GitHub runner's glibc requirement.
