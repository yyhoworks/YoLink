# Account health and playback diagnostics — backend contract

Intermediate implementation: backend endpoints and isolated regression tests are
present. User-facing center/wizard UI, browser acceptance and production release
are not yet complete. Do not treat this document as release approval.

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

Observations and transition notifications are stored together in SQLite schema
version 3. Repeated same-state failures do not mark an already-read notification
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

Schema version 4 stores at most the latest 200 reports, pruned on report creation;
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

Current backend increment passed `go test -count=1 ./...` (43.011s), `go vet
./...`, and `git diff --check`. Independent interim review identified and verified
corrections for account-first pagination, legacy failure visibility, transient vs
credential error classification, unsupported quota, legacy lookup mutation and
stale-request health contamination. The final correction's focused review tests
passed (0.110s). These are interim backend checks, not final candidate/UI signoff.
