# Satellite Data

[English](README.md) | [简体中文](README.zh-CN.md)

Unified scientific data discovery, retrieval, archive indexing and monitoring for
`nuist-fy-lab`.

`satellite-data` provides one control plane for heterogeneous satellite and
reanalysis providers while keeping provider credentials, discovery, transfer and
archive responsibilities clearly separated.

## Highlights

- **One catalogue across providers** — GOES, Himawari, MODIS, VIIRS, MTG,
  EarthCARE, CALIPSO, CloudSat and ERA5/ERA5-Land.
- **Persistent download orders** — clients submit once; the centre owns discovery,
  batching, retries and completion tracking.
- **Provider isolation** — discovery, transfer and verification pools are isolated
  so a slow provider does not consume another provider's slots.
- **Resumable, verified archive promotion** — transfer and verification are handled
  by the separate `satellite-data-downloader` service.
- **Shared archive awareness** — existing EarthCARE, ERA5, Himawari and GOES
  holdings can be indexed without silently moving or overwriting historical data.
- **Live operations UI** — catalogue, calendars, archive coverage, download state,
  provider throughput and queue telemetry are exposed through the embedded web UI.
- **Centralized secrets** — provider accounts, credentials and proxy authentication
  stay in the centre; the CLI never becomes a credential manager or download worker.

## Architecture

```text
                        portal / CLI
                             │
                             ▼
                    ┌─────────────────┐
                    │   satellited    │
                    │─────────────────│
                    │ auth + API      │
                    │ catalogues      │
                    │ discovery       │
                    │ order scheduler │
                    │ archive index   │
                    │ monitoring      │
                    └────────┬────────┘
                             │ private Unix socket
                             ▼
                 ┌───────────────────────────┐
                 │ satellite-data-downloader │
                 │───────────────────────────│
                 │ resumable transfer        │
                 │ verification              │
                 │ staging                   │
                 │ archive promotion         │
                 └─────────────┬─────────────┘
                               │
                               ▼
                         shared archives
```

### Component boundaries

| Component | Owns | Does not own |
| --- | --- | --- |
| `satellited` | API, portal, provider catalogues, discovery, persistent orders, credentials, proxy routes, archive index, monitoring | File transfer workers |
| `satellite-data-downloader` | Transfer queues, resumable downloads, verification, staging, archive promotion | Provider credentials or user-facing authentication |
| `satellite` CLI | Control requests, catalogue/query/status output, order submission | Provider logins, secrets, local downloading |
| Embedded web UI | Catalogue, calendars, access administration, downloads, monitoring | Direct provider access |

The centre and downloader communicate over a local Unix socket. Download
discovery is chunked and pipelined: assets are sent to the downloader as soon as
each discovery chunk is ready instead of waiting for the entire request to finish.

Provider catalogue/search calls are independently paced and adapt to repeated
errors. Persistent failures use bounded jittered backoff, so one degraded upstream
does not stall the whole service.

## Supported data sources

| Source | Official discovery path | Current project state |
| --- | --- | --- |
| GOES-16/17/18/19 | NOAA NODD object hierarchy; Google Cloud Storage primary | Discovery + transfer implemented |
| Himawari-8/9 | NOAA NODD anonymous S3 | Discovery + transfer implemented |
| MODIS Terra/Aqua | LAADS API v2 catalogue + `content/details` | Discovery + authenticated resumable transfer implemented |
| VIIRS Suomi-NPP / NOAA-20 / NOAA-21 | NASA CMR + LAADS Earthdata Cloud | Discovery + transfer implemented |
| MTG / Meteosat-12 FCI | EUMETSAT Data Store OpenSearch + OAuth2; MQTT/EUMETCast paths for low latency | Discovery implemented; authenticated transfer next |
| EarthCARE | ESA MAAP STAC + OIDC-protected science assets | Discovery implemented |
| CALIPSO | NASA CMR + Earthdata Login / ASDC | Discovery implemented |
| CloudSat | CIRA DPC THREDDS + authenticated HTTPServer; OPeNDAP for subsets | Discovery implemented |
| ERA5 / ERA5-Land | CDS STAC catalogue + asynchronous subset requests | Catalogue, request UI/API and persistent orders implemented; archive integration continues |

Provider integrations intentionally avoid retired FTP endpoints, HTML scraping and
legacy NASA APIs when an official machine-readable interface exists.

> **ERA5 is not a satellite observation product.** It is included because the
> project provides a unified retrieval and archive workflow for satellite-oriented
> research datasets.

## Typical workflow

1. `satellited` refreshes or queries the official provider catalogue.
2. A user browses the portal or CLI and submits a persistent download order.
3. The centre segments the request, performs provider-specific discovery and
   records durable progress.
4. Discovered assets are streamed to `satellite-data-downloader` in chunks.
5. The downloader resumes transfers, verifies payloads and promotes completed
   files into the configured archive.
6. Archive events return to the centre, which updates coverage and calendar state.
7. The web UI receives incremental state through SSE where appropriate instead of
   repeatedly reloading full datasets.

For long orders, the scheduler yields between segments so another request for the
same provider can make progress.

## CLI quick start

