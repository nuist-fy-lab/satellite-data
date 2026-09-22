# Satellite Data

Unified multi-satellite scientific data discovery, download, archive and synchronization service for `nuist-fy-lab`.

The project deliberately separates provider-specific discovery/authentication from the shared `Granule -> Asset -> transfer -> verify -> archive` pipeline. The CLI is a thin control client; persistent downloads will run in `satellited` on the data node.

## Current milestone

The first working slice implements anonymous public-cloud discovery for:

| Source | Discovery / transfer | State |
| --- | --- | --- |
| GOES-16/17/18/19 | NOAA NODD object hierarchy, Google Cloud Storage primary | implemented |
| Himawari-8/9 | NOAA NODD anonymous S3 | implemented |
| MTG / Meteosat-12 FCI | EUMETSAT Data Store OpenSearch + OAuth2 download; MQTT publication subscription / EUMETCast Terrestrial for low latency | discovery implemented; authenticated transfer next |
| MODIS Terra/Aqua | LAADS API v2 product catalog + `content/details`; LAADS Download Token + resumable HTTPS | implemented |
| EarthCARE | ESA MAAP STAC + authenticated HTTPS/object assets | next |
| CALIPSO | NASA CMR + Earthdata Login authenticated HTTPS | next |
| CloudSat | CIRA DPC THREDDS catalog + HTTPServer/OPeNDAP with DPC credentials | next |

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
    goes/
    himawari/
  transfer/              resumable HTTP transfer core
web/                      embedded dependency-free UI
```

## API

```text
GET /healthz
GET /api/v1/sources
GET /api/v1/catalog?source=goes18
GET /api/v1/search?source=goes18&collection=goes18:ABI-L1b-RadF&start=2026-09-22T09:00:00Z&end=2026-09-22T10:00:00Z
```

Interactive searches are intentionally limited to seven days. Larger historical requests will be represented as persistent backfill jobs rather than huge synchronous API responses.

## CLI

```text
satellite source list
satellite catalog list --source goes18
satellite query list \
  --source goes18 \
  --collection goes18:ABI-L1b-RadF \
  --start 2026-09-22T09:00:00Z \
  --end 2026-09-22T10:00:00Z
```

`SATELLITE_SERVER` selects the API endpoint. Provider credentials are never stored in the CLI.

## Verification policy

Project builds and tests run only in GitHub Actions. Development machines and production data nodes are used for source editing, configuration, deployment and post-deployment health checks, not project builds/tests.
