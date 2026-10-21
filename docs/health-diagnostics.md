# Account health and playback diagnostics

Status: 已随 0.12.0 发布，账号健康与播放诊断为正式功能。本文件保留为实现
记录；下文中 "intermediate implementation"、"not yet complete" 等表述均为
实现期历史记录，不代表当前发布状态。

## Account observations

`GET /api/accounts/{id}/health` requires an administrator session. It returns
independent `list`, `resolve` and `quota` operation states, observed configuration
revision, last success, consecutive failures, checked time, retryability and a
suggested next retry time. An untested revision is `unknown`; disabled accounts
remain disabled. Capacity success does not clear a resolve failure. Unsupported
capacity is explicitly `unsupported`, not a credential or health failure.

Whole List calls are observed, not each successful page: a failure on page two
does not publish a successful directory observation. Cancellation does not mark
credentials bad. Results are discarded if the account was removed, disabled or
edited; an older-started request cannot replace a newer-started result.

Observations and transition notifications are stored together in the SQLite
schema (current version 5). Repeated same-state failures do not mark an already-read notification
unread again. Only application-owned descriptions are persisted; raw upstream
errors, URLs and headers are excluded. Health persistence failure does not turn
a successful media response into an error; it is exposed through the existing
activity write-error indicator.

Affected jobs are filtered by account before pagination, 100 per page. Follow
`next_before` when `has_more=true`. Legacy failures attributable via their current
rule are visible but ineligible if they lack a snapshot.

`POST /api/accounts/{id}/recover` accepts `revision`, explicit `confirm: true`
and 1–50 selected `jobs`. It requires a successful list observation no older than
five minutes and no outstanding credential/rate-limit restriction. The shared
account gate protects the revision check through queueing. Each task must belong
to the account and match the current complete rule, ignoring only LastRun. The
existing retry engine enforces ownership, scope, saved checkpoints, one active
task per rule and duplicate-retry protection. Response separates `started` and
`deferred`; it does not silently drop selected scopes. Recovery is controlled,
not automatic after credential edits.

## Diagnostic reports

`POST /api/media/{id}/diagnose` accepts only `output` (optional registered STRM
key) and `probe` (boolean). There is no arbitrary URL parameter. It reports:

1. Registered media index.
2. Local STRM ownership, content and current-entry checks.
3. Enabled account/configuration state, not inferred credential validity.
4. Cancellable original-file resolution, respecting channel requirements.
5. Optional bounded HEAD/GET Range probe, reading at most 1024 media bytes.

The operation has a 45-second deadline and uses its own cancellable resolution,
not the detached playback cache flight. A changed account revision marks the
report stale. A failed local STRM step remains failed even if the independent
cloud probe succeeds. Modified user output is never rewritten.
Legacy path lookup is in-memory only during diagnosis; it does not update the
stored media identity. An expected-revision context rejects stale provider
requests before network/refresh and cannot overwrite current health observations.

Reports contain safe step descriptions, opaque media/account identifiers, timing,
HTTP status, channel, bounded byte count and explicitly limited conclusions.
They omit names, full source/output paths, temporary targets, signing URLs,
headers and credentials. `readable` is a NAS-side probe result, not player
success, watch history, audio/codec compatibility or proof of remote seeking.

The current schema (version 5) stores at most the latest 200 reports, pruned on report creation;
export additionally enforces a seven-day expiry. Authenticated
`GET /api/diagnostics/{id}/export` downloads the same sanitized JSON. Export does
not rerun network checks. Existing playback and generation routes are preserved.

## Isolated tests added

- Distinct per-operation health, repeat alert dedupe, transitions, old-request
  ordering, cancellation, edited/disabled revision rejection and raw-error safety.
- Listing page failure, recovery freshness/confirmation/revision/ownership,
  duplicate selections, changed rule options and rate-limit guards.
- More than 1000 unrelated failures cannot hide selected-account failures;
  affected-job cursor pagination and missing-snapshot visibility.
- 139 refresh 429/503/invalid XML, failed credential persistence, unsupported TV
  quota without a false alert.
- All five diagnosis steps; HEAD403 plus valid GET206; 1024-byte bound; report
  redaction/export/auth/expiry; modified output preservation; arbitrary URL and
  unowned path rejection; private-target fetch denial; cancellation and stale
  revision marking; upstream error text excluded.
