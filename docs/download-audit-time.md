# Download time audit

This is a local audit of task submission and time boundaries, not evidence of
current upstream availability. The HTTP matrix uses synthetic metadata and a
temporary SQLite store; it does not submit or download upstream data.

## Findings

The points below record the initial investigation. Final implementation fixes and verification are tracked in [the consolidated checklist](download-audit.md): both recurring times and scenes now validate the allowed selection step; full-disk GOES uses its actual file mode; invalid timezone offsets reject. Sparse observation/orbit scene manifests remain supported for existing research selections.

- **Medium: off-grid scene selections are accepted.** For
  `goes18:ABI-L1b-RadF`, a task for 2024-01-01 through the exclusive end
  2024-01-02 rejects recurring `times:["00:03"]` but accepts
  `scenes:{"2024-01-01":["00:03"]}`. An off-grid requested scene cannot
  match the nominal scan schedule, so a successful submission can yield no
  files. Full-disk historical five-minute selection is a separate valid case;
  nominal cadence must not be mistaken for the selection step. Sparse
  orbit/observation scenes are an existing supported research-manifest mode.
- **Low/medium: malformed timezone offsets are silently normalized.** An orbit
  task with `2024-01-01T00:00:00+24:00` returns 202 and stores
  `2023-12-31T00:00:00Z`; `+08:60` is accepted as `+09:00`. Both violate the
  RFC3339 offset bounds and should be rejected instead of changing the request.
- **Compatibility difference:** the legacy `/api/v1/downloads/batch` accepts
  partial monthly intervals which `/api/v1/download-tasks` rejects. This is not
  necessarily a defect: legacy callers may depend on coverage intersections.
- GOES ABI L2 products in the earlier 2026-09-27 catalog, for example
  `goes16:ABI-L2-ACHAF` with catalog cadence `10m`, currently resolve to
  observation because the explicit known scan rules cover ABI L1b, not all
  ABI derived products. This snapshot is not proof of current availability or
  proof that every L2 product has the same schedule. GLM event data,
  space-weather instruments and cumulative products must not be given a scan
  schedule solely from a file duration or a generic GOES prefix; authoritative
  product-specific cadence and phase evidence is needed.

## Coverage and limits

`TestDownloadMatrixAudit` exercises leap dates, UTC offsets, daily/monthly/
annual/dekadal and 7/8/10/16-day metadata, custom boundaries, half-open scene
end, 32/33 products, duplicate and unknown settings, atomic rejection,
per-product inheritance/reset, version persistence, and the legacy batch route.
`TestDownloadOwnerDedupMatrixAudit` verifies owner-isolated identities and
reads, equivalent UTC/BJT timestamps, reordered products, normalized times,
and distinct selections. `TestDownloadSnapshotMatrixAudit` loads the 808 rows
from the earlier 2026-09-27 snapshot: all 802 archive fixtures accepted a
common one-year request, while six ERA5 fixtures rejected missing on-demand
inputs. That does not establish availability or validate actual upstream
search results. Existing
`TestCompositeIntersectingRequestStartIsScheduledOnceAcrossChunks` exercises
the real coordinator with mocked granules and downloader, including an
intersection at the start and exclusive end across discovery chunks.

The regression expectations for invalid offset and scan selection failures
were added before their pending implementation fixes and may fail until those
changes land. This is not a catalog-wide upstream cadence validation. The new matrix does
not exercise actual coordinator scheduling for each period, non-midnight phase,
or all provider search semantics. No live upstream order was made.
