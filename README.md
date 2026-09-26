# Satellite Data

Unified multi-satellite scientific data discovery, download, archive and synchronization service for `nuist-fy-lab`.

The project deliberately separates provider-specific discovery from transfer.
The CLI is a thin control client. `satellited` owns official catalogues,
calendar/index state and provider-isolated discovery queues; the separate
`satellite-data-downloader` service owns resumable transfer, verification,
staging and archive promotion.

The two services communicate over a local Unix socket. Download discovery is
chunked and pipelined: each provider has its own discovery loop, assets are
submitted to the downloader as soon as each chunk is discovered, and every
downloader provider has independent transfer and verification worker pools.
A slow provider therefore cannot consume another provider's slots.
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
    eumetsat/
    goes/
    himawari/
    laads/
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
satellite download submit \
  --source goes18 --collection goes18:ABI-L1b-RadF \
  --start 2023-06-01 --end 2023-06-30 --band 6 --hourly
satellite download status --id 1 --json
```

`SATELLITE_SERVER` selects the API endpoint. Provider credentials are never stored in the CLI.
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