- Legacy lookup leaves stored media unchanged; stale diagnostics do not contact
  the provider or alter current health; XML error pages and malformed Content-Range
  totals cannot be reported as a valid ranged media probe.

No real provider or deployed-NAS behavior is claimed by these fixture tests.

## Contextual UI

`web/reliability.js` extends the existing account cards and generated-file list.
It does not start background cloud probes or add navigation destinations.

- Account details show separate directory/resolve/quota observations, timestamps,
  failure counts and retry hints. Opening the panel only reads stored observations.
  The explicit directory test contacts the provider.
- Affected-job pagination preserves selections, stops on a changed account
  revision, and shows why legacy/changed-rule jobs cannot recover. Users select
  at most 50 jobs, inspect the named scopes and explicitly confirm submission.
  Started and deferred results remain distinct. An uncertain network result is
  not retried automatically and is not hidden by the following status refresh.
- File diagnosis sends the selected registered output key and media identity.
  Bounded probing is opt-in. Cancellation and navigation abort the diagnostic
  request; stale callbacks cannot replace the next file/account's detail.
  A failed local STRM remains visibly failed even when the cloud probe succeeds.
- Export re-fetches the authenticated sanitized report. Expiry is actionable;
  a superseded export cannot download under the next report's identity.
- Recovery writes are not automatically cancelled/retried on navigation because
  the server may have already accepted the work. Navigating away hides the old
  panel and completion communicates a short result rather than populating a new
  account's panel. Closing restores focus to the current equivalent trigger.

The new CSS inherits the incumbent semantic tokens, controls and materials;
no brand, global theme, runtime dependency or new modal was introduced.

## Reproducible browser verification

From `frontend/`: `npm run check`, then `npx playwright test`.
The default browser suite serves synthetic read-only fixtures; tests explicitly
intercept writes. The reliability cases cover desktop 1440px and phone 390px,
pagination, eligibility, explicit recovery, mixed outcomes, failed writes,
revision changes, cancellation, close/reopen, export races and expired exports,
navigation/logout, six original palettes, and both glass text modes.

For real HTTP/auth/SQLite/queue/file integration, install frontend dependencies
and Edge, then run at the repository root:

```powershell
$env:YOLINK_BROWSER_E2E='1'
go test -run '^TestReliabilityBrowserEndToEnd$' -count=1 -v .
```

This opt-in test creates all data/output under Go temporary directories and binds
an ephemeral loopback HTTP server. Only the cloud and media transport are fake.
It drives normal login, directory test, recovery, diagnosis and download, then
checks persisted recovery/report records and preservation of edited STRM bytes.
It does not use NAS mounts, production credentials or external network services.
The test is skipped in ordinary `go test` runs unless explicitly enabled.

Local evidence from this UI increment (2026-10-20): Go full suite PASS 38.173s,
vet PASS; frontend type/lint/generated checks and 88 unit tests PASS; full browser
suite 54/54 PASS, including 17 reliability cases; opt-in real-handler test PASS
(latest 9.311s including browser startup and capture). Screenshots are local
artifacts under `.impeccable/review/reliability-browser/` and
`.impeccable/review/reliability-live/`, not production screenshots.
Glass captures use top/task viewport pairs: Chromium full-page screenshots can
truncate a fixed wallpaper below the initial viewport and are not valid evidence
of that region's real rendering.

Independent code review verified the async/focus corrections with no remaining
correctness blocker identified in this increment. Independent UI review inspected
the desktop/phone/palette/glass/real-handler captures and returned scoped `ship`.
It identified two inherited glass issues, both corrected and re-reviewed as
resolved: readable semantic colors now take priority over neutral fallback, and
the account browse action is classified as a primary control. Regression checks
assert distinct status colors and >=4.5:1 browse contrast in both tested glass
foreground modes. This does not certify arbitrary wallpapers or all glass states.
Documentation consistency review confirmed no durable visual-system change;
existing outer PRODUCT/DESIGN history was preserved, not copied into this checkout.
These reviews are increment-scoped, not either of the two final-suite signoffs.

Current backend increment passed `go test -count=1 ./...` (43.011s), `go vet
./...`, and `git diff --check`. Independent interim review identified and verified
corrections for account-first pagination, legacy failure visibility, transient vs
credential error classification, unsupported quota, legacy lookup mutation and
stale-request health contamination. The final correction's focused review tests
passed (0.110s). These are interim backend checks, not final candidate/UI signoff.
