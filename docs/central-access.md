# Central Access Ownership

`satellite-data` owns all provider credentials, accounts, proxies, expiry metadata,
authentication and the AES-GCM Vault. `satellite-data-downloader` owns only task
queues, transfers, verification and archive promotion. It no longer reads tokens
from environment variables or opens a master key.

The CLI only controls the centre: submitting download tasks and administrator-owned
ERA5 requests, querying catalogues and task status, and changing ordinary settings. It never handles provider
credentials/accounts, encryption, provider authentication or actual file
transfers. Centre-side discovery and download workers perform all downloads.
Provider proxy route settings belong to the centre too. A future CLI settings
API must expose only non-secret settings or references to centrally configured
proxies, with server-side authorization; proxy credentials are not CLI settings.

The CLI authenticates only with a Satellite API Key created on the web page.
`GET/POST/DELETE /api/v1/me/api-keys` are owner-web-session-only; mutations
also require `X-Satellite-Action: 1` and reject foreign origins. The full Key is
returned once with `Cache-Control: no-store`. Only its SHA-256 digest and public
ID are stored; list responses contain no Secret. Revocation and disabling an
owner take effect at the next request. Keys cannot read/mutate provider access
pools or manage other Keys, even with an accompanying web cookie. The CLI uses
`satellite config set` for hidden input and private `0600` configuration, sends
Bearer authorization only to the configured centre and never follows redirects.

Production reuses NSMC's `users` records for the portal password. Satellite owns
separate `satellite_api_keys` and `satellite_web_sessions` tables and distinct
token namespaces. NSMC Keys/sessions cannot be listed, authenticated or revoked
through Satellite, even by rewriting a token prefix. Existing NSMC sessions and
Keys are left untouched. Old Satellite access-page cookies must log in once at
the new portal entry; thereafter the credential page uses that same session.

ERA5 job submission and polling also accept Satellite CLI Keys belonging to an
enabled administrator. Nonadmin Keys remain read-only for ordinary operational
routes and cannot read CDS jobs; even admin Keys cannot manage credentials or
Keys. CDS provider credentials never leave the centre. A CLI result displays
asset metadata without printing the signed download URL.

The entire web portal is session-protected, not just the access page. The
`/login` page and login assets are public; unauthenticated pages redirect there
and data APIs return 401. Login is at `/api/v1/auth/login` (or `POST /login`),
session status at `/api/v1/auth/session`, and logout at `/api/v1/auth/logout`.
`/api/v1/access/*` uses that same portal session, requires the administrator
role and rejects cross-origin mutations; it does not ask for a second password.
Legacy access-login endpoints remain compatible aliases. Secret values are write-only.
The access page includes CDS credentials and ERA5 account binding.

Configuration (choose production paths explicitly):

- Centre: `SATELLITE_ACCESS_DB=/var/lib/satellite-data/access.sqlite3`
- Centre: `SATELLITE_ACCESS_MASTER_KEY=/etc/satellite-data/master.key`
- Both processes: centre `SATELLITE_CENTRAL_SOCKET` and downloader
  `SATDOWN_CENTRAL_SOCKET`, e.g. `/run/satellite-data/central.sock`

The socket is `0600`; both daemons must run as the same trusted service user.
When `XDG_RUNTIME_DIR` is available, both projects default their local IPC
sockets beneath that node-local directory. Do not place Unix sockets in a
shared HOME/state filesystem such as GPFS or NFS: another node can otherwise
unlink the pathname while the original process still holds the listener open.
On shared-HOME clusters, also pin the user services to the intended service
host. The c2ln1 deployment uses the drop-ins under
`deploy/systemd/c2ln1/` for that purpose.

The private fetch endpoint accepts only provider-allowlisted HTTPS asset URLs,
injects authentication centrally and streams the original status, Range, ETag
and content headers. No credentials or proxy passwords are returned to the
downloader. The centre must be reachable even for anonymous provider downloads.

## Coordinated Migration

Deploy only the matching, cloud-verified release artifacts. Do not rebuild on
the server. This change does not auto-migrate, auto-deploy or delete old data.

1. Keep both previous release binaries and service configurations for rollback.
2. Stop centre and downloader, and take local protected backups of the legacy
   SQLite database (including WAL state) and master key. Do not copy Secrets
   across machines or print them. Keep the original legacy database/key.
3. Set the centre paths above and import on the machine that already has the
   legacy files. The destination access database must be empty:

   ```sh
   /opt/satellite-data/satellited -migrate-access \
     -legacy-access-db "$HOME/.local/state/satellite-data-downloader/downloader.sqlite3" \
     -legacy-access-key "$HOME/.config/satellite-data-downloader/master.key"
   ```

   Only paths are arguments. The importer reads the source in read-only mode,
   decrypts in memory, re-encrypts under the centre key, preserves item IDs and
   commits all records atomically. A nonempty target or invalid payload aborts
   without overwriting anything. Counts, never Secret values, are reported.
4. Remove old token environment entries from active service configuration;
   credentials that only existed in environment files must be entered through
   the centre access page by their owner. Do not paste values into commands.
5. Start centre first, then downloader. Validate both systemd services,
   processes, sockets, health checks, access pools and one actual resumable
   transfer; inspect recent logs for new errors.

## Rollback

Stop both daemons, restore the previous binaries and service configuration,
then start the previous centre/downloader pair using the untouched legacy
database and key. Queue tables retain their existing layout. The new centre
access database/key should be retained separately, not copied into the old
downloader database. Do not remove either key until rollback is no longer needed.

## ERA5 Jobs

CDS submissions are explicitly initiated by an administrator; browsing does
not enqueue jobs. The centre persists each accepted job's account, credential
ID and credential fingerprint. After a restart it uses the same identity and
refuses to poll an unknown job or a job whose original Token has changed.

The unified `/api/v1/downloads` endpoint accepts ERA5 date ranges and data-only
inputs under the same administrator authorization. Its persistent order survives
CLI disconnects and centre restarts. The centre splits it into daily or monthly
CDS requests, saves a pre-POST checkpoint, polls accepted Job IDs and forwards
successful results to the downloader. A crash with an unknown POST outcome must
not cause an automatic resubmission. An order is complete only when its transfer
tasks have been verified and promoted, not when CDS merely generated results.

The downloader receives a stable `cds.climate.copernicus.eu` job reference.
On every transfer/resume the centre checks the persisted credential binding and
resolves the current result URL. CDS credentials are sent only to the API, never
to asset object storage. Unknown result hosts are rejected. Deploy matching
centre/downloader cloud artifacts together; the old downloader lacks ERA5 workers.
The new order tables are additive. A rollback retains them and all queued tasks;
the old centre cannot advance ERA5 orders until the matching new pair is restored.
