# Download ordering UI audit

Scope: simulated UI logic only. No production tasks or upstream submissions were created. Run `node --test web/download-order-audit.test.mjs`; the existing `web/download-timezones.test.mjs` contains additional mixed-category, retry, pagination, and filter cases. The application files are under active concurrent development, so results should be rerun on the final revision.

Observed during audit: the initial focused suite passed 5/6, with the visible-name search case failing; existing timezone suite passed 21/22, with its `__not_applicable` assertion expecting a placeholder that the current implementation intentionally omits. Both were reported to the parent. The expanded suite passed 8/11: the search and two timezone-preservation cases failed while the parent was implementing those changes. Rerun against the final revision.

## Findings

The initial failures below are fixed and all 11 focused audit tests now pass: visible-name search includes `product_name`/`product_code`, and timezone changes preserve exact per-product overrides. Shared civil dates remain the entered dates; UTC calendar periods remain unchanged. Final browser verification and residual limits are tracked in [the consolidated checklist](download-audit.md).

- **Search, reproducible:** A product whose displayed `product_name` is `Cloud properties` and whose `display_name` is `MYD06_L2` is not found by searching `Cloud properties`. The renderer prefers `product_name`, but the search fields omit it. The focused test asserts the expected behavior. Parent notified.
- **Timezone semantics, confirmed requirement:** Existing minute-exact product overrides must preserve absolute instants when UTC/BJT changes, including scan bounds and selected times; period products remain UTC. Shared calendar-date inputs deliberately remain the selected civil dates. Current implementation at the audit run reinterpreted overrides and shifted them by eight hours. New tests cover UTC-to-BJT-to-UTC around leap-day and year-end boundaries, alongside mixed UTC periods and unchanged shared dates.

## Simulated checks

- Same-instrument cross-satellite ABI definitions with matching cadence and wavelength/resolution enable shared controls. Wavelength or resolution mismatch disables shared band; cadence mismatch disables shared time; instrument mismatch disables both; missing band metadata disables shared band.
- Shared edits propagate; removing and reselecting a product clears that product's overrides while preserving the other product's edits. Divergent settings render `分别设置`.
- Monthly, dekadal and annual leap-year boundaries use the next exclusive UTC boundary. Observation window equality, impossible dates, and off-cadence scan times reject.
- A mixed scan/orbit confirmation keeps its snapshot after the form is changed, and simultaneous confirm calls cause one mocked POST.
- Queue uses a native button with `aria-expanded`; product settings use native `details`/`summary`. CSS constrains the collapsed queue to a single fixed-height row and truncates the title. This is a structural check, not a browser layout or keyboard test.
- Incompatible shared selectors are disabled and reject programmatic changes. Selected-band resolution and missing-version/area omission render as expected in the test fixture.
- Existing tests cover observation, orbit, scan, daily, weekly, composite, dekadal, monthly and annual controls; invalid values; search selection retention; retry after explicit failure; at most 30 displayed tasks; and task detail rendering. Run them on the final version, since the existing suite may reflect concurrent edits.

## Remaining checks

- Test actual desktop and narrow mobile layout with long names, including text clipping, visible expander, focus, Enter/Space and pointer interaction. The VM does not implement browser layout or real event dispatch.
- Verify timezone-switch behavior with live controls, reset and edit combinations, and confirmation display for mixed UTC periods and BJT observation windows.
- The mock cannot establish backend idempotency after a lost response, actual upstream availability, scheduling behavior, or GOES product cadence. Those require separate backend and catalog audits.
