# Satellite Data Downloader

Durable, provider-isolated transfer pipeline for the `nuist-fy-lab/satellite-data`
service.

The downloader deliberately does **not** own the public web UI or official
catalogue indexing. `satellite-data` discovers official granules and submits
idempotent asset tasks over a local Unix socket. This daemon persists those
tasks, downloads them, verifies them, atomically promotes them into the archive,
and exposes progress back to `satellite-data`.

## Pipeline

```text
satellite-data discovery
        |
        v
  Unix-socket API
        |
        v
 durable SQLite queue
        |
        +--> GOES transfer workers ------> GOES verify/promote workers
        +--> MODIS transfer workers -----> MODIS verify/promote workers
        +--> VIIRS transfer workers -----> VIIRS verify/promote workers
        +--> MTG transfer workers -------> MTG verify/promote workers
        +--> EarthCARE workers ----------> EarthCARE verify/promote workers
        +--> CALIPSO workers ------------> CALIPSO verify/promote workers
        +--> CloudSat workers -----------> CloudSat verify/promote workers
```

Every provider has its own worker pools and retry clock. A slow or broken
provider cannot consume another provider's transfer or verification slots.
Himawari-8/9 are intentionally disabled for now because the current c2ln1 ->
NOAA S3 path is unstable.

Network transfer and integrity verification are separate stages. A completed
network transfer is durably checkpointed as `verifying`, then the network worker
immediately takes another task while provider-local verification workers hash
and promote files.

## Authentication

Discovery may be public even when science-file transfer is not.

| Provider | Transfer authentication |
| --- | --- |
| GOES | anonymous |
| MODIS | LAADS download token |
| VIIRS | LAADS download token |
| MTG | EUMETSAT Consumer Key + Consumer Secret |
| EarthCARE | ESA MAAP bearer access token |
| CALIPSO | NASA Earthdata bearer token |
| CloudSat | CloudSat DPC THREDDS username + password |
| Himawari-8/9 | anonymous, currently disabled |

Credentials are read only by the downloader process. They are never accepted in
task JSON, stored in SQLite, or returned by status APIs.

## Configuration

General, non-secret runtime configuration and provider concurrency are separate
from credentials. See `deploy/systemd/satellite-data-downloader.env.example`.

The default Unix socket and state paths live under
`~/.local/state/satellite-data-downloader/`; the archive path must be configured
for the production data node.

