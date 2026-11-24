# Six SSH SOCKS5 download routes

The satellite centre can distribute authenticated HTTPS asset transfers over
six local SOCKS5 listeners carried by independent SSH connections. Configure
`SATELLITE_DOWNLOAD_EGRESS_FILE` in **both** `satellited` and
`satellite-data-downloader`. Use one private JSON file with the structure in
`deploy/systemd/satellite-download-egress.ssh.json.example` and substitute
the **observed** public IPv4 address of every WAN. The example uses RFC 5737
documentation addresses and is not a production routing file.

The egress pool applies only to file-body fetches at the central gateway;
catalogue discovery, account authentication, and CDS job control continue to
use the provider access profiles. Each file keeps one selected route for its
entire transfer, including Range retries in the download worker.

The downloader computes its source-specific worker ceiling and starting
concurrency from the existing provider settings multiplied by the number of
**unique public IPs**. The centre independently enforces each source's
`per_ip_limits`, sharing slots when multiple SSH tunnels have the same NAT
address. File verification concurrency and scheduler rates do not change.
The cap remains bounded to 128 workers per source and the centre's per-IP
defaults, even if the supplied configuration requests a larger limit.

When a route fails to dial, it is excluded from new allocations for 30 seconds.
The failed file is retried normally by the downloader and may choose a different
route on the next attempt. There is no implicit direct fallback if all SSH
routes fail. The centre exposes the active route counts and cooldown flags at
`GET /v1/egress/status` on its private Unix socket.

When a WAN MAC, private source IP or public NAT address changes, update the
SSH BindAddress and the shared egress file, verify the outbound IP, and restart
both services so per-IP allocation stays correct. Old route configurations and
binaries should remain available for rollback. Removing the environment
variable from both services restores the prior account-controlled download
routing and the original worker concurrency.

An optional route with `"backup": true, "sources": ["modis", "viirs"]`
can serve each listed download provider. It uses the existing worker budget.
Configure its observed exit IP so routes sharing that address share each
provider's per-IP quota. The pool periodically measures per-connection file
throughput. A backup that is at least 20% faster can take new transfers, and
the primary is retried as conditions change. Provider rejections cool down
that provider on the backup route. Connection failures cool down the route.

The download egress page shows connections and both rates by route group
and provider. Live rates use recent byte counts sampled every five seconds.
The smoothed rate is an exponential moving average with a 30-second time
constant. Each transfer counts once even if multiple routes share an IP.

### Nodes that require an outbound proxy

Set `SATELLITE_USE_ENV_PROXY=1` and configure `HTTPS_PROXY`, `HTTP_PROXY` and
`NO_PROXY` for the control plane and the downloader on hosts without direct
internet access. This lets direct account routes and public checksum lookups
use the environment's outbound proxy. Account-specific proxies and explicit
download routes retain precedence.