After signing into the portal, create a Satellite CLI API Key and configure the
thin client:

```bash
satellite config set
```

The client stores only the centre URL and CLI Key in
`~/.config/satellite/config.json` with mode `0600`.

### Browse products

```bash
satellite source list
satellite product list --search temperature --json
satellite product list --source earthcare --level L2
satellite product show --product goes18:ABI-L1b-RadF
satellite catalog list --source goes18
```

### Query published granules

```bash
satellite query list \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2026-09-22T09:00:00Z \
  --end 2026-09-22T10:00:00Z

satellite query list \
  --product goes18:ABI-L1b-RadF \
  --day 2024-01-01 \
  --band 6
```

Interactive searches are intentionally bounded. Larger historical acquisitions
should use persistent download orders instead of giant synchronous responses.

### Submit persistent downloads

```bash
satellite download submit \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2023-06-01 \
  --end 2023-06-30 \
  --band 6 \
  --hourly

satellite download status --id 1 --json
```

`download submit` uses inclusive UTC dates and supports repeatable `--time HH:MM`
or the `--hourly` shortcut. Band filtering is available only when the product has
a configured band-to-filename profile.

The CLI sends the request once and may disconnect immediately. Completion means
the centre has observed verified archive promotion — not merely that provider
discovery or an upstream job has finished.

## ERA5 retrieval

ERA5 and ERA5-Land are on-demand CDS datasets, so the project does **not** pretend
they have published granules like GOES or MODIS.

- Catalogue metadata describes dataset extent, not guaranteed per-hour file availability.
- Ordinary granule search/calendar indexing does not implicitly submit CDS jobs.
- A persistent unified download order is the preferred path for automatic retrieval.
- Low-level CDS job endpoints remain available for explicit administrator operations.

### Persistent ERA5 order

```bash
satellite download submit \
  --product era5:reanalysis-era5-single-levels \
  --start 2024-01-01 \
  --end 2024-01-02 \
  --variable 2m_temperature \
  --area 32.25,118.5,32,118.75
```

The centre derives request dates, splits hourly products by UTC day and monthly
products by complete month, persists CDS submission checkpoints and forwards
successful results to the downloader.

An uncertain CDS POST outcome is **never automatically repeated**. The operator
must reconcile the upstream request before resubmitting so duplicate jobs are not
silently created.

### Explicit CDS job operations

```bash
satellite era5 schema --collection era5:reanalysis-era5-single-levels

satellite era5 submit \
  --collection era5:reanalysis-era5-single-levels \
  --variable 2m_temperature \
  --product-type reanalysis \
  --year 2024 --month 01 --day 01 --time 00:00 \
  --area 32.25,-118.75,32,-118.5 \
  --data-format netcdf \
  --download-format unarchived

satellite era5 status --id JOB_ID
satellite era5 result --id JOB_ID
```

These low-level commands require an administrator-owned CLI Key and do not create
centre archive orders.

## HTTP API

The production portal is authenticated. `/login` is the web entry point; the same
HttpOnly session authorizes catalogue, calendar, monitoring and administrator
pages according to role.

Operational API calls accept either a portal session or a web-created Satellite
CLI API Key.

```text
GET /healthz
GET /api/v1/sources
GET /api/v1/catalog?source=goes18
GET /api/v1/search?source=goes18&collection=goes18:ABI-L1b-RadF&start=...&end=...
GET /api/v1/monitor/downloads?window=1h

GET  /api/v1/era5/schema?collection=era5:reanalysis-era5-single-levels
POST /api/v1/era5/requests
GET  /api/v1/era5/requests/{jobID}
GET  /api/v1/era5/requests/{jobID}/result
```

CLI Keys use the `satellite_v1_` namespace and are intentionally distinct from
NSMC Keys. The full Key is returned only once when created; the centre stores its
digest and rejects revoked Keys or Keys whose owner is disabled.

## Access and secret ownership

Provider credentials, accounts, proxy routes and proxy authentication belong to
the centre. Secret values are encrypted at rest and are write-only through the
protected administration interface.

The downloader receives only the minimum centre-mediated authorization needed to
fetch an allowlisted provider asset. It does not open the centre Vault, read a
master key or expose provider credentials.

The CLI:

- never accepts provider passwords or Tokens as ordinary command arguments;
- never stores the centre Vault key;
- never performs provider file transfer itself;
- never follows redirects when sending its centre Bearer authorization;
- cannot mint or manage provider credentials through a CLI Key.

See [`docs/central-access.md`](docs/central-access.md) for ownership, migration and
rollback details.

## Archive indexing and verification

The centre indexes both newly downloaded files and existing shared holdings.
Archive presence and official verification are deliberately different states.

### EarthCARE

- Shared archive root on c2ln1: `/misc/nuist_data1/public_sharing/EarthCare`.
- Supported historical layouts include `<product>/<YYYYMMDD>/<filename>` and
  `<product>/<YYYY>/<MM>/<filename>`.
- Existing files remain `unverified` until they can be matched to exact official
  metadata and hashed.
- A valid HDF5/NetCDF header is a format check, **not** proof of official identity.
- Inaccessible directories are reported rather than silently treated as empty.

