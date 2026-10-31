# Himawari regional scenes

JMA's [Himawari Standard Data guide](https://www.data.jma.go.jp/mscweb/en/himawari89/space_segment/hsd_sample/HS_D_users_guide_en_v13.pdf)
defines the filename date and minute as the ten-minute observation timeline,
and `JP01`-`JP04` and `R301`-`R304` as Japan and Target observation numbers
within that timeline. Its header description separately specifies observation
start and end times. [JMA's observation schedule](https://www.jma.go.jp/jma/jma-eng/satellite/introduction/satobs.html)
describes a 2.5-minute regional observation cycle, not an exact timestamp for
each object obtainable from the filename.

Read-only NOAA S3 listings for Himawari-9 on 2024-01-01 at 00:00 UTC showed
all four JP and R3 observation codes in the same timeline directory. The
provider groups bands and segments by timeline and observation code, with a
stable distinct ID for each code. Regional search reads the basic HSD header
from each listed asset and returns the earliest start and latest end in the
group. Metadata identifies the observation code, the timeline anchor and raw
MJD endpoints. The application treats the resulting union as `[start,end)`;
this is an application convention, not a claim that JMA specifies an
exclusive end. Before using a header, the provider checks its embedded HSD
filename, satellite, observation code and timeline against the object name;
the S3 `.bz2` wrapper is not part of the embedded `.DAT` filename. Identity
mismatches fail the query and are not cached. Select observations by full
granule ID, not timestamp alone.

This metadata extraction closes each stream after obtaining the complete
basic header block. It validates the HTTP Range response and, when an ETag
was listed, the object ETag, as well as the header identity described above.
It does not consume the complete bzip2 block or file, so it does not establish
the entire bzip2 block CRC or a full-file checksum. Full science-file integrity
remains the responsibility of the existing download verification pipeline.

Discovery begins one timeline before the ten-minute timeline containing the
query start. This finds overlap from those two timelines, but cannot prove
discovery of a file belonging to an even earlier timeline that extends across
the query start. Header errors and budget exhaustion fail the query instead of returning
fabricated precision. Limits are 256 objects and 128 MiB of compressed data
per query, 2 MiB per object, and four concurrent header reads. Successfully
read headers with ETags are cached for 24 hours, with at most 32768 entries.
Large ranges may exceed these bounds and require smaller requests.
The download coordinator splits these two collections at UTC ten-minute
boundaries; for dated scene selections it queries only windows containing a
selected time. Other collections retain their existing chunk behavior.

## Header samples

On 2026-10-31, byte ranges of NOAA S3 Himawari-9 objects were saved under
`/private/tmp/satellite-metadata/himawari`. These hashes cover the downloaded
compressed prefixes, not the full objects, except where marked full. The
filenames below are under `AHI-L1b-Japan/2024/01/01/` or
`AHI-L1b-Target/2024/01/01/` respectively. The block 1 start/end values are
little-endian IEEE 754 MJD days, converted from 1858-11-17 UTC; the guide
identifies these as observation start/end, but does not specify inclusive or
exclusive end. UTC values below are rounded to microseconds.

| Object suffix | Bytes, scope | SHA256 | Start MJD / UTC | End MJD / UTC |
|---|---|---|---|---|
| `0000/HS_H09_20240101_0000_B01_JP01_R10_S0101.DAT.bz2` | 65536, prefix | `d56bd424a0b37315e0c660f8960504ca003d06ed2ddd5cbc649538422a5ce336` | 60310.00015653425 / 00:00:13.524560 | 60310.001271287634 / 00:01:49.839252 |
| `0000/HS_H09_20240101_0000_B02_JP01_R10_S0101.DAT.bz2` | 262144, prefix | `d6107b79338f427b1ca838532d651ecb80f19f29f18a65cd23188eca28e3aed4` | 60310.000159423784 / 00:00:13.774215 | 60310.001274177135 / 00:01:50.088904 |
| `0000/HS_H09_20240101_0000_B01_JP02_R10_S0101.DAT.bz2` | 65536, prefix | `b1616ca3e7664da5f0e3302328edb099ffc26b5ce00e1706e20a962e170d2825` | 60310.00194207135 / 00:02:47.794965 | 60310.00294688791 / 00:04:14.611115 |
| `0010/HS_H09_20240101_0010_B01_JP01_R10_S0101.DAT.bz2` | 65536, prefix | `ae2db7e11a7d801b1203ee383d4f89b05743ec9319a3ead838519b229f827084` | 60310.00710087042 / 00:10:13.515204 | 60310.00821562392 / 00:11:49.829907 |
| `0000/HS_H09_20240101_0000_B01_R301_R10_S0101.DAT.bz2` | 911466, full | `36dc2b1902a24ed65c75e93d263bbd01c1c698d7c9dbf2d3cb8578e7e9538a26` | 60310.00158370781 / 00:02:16.832355 | 60310.00162331776 / 00:02:20.254654 |

The Target object's first 262144 compressed bytes did not yield the first
bzip2 block. Japan JP01 B01 and B02 differ by roughly 0.25 seconds in both
header endpoints; one representative band does not establish the union of all
assets in a granule. These samples do not establish a maximum scan duration,
timeline containment, or archive-wide rules.

A 2026-10-31 cold-read experiment for all 64 Japan objects in the 0000
timeline, using four concurrent ranged GETs and 16 KiB reads until each
basic header was available, consumed 8,323,072 compressed bytes in 30.39
seconds, excluding the listing request. The largest individual compressed
prefix read was 229,376 bytes. This is one network measurement, not an
upper bound; it already exceeds a 30-second synchronous UI budget.

After enabling the prior-timeline lookup, an actual cold provider query for
Japan `[2024-01-01T00:00Z,00:10Z)` on 2026-10-31 issued 2 list requests and
128 object requests, consumed 15,680,620 compressed bytes, returned 4 groups,
and took 54.70 seconds. These are observed counts on that network, not limits.

For `AHI-L2-FLDK-ISatSS`, the archive directory minute is retained as the
search anchor and the end remains unknown. A nominal ten-minute periodicity
is not used as the file's observation end.
