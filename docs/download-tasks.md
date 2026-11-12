# Download task time selection

A task contains 1–32 products. Submit it once; the coordinator queries each product and schedules its files automatically. Each product may override the shared dates, scan times and band. Validation is atomic, and equivalent submissions by the same owner reuse the existing task.

| Catalogue time type | Form | Query semantics |
| --- | --- | --- |
| Geostationary product with a verified scan policy | Dates plus all scans, compatible hourly/synoptic presets, or explicit times | Nominal scan starts with bounded phase tolerance; GOES full-disk ABI radiance/CMIP uses the actual file's mode 3/4/6 |
| Polar swath / observation | Observation start and exclusive end, with second precision | Intersect the available official coverage interval; retain point events when only a start is available |
| Orbit product | Data start and exclusive end | CloudSat orbit-file discovery uses file starts, except 2B-GEOPROF/2B-CLDCLASS when profile-centre metadata is available; the RMCP statistics collections have independent coverage intervals |
| Daily | Inclusive date range, UTC | Files intersecting the chosen days |
| Multi-day composite | Inclusive coverage date range, UTC | Official coverage; no universal composite epoch or year-end truncation |
| Monthly / annual | Inclusive month / year range | Whole UTC calendar months / years |

CloudSat orbit granules generally use filename start times. For 2B-GEOPROF and
2B-CLDCLASS the provider can resolve observation coverage through profile-centre
metadata when the metadata reader is configured. The 3F/3S-RMCP statistics
collections follow a separate path: the THREDDS provider parses month, season,
year and multi-month coverage directly from file names, then intersects those
intervals with the request. `coverage_basis=filename_period` identifies this
behavior. Official THREDDS listings include 2008 annual and 2008–2009 DJF
seasonal files. Metadata discovery does not establish authenticated file
download success.

IASI / Metop L1C spectra, principal-component products, combined sounding
products and the orbit-based climate records use the official EUMETSAT
granule start/end as orbit coverage. The FORLI-CO L2 collection is indexed as
individual timestamped retrievals rather than a recurring scan grid. The
noise-covariance collection is ancillary data; its product timestamp is
parsed from the product identifier because the upstream product-level date
range is intentionally broad.

The EUMETSAT portal may list additional IASI records beyond the Data Store
Browse/OpenSearch collection index. They are not advertised as downloadable
by this provider until an equivalent searchable and downloadable endpoint is
verified.

The server returns `time_profile` in both the download catalogue and task summaries. A five-minute polar granule duration does not imply a recurring five-minute scan schedule. A rolling composite published daily remains a daily product.

`kind` remains the scheduling contract. Additive `semantic`, `subtype` and
`semantic_label` fields describe scientific content independently, while
`window_minutes` represents a fixed scientific aggregation window only when
that window is independently established. `nominal_update_minutes` records an
approximate publication interval; `coverage_basis` records how actual file
coverage is obtained. These fields do not create a scan grid or assert that a
file covers its nominal publication interval. The UI separates three time
selection modes (fixed scans, actual time ranges, calendar periods) from
scientific time attributes and evidence. A scientific family such as cloud
retrieval is not treated as a new time selection mode.

In the download UI, `时间组织形式` distinguishes `扫描时次` (scanned
acquisitions), `时间覆盖区间` (temporal extent of a file or granule), and
`周期产品` (calendar-indexed products). The terminology follows the
granule temporal-extent concepts used by NASA Earthdata and STAC:
https://wiki.earthdata.nasa.gov/spaces/CMR/pages/382274521/Temporal+Extent+Granule
and https://docs.ogc.org/cs/25-004/25-004.html. A data granule with a
single timestamp may still be selected using a requested time interval;
this grouping describes the catalogue retrieval controls rather than
promising a continuous observation record.

`产品类别` is based on the scientific semantic family (observation,
retrieval/derived, event, accumulation, composite, statistics, reanalysis,
ancillary, etc.). It replaces the ambiguous former `时间属性` filter:
these are product families, not temporal-resolution measurements or CF
time cell methods. The corresponding detailed time characteristic remains
visible in the product metadata. The ten facets are presented as two
five-column rows on a sufficiently wide download panel, with responsive
reflow for smaller screens.

### Dated scan evidence and collection versions

The catalogue includes `version_alternatives` for separate official
collection IDs sharing the same source, satellite and data type. These
collections are independently selectable. Different collection versions
may have different processing algorithms, filenames, formats and production
periods; local archive date limits do not establish their complete official
availability ranges.

For 20 audited GOES-16 ABI collections, `observed_scan_modes` contains
file-backed NOAA GCS samples for 2018-06-15 (M3), 2020-06-15 (M6), and
2023-06-15 (M6). These are specifically dated observations rather than
inferred regime boundaries or confirmed mode-switch dates. Other products
omit this field until evidence is verified. The download coordinator
continues using the mode and timestamp of each actual file when matching
requested times; the UI shows these dated samples as additional context.
For collections with the actual-scenes picker, the available-times API also
exposes the returned file's validated `scan_mode` (M3, M4 or M6) per scene.
This describes the selected day's file, while the historical samples in
the catalogue are independent and are never substituted for the actual mode.

Explicit collection rules correct CALIPSO monthly L3 statistics, CloudSat
multi-period statistics, daily/hourly GOES flood composites and half-hour
GEO-LEO composites. GOES Flood-Hourly may be published hourly while a file
covers a growing portion of the day: a NOAA GOES-19 file published on
2025-08-25 records `time_coverage_start=09:30:21Z` and
`time_coverage_end=15:49:51Z`, with processing revision `v1r1`. Flood-Day
likewise indicates a daily product, not guaranteed 24-hour scene coverage.
The provider exposes each flood file's parsed processing revision as
`processing_revision`, separate from the catalogue version. Scientific labels
also distinguish EarthCARE calibration
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

The task list also includes ungrouped legacy requests, labeled as historical requests alongside grouped tasks. They retain their own request status and counts; visibility follows the same owner and administrator permissions as the API.

Selected ABI collections and `viirs:C3860041042-LAADS` also offer provider-listed granule groups. `GET /api/v1/downloads/available-times?product=<key>&date=YYYY-MM-DD` lists one UTC day under the same authentication as orders. Each entry contains the provider's `id`, `start`, and an `end` only when known. The per-product `granule_ids` option selects those IDs exactly and cannot be combined with `times` or `scenes`; it does not select individual assets within a provider group. The normal band filter still applies. IDs are sorted and deduplicated, with a maximum of 2,000 IDs and 1,024 bytes per ID. A listed group is not proof that its assets can be downloaded.

The verified derived ABI full-disk collections `ABI-L2-ACHAF` and `ABI-L2-ACMF` support this selection without changing their observation-range scheduling profile. NOAA documents the filename observation start and end in its [ABI filename guide](https://www.star.nesdis.noaa.gov/atmospheric-composition-training/satellite_data_decoding_data_file_names.php); listing availability is not a fixed cadence guarantee.

For Himawari-8/9 `AHI-L1b-Japan` and `AHI-L1b-Target`, add `time=HH:MM` to the availability request; the UTC time must align to a ten-minute boundary and the query covers exactly that ten-minute window. The returned interval is the union of the listed assets' HSD header observation times and does not depend on the selected band. The provider looks back one timeline for overlap; an unusually long scene from an earlier timeline is not guaranteed to be discovered. Header reads are bounded and may fail or time out on cold data; a failure never represents an empty or partial successful listing. This window is a query bound, not a recurring scan axis.

Interactive `/api/v1/search` and the CLI `query` command limit these four regional collections to at most ten minutes per request. Submit a persistent download task for longer ranges.
