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

When a route's measured speed for a source is below 20% of the fastest
recently measured healthy route, the gateway limits that route to one active
transfer for the source. A faster route at its concurrency limit still counts
in the comparison. In-progress transfers keep their routes, and an idle slow
route can be measured again. Failed or stale routes do not set the benchmark.

`"explore_fast_route_capacity": true` lets a healthy primary route with at
least 80% of the best recent single-connection rate test an extra transfer
above its per-IP
limit. The test compares aggregate throughput in 30-second windows at each
limit. A gain of at least 10% keeps the extra slot temporarily. Insufficient
gain, a failed route, or a trial that cannot collect a full window returns
the original limit for new allocations. Routes sharing an IP keep their
normal shared limit. Backup proxies retain their original limits. The
downloader's worker caps remain in force.

The optional proxy protocol breakdown uses the dedicated Mihomo instance's
connection counters. Configure its controller with `external-controller-unix`
in a root-only directory on the router. Run `deploy/mihomo-egress-metrics.py`
with `--controller` pointing to that socket, `--listen` pointing to a different
socket in a private directory, and `--inbound-port` set to the proxy's mixed
port. Forward the collector socket over SSH to a private local socket, then set
`SATELLITE_PROXY_EGRESS_METRICS_SOCKET` on `satellited` to the local socket path.
The collector serves only `GET /snapshot` with aggregate counts and rates.
Protocol metrics cover the dedicated proxy's inbound connections across all
sources. Sampling can miss connections that close between snapshots, and
proxy connections may be reused, so those figures use a different counting
scope from the gateway's file transfer totals.

The `satellited` log records `download egress selected` once per acquired
transfer. Search by `source` and `route`. `event_at` is UTC. `reason`
distinguishes primary sampling, throughput-based selection, backup trials,
faster backups, and lack of available primary routes. The log includes the
per-connection speed estimates when samples exist, and the primary comparison
when a backup is selected over a measured primary. `download egress cooldown`
records its `scope`, `blocked_until` in UTC, and the HTTP status when an upstream
response triggered a source cooldown.

### Nodes that require an outbound proxy

Set `SATELLITE_USE_ENV_PROXY=1` and configure `HTTPS_PROXY`, `HTTP_PROXY` and
`NO_PROXY` for the control plane and the downloader on hosts without direct
internet access. This lets direct account routes and public checksum lookups
use the environment's outbound proxy. Account-specific proxies and explicit
download routes retain precedence.
