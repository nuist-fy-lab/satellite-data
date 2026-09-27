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
Submission and job/result access require an administrator session. CDS jobs
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
file size and HTTPS location. The result link downloads directly from CDS;
**automatic transfer/resume/verification/archive promotion through the separate
downloader is not implemented for ERA5**. ERA5 therefore advertises `catalog`
and `retrieval`, not `search`, `download` or `subscription`.

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
prints metadata without exposing the signed result URL. The CLI does not
transfer or archive CDS files; the web portal has the direct result link.

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
