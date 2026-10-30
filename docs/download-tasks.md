# Download task time selection

A task contains 1–32 products. Submit it once; the coordinator queries each product and schedules its files automatically. Each product may override the shared dates, scan times and band. Validation is atomic, and equivalent submissions by the same owner reuse the existing task.

| Catalogue time type | Form | Query semantics |
| --- | --- | --- |
| Geostationary product with a nominal scan cadence | Dates plus all scans, compatible hourly/synoptic presets, or explicit times | Nominal scan starts, with the existing bounded phase tolerance |
| Polar swath / observation | Observation start and exclusive end, to the minute | Intersect the available official coverage interval; retain point events when only a start is available |
| Orbit product | Observation start and exclusive end | Whole files selected by official time metadata; no invented orbit-number filter |
| Daily | Inclusive date range, UTC | Files intersecting the chosen days |
| Weekly | Inclusive coverage date range, UTC | Official weekly coverage; no assumed ISO-week anchor |
| Multi-day composite | Inclusive coverage date range, UTC | Official coverage; no universal composite epoch or year-end truncation |
| Calendar dekad | Month plus upper/middle/lower dekad | Days 1–10, 11–20, 21–month end, UTC |
| Monthly / annual | Inclusive month / year range | Whole UTC calendar months / years |

The server returns `time_profile` in both the download catalogue and task summaries. A five-minute polar granule duration does not imply a recurring five-minute scan schedule. A rolling composite published daily remains a daily product. A ten-day interval is not assumed to be a calendar dekad.

NASA documents different composite coverage behavior, including intervals crossing the year boundary: [MODIS Atmosphere QA plan](https://atmosphere-imager.gsfc.nasa.gov/sites/default/files/ModAtmo/L3_ATBD_C6_2018_04_11.pdf), [MODIS Snow guide](https://landweb.modaps.eosdis.nasa.gov/data/userguide/MODIS-snow-user-guide-C6.pdf). Use the official granule start/end rather than reconstructing them from cadence alone. A file already covering the beginning of a request belongs to its first query chunk; subsequent chunks do not count it again.

`POST /api/v1/download-tasks` accepts per-product overrides keyed by the complete catalogue product key (including the catalogue identity of its version):

```json
{
  "products": ["goes18:ABI-L1b-RadF", "modis:collection-concept-id"],
  "start": "2024-02-01T00:00:00Z",
  "end": "2024-03-01T00:00:00Z",
  "options": {
    "goes18:ABI-L1b-RadF": {
      "start": "2024-02-29T00:00:00Z",
      "end": "2024-03-01T00:00:00Z",
      "band": 2,
      "times": ["00:00", "12:00"]
    },
    "modis:collection-concept-id": {
      "start": "2024-02-01T00:00:00Z",
      "end": "2024-03-01T00:00:00Z",
      "times": []
    }
  }
}
```

An explicit `band: 0`, `times: []`, or `scenes: {}` clears that shared selection for the product. Omitted fields inherit the top-level value. Period products reject recurring minute times and partial calendar periods. The browser reviews and submits an immutable per-product summary, and task history displays each product's actual range and version. Lists contain at most 30 tasks per page.
