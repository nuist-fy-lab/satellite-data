# Satellite Data

**English** | [简体中文](README.zh-CN.md)

Unified multi-satellite scientific data discovery, ordering, archive indexing, and synchronization service for [`nuist-fy-lab`](https://github.com/nuist-fy-lab).

## Overview

`satellite-data` provides a central web portal, HTTP API, and thin CLI (`satellite`) for discovering, ordering, and indexing multi-source earth observation and reanalysis datasets.

The system deliberately separates **catalogue discovery & order management** from **bulk data transfer**:

- **`satellited` (this repository)**: Manages official product catalogues, availability calendars, local archive indexing, provider credentials/routes, and persistent acquisition orders.
- **[`satellite-data-downloader`](https://github.com/nuist-fy-lab/satellite-data-downloader)**: Consumes approved asset tasks over a local Unix socket and handles provider-isolated resumable downloads, checksum verification, staging, and atomic archive promotion.
- **`satellite` (thin CLI)**: Submits orders and queries catalogue or task status via Bearer API keys—never storing upstream provider credentials or executing transfers locally.

```text
Browser / satellite CLI
          │
          │ HTTPS (Session / CLI API Key)
          ▼
     satellited ◄───────────► SQLite (state & archive index)
          │
          │ Local Unix Sockets (tasks / routes / completion events)
          ▼
satellite-data-downloader ──► Verified Scientific Archive
```

## Supported Data Sources

All providers use official cloud object stores, STAC/CMR catalogues, or authenticated APIs—avoiding retired FTP endpoints and HTML scraping.

| Source | Products / Instruments | Discovery & Transfer Mechanism |
| :--- | :--- | :--- |
| **GOES-16 / 17 / 18 / 19** | ABI L1b / L2 | NOAA NODD object hierarchy (Google Cloud Storage primary) |
| **Himawari-8 / 9** | AHI Full-Disk / Target | NOAA NODD anonymous S3 |
| **MODIS (Terra / Aqua)** | Collection 6.1 / 7 L1B & L2 | LAADS API v2 catalogue + token-authenticated resumable HTTPS |
| **VIIRS (SNPP / NOAA-20 / 21)** | L1B & L2 science products | NASA CMR / LAADS + Earthdata Cloud HTTPS |
| **MTG (Meteosat-12)** | FCI | EUMETSAT Data Store OpenSearch + OAuth2 |
| **EarthCARE** | L1 / L2 orbits | ESA MAAP STAC + OIDC-protected HTTPS |
| **CALIPSO** | CALIOP L1 / L2 | NASA CMR + Earthdata Login ASDC HTTPS |
| **CloudSat** | 2B-GEOPROF / CPR | CIRA DPC THREDDS catalogue + authenticated HTTPServer |
| **ERA5 / ERA5-Land** | Hourly & monthly single/pressure levels | Copernicus CDS STAC catalogue + asynchronous subset retrieval |

## Key Capabilities

- **Multi-Product Batch Orders & Three-Layer Deduplication**: Group up to 32 products across a shared date window in a single order. Deduplication operates across identical user submissions, concurrent orders targeting the same upstream asset, and already-archived local files (reused only after verifying path, size, mtime, and SHA-256 against official records).
- **Automated Availability Calendar & Archive Indexing**: Tracks daily and sub-daily band/slot coverage across fixed-cadence geostationary grids and event-based overpass timelines. Background indexers incrementally reconcile local filesystem changes and upstream availability with adaptive per-provider rate pacing.
- **Centralized Credential & Route Governance**: Upstream tokens, OAuth2/OIDC credentials, CDS keys, and proxy routes are encrypted at rest inside `satellited` and managed exclusively by administrators.
- **Unified ERA5 On-Demand Retrieval**: Supports both persistent multi-day/multi-month archive orders (automatically split, polled, verified, and archived) and low-level interactive CDS job inspection.

## CLI Usage

### 1. Configuration

Sign in to the web portal, create a CLI API Key (`satellite_v1_...`), and configure the client:

```bash
satellite config set
```

The CLI stores only the server URL and API Key in `~/.config/satellite/config.json` (`0600`). You may also override them in automated environments via `SATELLITE_SERVER` and `SATELLITE_API_KEY` (or custom path `SATELLITE_CONFIG`).

### 2. Browsing Catalogues & Querying Availability

```bash
# List available sources and search products
satellite source list
satellite product list --search temperature --json
satellite product list --source earthcare --level L2
satellite product show --product goes18:ABI-L1b-RadF

# Query official granules for a single UTC day or an interval (up to 31 days)
satellite query list --product goes18:ABI-L1b-RadF --day 2024-01-01 --band 6
satellite query list \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2026-09-22T09:00:00Z \
  --end 2026-09-22T10:00:00Z
```

### 3. Submitting & Monitoring Download Orders

Orders are persistent and execute asynchronously on the server; the CLI can disconnect immediately after submission.

```bash
# Single-product order with band and hourly slot filters
satellite download submit \
  --product goes18:ABI-L1b-RadF \
  --start 2023-06-01 --end 2023-06-30 \
  --band 6 --hourly

# Multi-product batch order
satellite download submit \
  --product modis:C1443536017-LAADS \
  --product goes18:ABI-L1b-RadF \
  --start 2023-05-03 --end 2023-05-10

# Unified ERA5 subset order (automatically split by UTC day/month and archived)
satellite download submit \
  --product era5:reanalysis-era5-single-levels \
  --start 2024-01-01 --end 2024-01-02 \
  --variable 2m_temperature \
  --area 32.25,118.5,32,118.75

# Check order progress
satellite download status --id 1 --json
satellite download batch-status --id 1
```

> **Note**: Low-level CDS inspection commands (`satellite era5 schema|submit|status|result`) are also available for administrator keys when interacting with raw upstream CDS jobs without creating archive orders.

## HTTP API Overview

All portal pages and API endpoints require either an authenticated web session (`GET /login`) or a `Bearer` CLI API Key:

| Endpoint | Description |
| :--- | :--- |
| `GET /healthz` | Service health check |
| `GET /api/v1/sources` | List configured satellite and reanalysis providers |
| `GET /api/v1/catalog?source=<id>` | Browse stored official product catalogue |
| `GET /api/v1/search?...` | Interactive granule search (up to 7-day window) |
| `POST /api/v1/downloads` | Submit persistent single or multi-product acquisition order |
| `GET /api/v1/archive/index-status` | Local archive scan status, year coverage, and index queue state |
| `GET /api/v1/monitor/downloads?window=1h` | Provider-isolated transfer throughput and worker metrics (`realtime` to `1w`) |
| `GET /api/v1/era5/schema` | Official CDS parameter schema for ERA5 collections |

## Repository Layout

```text
cmd/
  satellite/             Thin user CLI
  satellited/            Central API and embedded web daemon
internal/
  app/                   Service orchestration and provider wiring
  httpapi/               REST & SSE endpoints
  model/                 Source / Collection / Granule / Asset domain models
  objectstore/           Anonymous GCS and S3 listing clients
  provider/              Source adapters (calipso, cloudsat, earthcare, era5, eumetsat, goes, himawari, laads)
  transfer/              HTTP transfer primitives
web/                     Embedded dependency-free web interface
deploy/                  Deployment scripts and archive maintenance utilities
docs/                    Architecture and operations documentation
```

## Deployment & Configuration

Builds and tests run in GitHub Actions (`CGO_ENABLED=0`, `GOAMD64=v1` static binaries). Production hosts run `satellited.service` alongside `satellite-data-downloader.service` as user-level `systemd` units.

Key environment variables for `satellited`:

| Variable | Purpose | Example / Default |
| :--- | :--- | :--- |
| `SATELLITED_STATE_DB` | Central state & index SQLite database | `~/.local/state/satellite-data/state.sqlite3` |
| `SATELLITE_CENTRAL_SOCKET` | Private IPC socket shared with downloader | `$XDG_RUNTIME_DIR/satellite-data/central.sock` |
| `SATELLITED_ARCHIVE_ROOT` | General satellite archive root | `/data/archive/satellite` |
| `SATELLITED_EARTHCARE_ARCHIVE` | Dedicated EarthCARE archive root | `/data/archive/EarthCare` |
| `SATELLITED_ERA5_ARCHIVE` | Dedicated ERA5 archive root | `/data/archive/ERA5` |
| `SATELLITED_HIMAWARI_ARCHIVE` | Historical Himawari archive root | `/data/archive/Himawari` |
