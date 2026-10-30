# Download order audit — 2026-10-30

Three Paseo GPT-6.1 Terra reviewers covered catalogue semantics, backend combinations and frontend interactions. All submissions used temporary stores or mocked APIs; no production orders or CDS jobs were created.

| Check | Result / implemented correction |
| --- | --- |
| Catalogue-wide submission | 808 historical fixture rows across 13 source IDs: all 802 archive products accept a one-year interval; six ERA5 datasets reject missing generation inputs. This tests metadata/submission compatibility, not live availability. |
| Live MODIS versions | NASA CMR currently lists 70 LAADS/MODIS collections, of which 10 version-6 collections have zero granules. Only the 60 collections with positive counts are offered. Independently versioned derived products retain their real 1/1.1/2 versions. VIIRS uses the same existence rule. |
| Mixed tasks | Single/multiple source products, separate periods/bands/times, explicit resets, unknown keys, 32/33-product limit, invalid one-of-many atomic rejection. |
| Time edges | Leap day, month/dekad/year end, 7/8/10/16-day coverage, half-open bounds, invalid RFC3339 offsets, minute scenes, recurring times, exact UTC/BJT round trips. |
| GOES historical modes | Mode 3/4/6 full disks use 15/5/10-minute axes from file metadata. ABI CMIP joins the verified scan policies; MAG cannot inherit ABI area/cadence from its name suffix. Mocked coordinator verifies all three modes in one order. |
| Leading overlap | GOES looks into prior folders and retains files whose real coverage intersects the left boundary; exclusive ends and duplicate objects are tested. Composite overlap is scheduled only in its first chunk. |
| Privacy and duplicates | Separate owners, reordered members, normalized times, equivalent offsets, resubmission and distinct selections. |
| Shared controls | Same instrument and matching scan rules for time; identical wavelength/resolution/channel definitions for bands. Incompatible selections disable controls and cannot mutate independent settings. |
| Clear presentation | Unknown/inapplicable chips omitted; real resolutions replace “随波段”; real versions replace file-level placeholders; visible names searchable; explicit product expand/collapse; collapsed task is one 40px row. |
| Confirmation | Immutable reviewed payload, cancellation, simultaneous confirmation and retry after failure. |

Local verification passed for all 83 web tests and the affected API/coordinator/product-time/slot-axis/provider suites. CI additionally runs the audit web suite and real Firefox checks at 320, 390, 1440, 1920 and 2560px, including task height, product expanders, shared controls and summary interaction. See the CI run attached to the implementation PR for the final browser result.

Remaining limits are explicit: the historical fixture does not establish today's downloadability, authenticated access or every official cadence. CloudSat lacks official file end times, so it selects file starts. GOES prior-folder lookup is bounded (one hour for ABI L1b/GLM, 24 hours for other products); it cannot certify arbitrarily long overlapping files. Other ABI L2 and future sub-minute scan services need product-specific policy evidence before offering a fixed grid. Legacy endpoints retain their older coverage-interval contract.

Evidence: [NASA CMR granule-existence parameters](https://cmr.earthdata.nasa.gov/search/site/docs/search/api.html), [NOAA ABI historical scan modes](https://www.ospo.noaa.gov/operations/goes/product-quality-overview/ps-pvr/goes-16/ABI/Cloud%20and%20Moisture%20Imagery/Full/GOES-16_ABI-L1b-CMI_Full-Validation_ProductPerformanceGuide_v2.pdf), [NOAA CMIP specification](https://www.star.nesdis.noaa.gov/goesr/documents/ATBDs/Enterprise/ATBD_Enterprise_Cloud_and_Moisture_Imagery_Product_v5_2024-03-18.pdf).
