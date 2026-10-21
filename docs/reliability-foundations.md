# Reliability foundations — implementation evidence

Status: 本文件为实现期工程记录；所述可靠性基础已随 0.12.0–0.14.0 发布。下文
中 "not release approval"、"remain pending" 等表述为历史记录，不代表当前发布
状态。No production data was used by the tests below.

## Implemented foundation changes

- Provider errors carry a stable kind, retryability and bounded Retry-After.
  Unknown provider-specific codes do not imply expired credentials. HTTP 403 is
  distinguished from 401; wrapped deadlines/cancellation retain their identity.
- Account requests use a cancellation-aware priority gate. Playback can run
  between directory pages; at most four queued foreground requests overtake a
  waiting scan. Import/enable/remove share the same account gate. Every page
  reloads credentials; a user configuration revision change aborts the listing.
- Directory responses require valid lists and pagination metadata. Duplicate,
  empty-intermediate, invalid/missing metadata and changing-total pages fail
  without returning a partial successful directory. Strict checks still require
  real-provider compatibility checks before release.
- Account connection/quota reports commit only against the revision they tested;
  older reports are hidden after account changes.
- Schema versions 1 and 2 upgrade the legacy schema transactionally and add
  compact checkpoint storage. Newer schema versions are refused before keys or
  schema initialization are changed. Interrupted migrations do not advance the
  version or retain partial DDL/data changes.
- Checkpoint directory payload, scalar cursor and incremental output claims are
  stored separately. Job progress and cursor/claim changes still commit in the
  same transaction. Legacy inline snapshots convert on the next successful save.
  A cleanup trigger removes associated state when the checkpoint is deleted.
- Checkpoints reject missing structural state, invalid cursors, mismatched
  snapshots and invalid/repeated file identities. Companion copying propagates
  cancellation, so shutdown cannot advance past unfinished subtitle/NFO work.
- Tentative per-item counters revert to the committed cursor state on item
  cancellation/error or checkpoint failure. A two-movie Limit=2 shutdown/restart
  test confirms replay cannot prematurely reach the limit and omit movie two.

## Reproducible checkpoint benchmark

Command (from repository root, Go available on PATH):

```sh
go test -run '^$' -bench '^BenchmarkCheckpointProgress$' -benchtime=20x -count=2
```

Observed on Windows amd64, Intel i5-12600KF, Go 1.27.2, isolated SQLite databases:

| Directory | Representation | Time per progress commit (two runs) | Allocated bytes/op |
|---|---|---|---|
| 100 entries | prior full JSON | 0.744 / 0.727 ms | 25,814 / 25,284 |
| 100 entries | compact | 0.867 / 0.599 ms | 3,720 / 3,279 |
| 10,000 entries | prior full JSON | 7.101 / 7.662 ms | 3,735,547 / 4,204,383 |
| 10,000 entries | compact | 0.926 / 0.780 ms | 5,670 / 5,239 |

The test uses actual SQLite transactions, the same job-progress write in both
variants and a preloaded directory/claim set. Setup is excluded. The compact
variant measures ordinary cursor updates; directory-boundary changes and first
snapshot writes are intentionally outside this microbenchmark. Small-directory
times overlap/noise; only the large-directory hot path shows a clear reduction.
These measurements do not establish NAS scan throughput or network latency.

## Verification so far

- Current foundation revision: full `go test -count=1 ./...` passed (35.362s),
  `go vet ./...` and `git diff --check` passed. Independent correction review
  reran focused recovery tests (0.619s), confirmed the generation-limit fix and
  found no new blockers in that correction; final release review is still pending.
- Existing Go suite passed after initial scheduler/error changes and after
  schema/compact-checkpoint changes. Focused schema, rollback, future-version,
  compact checkpoint, corruption, stale-report and companion recovery tests pass.
- Earlier baseline: 88 frontend unit tests, typecheck, lint and generated-asset
  consistency passed. No UI source changed in this foundation increment.
- Independent interim review found semantically empty checkpoints and companion
  interruption/error propagation gaps; fixes and regression tests were added.
  This is not one of the required two final candidate signoffs.
- Windows race invocation reported `-race requires cgo`; no local C compiler was
  found on PATH. A supported race-test environment remains a release gate.

## Boundaries still requiring work

（历史记录：以下为实现期未完成清单，所列功能已随 0.12.0–0.14.0 发布。）

- Health/recovery center, diagnostics wizard, backup/restore, incremental sync,
  change review, templates and page-aware refresh are not complete.
- File publication and activity logging precede a per-item checkpoint commit.
  An abrupt process death in that pre-existing narrow window can replay an item;
  byte/ownership checks preserve output, but exact result-counter/activity replay
  semantics need further hardening and crash-injection tests before release.
- No real-account cloud behavior, playback, production migrations, Docker image
  or NAS deployment was verified by this increment.
