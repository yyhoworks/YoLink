# Page-aware refresh

The shell retains Go + SQLite + classic static scripts. No polling library,
service worker, subscription service or new runtime dependency is added.

## Policy

- `/api/state` uses one completion-scheduled timer: 4 seconds while the visible
  dashboard/generation-status page has queued or running jobs, otherwise 30 seconds.
  A failed refresh cycle doubles its delay, capped at 60 seconds. Dashboard and
  notification failures count even when the shared state endpoint succeeds.
- The next timer starts after the current cycle settles, preventing overlapping
  background reads on slow connections. Explicit reads can supersede old reads;
  abort plus identity checks prevent stale application.
- Hidden documents stop shared polling and cancel state/dashboard/settings reads.
  Foreground starts a fresh state read, including an interrupted first bootstrap.
  Login/logout invalidate old state responses; late responses cannot reopen a
  logged-out app. Incomplete settings loads resume after a successful state read,
  even when the first foreground read failed.
- Only the visible page renders its state-backed sections. Recent ingest belongs
  to generation, legacy event options to settings, migration restoration to
  maintenance, and playback controls/media to the record-tools tab.
- Settings integrations/automation/channel/client reads are demand-driven on
  entry, drawer opening, explicit refresh or mutation follow-up, not every state
  poll. Their parent cancellation propagates to follow-up reads.
- Records keep their existing scoped 10-second page polling; inventory and cloud
  directory listings remain explicit. No new periodic provider probes occur.
- Notifications update the shared badge at a 30-second minimum interval; their
  list DOM changes only when that page is visible.

## Input and focus protection

An open settings drawer keeps the unsaved service-address field even after blur.
An open rule editor keeps its account options and text draft if background state
changes. The server continues to validate submitted account/rule state.

List rerenders restore the focused control by ID or stable data attributes within
its original ID-bearing container, avoiding the duplicate action for the same job
in a different section. Disclosures/selections retain their existing identity
handling. Icon conversion only processes newly visible `i[data-lucide]`
placeholders, not existing SVGs, and tab reveals convert their own new icons.

## Measured before/after

`frontend/tests/browser/refresh-measure.spec.ts` observes requests after initial
loading and DOM child/text mutations in the hidden generation view. Each case
steps an installed browser clock through fifteen 4-second intervals with a short
network-settlement opportunity between steps. This is a deterministic synthetic
runtime workload, **not real-device CPU, NAS throughput, or Web Vitals evidence**.

| Idle view, virtual 60-second window | Before (87282f4) | After |
|---|---:|---:|
| Accounts: state reads | 15 | 1 |
| Dashboard: state / dashboard reads | 15 / 15 | 1 / 1 |
| Settings: state reads | 15 | 1 |
| Settings: each of integrations, automation, channels, clients | 15 | 0 |
| Hidden generation DOM mutations (each view) | 345 | 0 |

The completion-based timer and stepped clock put the second idle cycle outside
this measured window. Steady-state nominal idle cadence is approximately two
cycles/minute, plus manual/navigation activity—not a promised one request/minute.
Active status remains approximately fifteen cycles/minute when requests are fast.

Reproduce from `frontend/`: `npx playwright test refresh-measure.spec.ts`.
For comparison against a baseline that lacks these invariants, set
`YOLINK_REFRESH_BASELINE=1` to retain measurement without optimized assertions.
Do not change the workload or substitute these counts for production profiling.

## Verification

Eleven browser lifecycle cases cover active/idle cadence, focused duplicate job
controls, desktop/phone unsaved settings, interrupted bootstrap, hidden/foreground
and logout races, slow-response exclusion, partial endpoint backoff, immediate
range changes, ingest placement, rule draft retention, interrupted settings
resumption after foreground failure, and tab icon reveals. Measurements add three
cases. Existing browser tests continue to cover records cancellation, routes,
themes, account forms, diagnostic/recovery and wallpaper behavior.

Local results: frontend 88 unit tests/types/lint/generated PASS; Go full suite
37.426s and vet PASS; 11 lifecycle browser tests PASS; full 68-case browser run
ended with `refresh-final-browser/.last-run.json` status `passed`, no failed IDs;
real Go HTTP/SQLite/queue diagnostic browser test PASS7.821s. These are isolated
fixtures, not deployed-NAS verification. Independent code correction review found
no remaining actionable runtime findings in this increment. Whole-goal final
reviews and release gates remain pending.

Independent UI finish review returned scoped `ship` after desktop/phone draft,
smoke and sampled theme captures; no new UI-preservation finding. Documentation
consistency review confirmed runtime-only change, with no design-system rewrite.
Neither review constitutes either final whole-suite signoff.

> 状态更新（2026-10-21）：页面感知刷新已随 0.14.0 发布。上文 “final reviews and release gates remain pending”、
> “Neither review constitutes either final whole-suite signoff” 等表述为实现期历史记录，不代表当前发布状态。
