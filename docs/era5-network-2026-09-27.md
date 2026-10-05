# c2ln1 to CDS network snapshot

Measured on c2ln1 through AgentDock at 2026-09-27 06:07 UTC. All requests
were read-only and unauthenticated; no retrieval jobs were submitted.
These are dated observations, not guarantees of future throughput.

| Target | DNS IPv4 | HTTP outcomes | TCP connect (cumulative) | TLS complete (cumulative) | Total |
| --- | --- | --- | --- | --- | --- |
| CDS single-levels STAC collection (7,664 bytes) | 136.156.139.54 | 3/3 HTTP 200 | 0.153-0.175 s | 0.310-0.351 s | 0.503-0.564 s |
| CDS single-levels process schema (15,195 bytes) | 136.156.139.54 | 3/3 HTTP 200 | 0.156-0.229 s | 0.318-0.450 s | 0.811-3.534 s |
| ECMWF CCI2 catalogue form (51,455 bytes) | 136.156.136.3 | 3/3 HTTP 200 | 0.160-0.173 s | 0.324-0.349 s | 0.806-0.885 s |
| ECMWF CCI2 catalogue constraints (2,233,070 bytes) | 136.156.136.3 | 3/3 HTTP 200 | 0.147-0.208 s | 0.297-0.423 s | 1.443-3.402 s |

DNS lookup in the individual curl requests took 2-10 ms. Subtract lookup
time from cumulative TCP connect to obtain approximate connection latency.
One CDS schema request had a 3.359 s first-byte time while the other two
were 0.654-0.668 s. API responsiveness therefore has observed variability.

At 06:25 UTC, a separate probe made 20 direct TCP connections to each resolved
IPv4 address on port 443 (3 s timeout, 75 ms pause between attempts):

| Target | Successful connections | Min / median / max connection time |
| --- | --- | --- |
| CDS API | 20/20 | 147.86 / 167.96 / 267.82 ms |
| ECMWF CCI2 object store | 20/20 | 139.85 / 182.96 / 278.37 ms |

No connection failures were observed in this small sample. These TCP results
do not measure ICMP packet loss or guarantee error-free long downloads.

The constraints object transferred at 656,399 / 1,074,107 / 1,547,519 B/s,
approximately 0.63-1.48 MiB/s (5.25-12.38 Mbps). **This is a small catalogue
metadata object, not an ERA5 GRIB/NetCDF data download benchmark.** The actual
retrieval cache host is only known once a real CDS request returns a result;
CCI2 metadata reachability does not prove that every result host is reachable.

The shell had no CDS credential environment values, and `~/.cdsapirc` was
absent. We did not inspect secret contents or configure any credential. A
real small-area request, queue-time measurement, result-host probe and sustained
data-speed/resume check still require an authorized CDS token and accepted
dataset licence. No production service was changed for this measurement.
