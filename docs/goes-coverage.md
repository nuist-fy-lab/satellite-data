# GOES discovery coverage

The provider normally lists GCS objects under product/year/day-of-year/hour
prefixes and filters discovered filenames using their actual start and end times. The NOAA Level 2+
product guide defines those filename fields as observation period start and
end: https://www.ospo.noaa.gov/resources/documents/PUG/GS%20Series%20416-R-PUG-L2%20Plus-0349%20Vol%205%20v3.0%20final.pdf
The standard open-data archive layout is described at
https://github.com/awslabs/open-data-docs/blob/main/docs/noaa/noaa-goes16/README.md.

GOES-19 public GCS listings on 2026-10-31 showed a different layout for
`ABI-Flood-Day`, `ABI-Flood-Hourly` and their `-TIF` and `-Shapefiles`
collections: product/year/month/day, with calendar-date timestamps in object
names. The provider lists those six collections by calendar day and treats
each file as a distinct composite. For example, hourly AOI002 files starting
2025-08-25 11:40:21 UTC have different end times. These samples do not prove
the maximum duration or availability of every flood object.

The same date's small GOES-18 listing samples for 2026/001/00 showed ABI
L1b RadF files of about 9.5 minutes, GLM LCFA files of 20 seconds, and MAG
GEOF files of about one minute. These observations are not archive-wide
duration bounds or evidence that longer files exist.

Discovery currently starts one hour before the request for ABI L1b and GLM,
and 24 hours before it for other products, rounded down to the listed hour or
day. This is a bounded fallback, not a documented maximum file duration. A
file whose directory is earlier than the first listed prefix can overlap the
request and be missed. Listing all pages does not address that limitation.
Missing or invalid end times cannot establish overlap before the request.

Complete arbitrary-duration overlap discovery without scanning all history
requires an authoritative per-product duration bound or an interval index
covering the archive and synchronized with updates. Neither is currently
provided by this implementation. Do not infer a bound from cadence or a few
observed files.
