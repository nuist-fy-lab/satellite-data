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
