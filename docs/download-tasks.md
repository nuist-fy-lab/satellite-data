# Download task time selection

A task contains 1–32 products. Submit it once; the coordinator queries each product and schedules its files automatically. Each product may override the shared dates, scan times and band. Validation is atomic, and equivalent submissions by the same owner reuse the existing task.

| Catalogue time type | Form | Query semantics |
| --- | --- | --- |
| Geostationary product with a verified scan policy | Dates plus all scans, compatible hourly/synoptic presets, or explicit times | Nominal scan starts with bounded phase tolerance; GOES full-disk ABI radiance/CMIP uses the actual file's mode 3/4/6 |
| Polar swath / observation | Observation start and exclusive end, with second precision | Intersect the available official coverage interval; retain point events when only a start is available |
| Orbit product | Data start and exclusive end | Whole files selected by official time metadata; CloudSat explicitly selects file start because its catalogue does not provide file end times |
| Daily | Inclusive date range, UTC | Files intersecting the chosen days |
| Multi-day composite | Inclusive coverage date range, UTC | Official coverage; no universal composite epoch or year-end truncation |
| Monthly / annual | Inclusive month / year range | Whole UTC calendar months / years |

CloudSat's current provider discovers file starts only and returns no coverage
end. This also applies to the multi-period 3F/3S collections after their
scientific reclassification. Their profile retains `selection_basis=file_start`;
neither the statistics label nor mocked coordinator overlap tests establish
leading-overlap discovery. The official 3F/3S access route is SFTP and actual
retrieval through this provider remains unverified.

The server returns `time_profile` in both the download catalogue and task summaries. A five-minute polar granule duration does not imply a recurring five-minute scan schedule. A rolling composite published daily remains a daily product.

`kind` remains the scheduling contract. Additive `semantic`, `subtype` and
`semantic_label` fields describe scientific content independently, while
`window_minutes` records a documented aggregation duration without implying a
clock anchor or selectable scan grid. The UI's time-type filter describes the
actual selection: scan times, observation/event ranges, accumulation/composite
windows, data-validity times, or calendar periods. A scientific family such as
cloud retrieval is not presented as a new time mode.

Explicit collection rules correct CALIPSO monthly L3 statistics, CloudSat
multi-period statistics, daily/hourly GOES flood composites and half-hour
GEO-LEO composites. Scientific labels also distinguish EarthCARE calibration
and support data, motion winds, rainfall rate, lightning events and accumulated
fields. Unverified Himawari ISatSS collection identities retain a plain time
range; no missing-metadata chip or invented fixed cadence is shown.

The selected-product header exposes shared scan-time and band controls only for compatible instruments and time rules (including selection step and ownership basis) or identical band definitions (number, wavelength and resolution). Unified dates sit above the selected products; inherited dates appear in each product's summary, and individual date editors appear only after opting out of inheritance. Explicit scan times use a native time input and removable chips. Removing the final chip keeps the explicit selection empty and blocks submission rather than silently selecting every scan. Each product retains an explicit expand/collapse action. Missing and inapplicable metadata do not become placeholder chips. Only actual version numbers are offered; NASA CMR collections require a positive official granule count. A derived product's version 1.1 is independent of the MODIS standard-collection 6.1 sequence.

Shared dates represent civil dates in the chosen timezone. Exact per-product time overrides and selected scan times preserve their instants when switching UTC/BJT; calendar-period products remain UTC. Full-disk GOES permits the five-minute selection step shared by historical 15/5-minute and current 10-minute modes, then matches against the mode recorded in each returned file. This does not promise that every selected time has data. Unknown scan modes retain actual event minutes instead of being snapped to a guessed grid.

Each valid overridden bound and time selection converts independently, including
while another bound is incomplete or no custom time has been added yet. Invalid
input remains editable and is rejected at submission. Confirmation freezes the
per-product selection before the single scheduling request.

Open-source references and implementation choices: [downloader review](opensource-downloaders.md).

NASA documents different composite coverage behavior, including intervals crossing the year boundary: [MODIS Atmosphere QA plan](https://atmosphere-imager.gsfc.nasa.gov/sites/default/files/ModAtmo/L3_ATBD_C6_2018_04_11.pdf), [MODIS Snow guide](https://landweb.modaps.eosdis.nasa.gov/data/userguide/MODIS-snow-user-guide-C6.pdf). Use the official granule start/end rather than reconstructing them from cadence alone. When the provider discovers a file already covering the beginning of a request, it belongs to the first query chunk; subsequent chunks do not count it again. Provider-side discovery limitations still apply.

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

Selected ABI collections and `viirs:C3860041042-LAADS` also offer provider-listed granule groups. `GET /api/v1/downloads/available-times?product=<key>&date=YYYY-MM-DD` lists one UTC day under the same authentication as orders. Each entry contains the provider's `id`, `start`, and an `end` only when known. The per-product `granule_ids` option selects those IDs exactly and cannot be combined with `times` or `scenes`; it does not select individual assets within a provider group. The normal band filter still applies. IDs are sorted and deduplicated, with a maximum of 2,000 IDs and 1,024 bytes per ID. A listed group is not proof that its assets can be downloaded.

The verified derived ABI full-disk collections `ABI-L2-ACHAF` and `ABI-L2-ACMF` support this selection without changing their observation-range scheduling profile. NOAA documents the filename observation start and end in its [ABI filename guide](https://www.star.nesdis.noaa.gov/atmospheric-composition-training/satellite_data_decoding_data_file_names.php); listing availability is not a fixed cadence guarantee.
