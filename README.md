# Satellite Data Downloader

**English** | [简体中文](README.zh-CN.md)

Durable, provider-isolated transfer, verification, and archive-promotion service for [`nuist-fy-lab/satellite-data`](https://github.com/nuist-fy-lab/satellite-data).

## Overview

`satellite-data-downloader` is the dedicated data-plane daemon paired with `satellite-data`. While `satellite-data` manages catalogues, orders, credentials, and user-facing APIs, this service focuses exclusively on turning approved asset tasks into verified archive files:

- Persists download tasks in SQLite before transfer begins;
- Isolates transfer and verification worker pools per provider;
- Supports HTTP resumable transfers (`Range` / `If-Range` / `ETag`) and recycles abnormally slow connections without losing partial progress;
- Decouples network transfer from file integrity verification;
- Verifies local SHA-256 alongside authoritative upstream checksums (MD5, CRC32C, SHA-256) prior to atomic archive promotion;
- Refuses to overwrite existing files with different content and delivers durable completion events back to the centre.

```text
 satellite-data (discovery + orders + credentials)
          │
          │ Private Unix Sockets
          ▼
 ┌─────────────────────────────┐
 │ satellite-data-downloader   │
 │                             │
 │ Durable SQLite Task Queue   │
 │          │                  │
 │          ▼                  │
 │ Provider Transfer Workers   │
 │          │                  │
 │          ▼                  │
 │ Provider Verify Workers     │
 │          │                  │
 │          ▼                  │
 │ Atomic Archive Promotion    │
 └──────────┬──────────────────┘
            │
            ├──► Verified Archive File
            └──► Durable Completion Event ──► satellite-data
```

## Provider Isolation & Adaptive Pacing

Each provider operates with its own transfer pool, verification pool, adaptive concurrency, and retry backoff clock so a slow or throttled upstream source never blocks other providers.

| Provider | Initial / Max Transfers | Verify Workers | Authoritative Upstream Checksum |Centre Auth Reference |
| :--- | :---: | :---: | :--- | :--- |
| **GOES** | 4 / 8 | 2 | MD5 + CRC32C (GCS metadata) | anonymous / centre route |
| **Himawari-8** | 1 / 1 | 1 | Local SHA-256 | anonymous / centre route |
| **Himawari-9** | 1 / 1 | 1 | Full-object S3 checksum | anonymous / centre route |
| **MODIS** | 4 / 8 | 2 | MD5 (NASA CMR UMM-G) | `laads` |
| **VIIRS** | 4 / 4 | 2 | MD5 (NASA CMR UMM-G) | `laads` |
| **MTG** | 2 / 2 | 1 | MD5 (EUMETSAT Browse) | `eumetsat` |
| **EarthCARE** | 2 / 2 | 1 | Local SHA-256 | `earthcare` |
| **CALIPSO** | 3 / 3 | 2 | MD5 (NASA CMR UMM-G) | `earthdata` |
| **CloudSat** | 2 / 2 | 2 | Local SHA-256 | `cloudsat` |
| **ERA5** | 2 / 2 | 2 | Local SHA-256 | `cds` |

### Congestion Control & Retries

- **Adaptive Pacing**: Providers start with a 200 ms launch interval. Timeouts or HTTP `408`/`429`/`5xx` responses scale down concurrency and widen the interval up to 5 s; sustained success restores concurrency and narrows the interval down to 100 ms.
- **Jittered Exponential Backoff**: Transient failures retry with equal-jitter exponential backoff (5 s base up to 10 min cap) while respecting upstream `Retry-After` headers (up to 1 hour). HTTP `404`/`410` responses are verified up to 6 attempts before failing permanently.
- **Slow-Transfer Recycling**: Evaluates moving-window throughput after a 30 s grace period and recycles stalled connections while preserving validated `.part` files for seamless range resumption.

## Access & Security Model

All upstream credentials, accounts, and proxy policies reside in the `satellite-data` centre. The downloader holds no secrets or credential fallback variables; before each transfer it requests an authorized route over `SATDOWN_CENTRAL_SOCKET`. If no route is currently available, tasks wait in `waiting_auth` and resume automatically once the centre restores access.

## Task Lifecycle & Archive Integrity

```text
queued ──► downloading ──► verifying ──► complete ──► archive-event outbox
               │               │
               ├─► retry       ├─► redownload (on checksum mismatch)
               └─► waiting_auth└─► verification retry (on path collision)
```

1. **Staging & Single-Pass Hashing**: Payloads land in `SATDOWN_DOWNLOAD_ROOT` first. Verification workers compute local SHA-256 and any applicable upstream digests in a single file pass without occupying transfer workers.
2. **Atomic Promotion**: Verified files are promoted atomically into the target archive root. Pre-existing verified local files matching official identity and SHA-256 can also be adopted across orders without repeating network transfer.
3. **Historical Checksum Sidecar**: An optional `satellite-checksum-sidecar` daemon audits historical completed archives in read-only mode at low I/O priority (`8 MiB/s` cap) and records official checksum evidence without interfering with live downloads.

## Configuration & Deployment

See [`deploy/systemd/satellite-data-downloader.env.example`](deploy/systemd/satellite-data-downloader.env.example) for a complete template. Keep Unix sockets on node-local runtime storage (`$XDG_RUNTIME_DIR`) rather than shared network filesystems (NFS/GPFS).

### Core Environment Variables

| Variable | Purpose | Default / Example |
| :--- | :--- | :--- |
| `SATDOWN_SOCKET` | Downloader task API Unix socket | `$XDG_RUNTIME_DIR/satellite-data-downloader/downloader.sock` |
| `SATDOWN_DB` | Durable SQLite queue database | `~/.local/state/satellite-data-downloader/downloader.sqlite3` |
| `SATDOWN_DOWNLOAD_ROOT` | Temporary staging directory | `~/.local/state/satellite-data-downloader/delivery` |
| `SATDOWN_ARCHIVE_ROOT` | Default satellite archive root | `/data/archive/satellite` |
| `SATDOWN_<PROVIDER>_ARCHIVE_ROOT` | Provider-specific archive root override | `/data/archive/EarthCare`, `/data/archive/ERA5` |
| `SATDOWN_CENTRAL_SOCKET` | Centre route/authorization socket | `$XDG_RUNTIME_DIR/satellite-data/central.sock` |
| `SATDOWN_ARCHIVE_EVENT_SOCKET` | Durable completion-event socket | `$XDG_RUNTIME_DIR/satellite-data/archive-events.sock` |

Per-provider concurrency and timeout overrides follow `SATDOWN_<PROVIDER>_{ENABLED,INITIAL_TRANSFER_CONCURRENCY,TRANSFER_CONCURRENCY,VERIFY_CONCURRENCY,REQUEST_TIMEOUT}`.

### Running with systemd

```bash
systemctl --user status satellite-data-downloader.service
journalctl --user -u satellite-data-downloader.service -n 100 --no-pager
```

## Repository Layout

```text
cmd/
  satellite-data-downloader/   Main downloader daemon
  satellite-checksum-sidecar/  Low-impact historical checksum auditor
internal/
  access/                      Centre route & authorization client
  archiveevent/                Durable completion event outbox
  config/                      Non-secret runtime configuration loader
  model/                       Task and lifecycle state models
  pipeline/                    Provider worker pools, pacing, and retry logic
  server/                      Private Unix socket task API
  store/                       SQLite persistence and crash recovery
  transfer/                    Resumable HTTP client, verifier, and archive promoter
deploy/systemd/                User systemd units and environment templates
```