Historical repair tooling and evidence rules are documented in
[`docs/earthcare-historical-verification.md`](docs/earthcare-historical-verification.md).

### ERA5

- Shared root: `/misc/nuist_data1/public_sharing/ERA5`.
- CDS results are archived under
  `CDS/<dataset>/<YYYY>/<MM>/<DD>/<job-id>/<result-name>`.
- Historical files without durable CDS job/result identity are not automatically
  promoted to verified status.

### Himawari

`SATELLITED_HIMAWARI_ARCHIVE` can index an existing shared Himawari archive.
Historical `.bz2` payloads receive format-level checks only unless exact provider
identity can be established.

### GOES

GOES archive scans derive product/day identities from NOAA filenames and can queue
missing official catalogue days. Per-day fingerprints survive restarts. Partial
band downloads remain partial coverage and are never presented as complete days.

New archive writes refuse to overwrite a different file with the same name.

## Calendar and monitoring model

The web UI keeps archive/calendar state live through incremental index updates.

- Fixed-cadence products preserve expected gaps rather than converting missing
  observations into fake zero values.
- Event products use actual timestamps rather than snapping every event onto a
  nominal slot.
- Daily band views distinguish complete, partial, missing and unverified coverage.
- Year calendars support leap years and remain horizontally scrollable on smaller screens.
- Download inputs/lists default to UTC; transfer charts and sample times can use BJT (UTC+8).

The centre samples downloader telemetry every 15 seconds and retains eight days of
history. Monitoring exposes provider-isolated throughput, active transfer workers,
adaptive limits and queue depth for `realtime`, `1h`, `3h`, `6h`, `12h`, `1d`,
`2d` and `1w` windows. Missing samples remain gaps instead of being rewritten as
zero-speed observations.

## State and configuration

The centre stores application state and index data in one SQLite database:

```text
~/.local/state/satellite-data/state.sqlite3
```

It is separate from:

- NSMC `index.sqlite3`;
- the downloader database;
- the access/credential database when an explicit production path is configured.

Important production settings include:

```text
SATELLITE_ACCESS_DB
SATELLITE_ACCESS_MASTER_KEY
SATELLITE_CENTRAL_SOCKET
SATELLITED_STATE_DB
SATELLITED_EARTHCARE_ARCHIVE
SATELLITED_ERA5_ARCHIVE
SATELLITED_HIMAWARI_ARCHIVE
SATDOWN_CENTRAL_SOCKET
SATDOWN_EARTHCARE_ARCHIVE_ROOT
SATDOWN_ERA5_ARCHIVE_ROOT
```

Unix sockets should live on node-local runtime storage, not a shared HOME/state
filesystem such as GPFS or NFS.

## Repository layout

```text
cmd/
  satellite/              thin CLI
  satellited/             API + embedded web daemon

internal/
  app/                    provider wiring
  authstore/              portal / CLI-key persistence
  central/                credentials, provider auth, private fetch gateway
  downloadcoord/          persistent download coordination
  downloadfilter/         product/band filtering profiles
  downloadmon/            downloader telemetry sampling
  httpapi/                authenticated HTTP API + SSE
  indexer/                shared/local archive indexing
  linkmon/                provider/link reachability
  model/                  source / collection / granule / asset model
  objectstore/            anonymous GCS + S3 listing
  provider/               provider-specific catalogue/discovery adapters
  slotaxis/               nominal/event timeline policies
  state/                  SQLite-backed state and scheduling
  transfer/               centre-side HTTP transfer helpers

web/                      embedded dependency-free portal UI
deploy/                   deployment units and archive maintenance tools
docs/                     focused design and operational notes
```

## Documentation

| Document | Purpose |
| --- | --- |
| [`docs/central-access.md`](docs/central-access.md) | Credential ownership, centre/downloader boundary, migration and rollback |
| [`docs/earthcare-historical-verification.md`](docs/earthcare-historical-verification.md) | EarthCARE historical archive verification and repair evidence |
| [`docs/product-availability-2026-09-27.md`](docs/product-availability-2026-09-27.md) | Provider/product availability snapshot |
| [`docs/product-catalog-2026-09-27.csv`](docs/product-catalog-2026-09-27.csv) | Product catalogue snapshot |
| [`docs/satellite-timeline.md`](docs/satellite-timeline.md) | Mission timeline semantics used by the UI |
| [`docs/era5-network-2026-09-27.md`](docs/era5-network-2026-09-27.md) | c2ln1 ERA5/CDS network snapshot |

## Development and verification policy

Project builds and tests run in GitHub Actions. Development machines and production
data nodes are used for source editing, configuration, deployment and post-deploy
health checks; they are not used as substitute build/test runners.

Linux release artifacts use `CGO_ENABLED=0` and `GOAMD64=v1` so the same verified
binary can run on older HPC userspace distributions without inheriting the build
runner's glibc requirement.

## Production service names

On the data node the user-level units are:

```text
satellited.service
satellite-data-downloader.service
```

Check exact unit names with:

```bash
systemctl --user list-unit-files '*satellite*'
```

There is no `satellite-downloader.service` unit.
