# Download task time selection

A task contains 1–32 products. Submit it once; the coordinator queries each product and schedules its files automatically. Each product may override the shared dates, scan times and band. Validation is atomic, and equivalent submissions by the same owner reuse the existing task.

| Catalogue time type | Form | Query semantics |
| --- | --- | --- |
| Geostationary product with a verified scan policy | Dates plus all scans, compatible hourly/synoptic presets, or explicit times | Nominal scan starts with bounded phase tolerance; GOES full-disk ABI radiance/CMIP uses the actual file's mode 3/4/6 |
| Polar swath / observation | Observation start and exclusive end, to the minute | Intersect the available official coverage interval; retain point events when only a start is available |
| Orbit product | Data start and exclusive end | Whole files selected by official time metadata; CloudSat explicitly selects file start because its catalogue does not provide file end times |
| Daily | Inclusive date range, UTC | Files intersecting the chosen days |
| Multi-day composite | Inclusive coverage date range, UTC | Official coverage; no universal composite epoch or year-end truncation |
| Monthly / annual | Inclusive month / year range | Whole UTC calendar months / years |

The server returns `time_profile` in both the download catalogue and task summaries. A five-minute polar granule duration does not imply a recurring five-minute scan schedule. A rolling composite published daily remains a daily product.

The selected-product header exposes shared scan-time and band controls only for compatible instruments and time rules or identical band definitions (number, wavelength and resolution). Each product retains an explicit expand/collapse action. Missing and inapplicable metadata do not become placeholder chips. Only actual version numbers are offered; NASA CMR collections require a positive official granule count. A derived product's version 1.1 is independent of the MODIS standard-collection 6.1 sequence.

Shared dates represent civil dates in the chosen timezone. Exact per-product time overrides and selected scan times preserve their instants when switching UTC/BJT; calendar-period products remain UTC. Full-disk GOES permits the five-minute selection step shared by historical 15/5-minute and current 10-minute modes, then matches against the mode recorded in each returned file. This does not promise that every selected time has data. Unknown scan modes retain actual event minutes instead of being snapped to a guessed grid.

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
