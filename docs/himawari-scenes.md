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
stable distinct ID for each code. Its `Start` is the timeline anchor, not the
actual observation start; `End` is the zero time (unknown), and `time_precision=timeline` and
`observation_code` identify the interpretation. Exact start and end cannot be
derived from listing metadata alone; no science file is read by search.

Regional L1b queries retain the existing half-open timeline-anchor filter:
only anchors in `[start,end)` are returned. Thus a query beginning inside a
timeline does not return that timeline's observations, even though their
actual observation times are unknown and may be later than the query start.
Moreover, returning an earlier anchor with unknown `End` would not fix this
end to end: the download coordinator's `OwnsCoverage` rejects it when the
anchor precedes the request start. Resolving actual-time boundaries requires
verified observation-time metadata and coordination beyond this provider.
Select a particular observation by its full granule ID, not by timestamp
alone. Other collections retain their existing search behavior.
