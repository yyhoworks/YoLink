# Incremental synchronization and controlled change repair

Status: approved design with reviewed corrections, implemented in the next
milestone after manual backup/recovery; final implementation acceptance tracked
separately. No provider
delta API or cloud mutation is implemented by this document. Preserve the
Go/SQLite/static frontend/single-container architecture and existing task engine.
Independent gpt-6-astra/high design review and correction verification preceded
implementation. The implementation/correction review is a separate gate.

## Capability and operator contract

The currently supported provider contract returns a complete, validated folder
listing or an error. It has no verified durable change cursor. Incremental mode
therefore still lists every in-scope directory and compares entries to the last
completed baseline; it reduces repeated generation/database/output work, not
provider requests. No timestamp alone proves an unchanged subtree. Expose that
boundary in the UI and retain an explicit full reconciliation action. Existing
scheduler and event jobs remain the only execution mechanisms.

Provider-specific durable cursors may be added later behind a capability
interface with recorded expiry/coverage behavior. Do not label a polling list as
instant push. Size zero or absent modification time is unknown evidence, not
proof that file bytes are identical. With weak metadata, report only observed
identity/name/path/size/time changes and use full reconciliation for outputs.

## Durable baselines and completion

Add a transactional schema migration, preserving current tables and future
version rejection. Tables hold a bounded scan run, per-run directory/entry rows,
the current completed run for each rule/scope, and durable change rows. Entry
keys include account revision, remote ID and observed path. Enforce a separate
run-wide unique remote identity map before the existing Visited short-circuit or
media update: one remote ID may have only one path/type/observation per account
revision in a run. Same-ID different-path/type observations invalidate publication;
idempotent replay accepts only identical previously recorded observations. A
compound identity/path key alone cannot enforce this invariant.
Store only fields needed for comparison and generation, never download URLs,
cookies, authorization headers or expiring provider tokens.

Each run captures the effective Rule without LastRun, account revision, verified
folder/source scope, output mapping, public_base, signing-key identity and
maintenance generation. Capture all inputs that affect STRM bytes and use the
same frozen values for every item. Build through
the existing job queue, account gate and compact checkpoint protocol. A resumed
run retains the same run identity and inserts idempotently under unique keys;
it must not combine pages from different account or rule revisions. Propagate
the expectedAccountRevisionKey through every List, not merely within one folder's
pages. Publish and repair admission must validate frozen content/config identity
under a transaction/mutex protocol also honored by corresponding settings,
account and rule writes; a last query without write exclusion is insufficient.

Directory rows become complete only after the provider listing succeeds and its
entries pass duplicate/cursor/type checks. A scan becomes a baseline only when
the entire requested scope finishes without cancellation, truncating Limit,
listing/generation error, revision change, or dry-run. Publish the completed
baseline pointer and its corresponding diff transactionally. Partial/failed
runs may retain observations and failure reasons, but never assert absence or
replace the last completed baseline. Garbage collection may delete only runs
not referenced by an active job, published baseline or retained change preview.

Same-run checkpoint resume and a new failed-directory retry job have different
coverage. RetryDirs replaces the original worklist while Job can still advertise
the original root. First delivery therefore never promotes a RetryDirs-only job
to its original scope baseline or advances full-root completion time, even if
that retry reports completed. Complete coverage must come from a fresh full job
or explicit same-run directory proofs; do not merge older successes and new
retry observations across different times/revisions to imply a single snapshot.

Scoped event scans compare only their exact covered subtree. They cannot infer
deletion elsewhere or establish a full-root reconciliation timestamp. A later
full-root pass reconciles all subtrees and removes obsolete scope pointers in
one transaction. Schedule a normal full reconciliation at a bounded interval;
persist its last successful completion so repeated failures stay visible.

Pointer rowids are assigned above every surviving pointer in the publication
transaction, independent of wall-clock or admission order. Retained scoped
baselines carry bounded per-identity tombstones with the publication of the
positive observation they retired. Replacing a newer positive scope with
successive empty snapshots must not revive an older sibling observation.
Tombstones suppress only older/equal positive publications, include directory
identity transitions, remain hidden from user-facing history counts, and are
discarded when a root scan atomically retires every older covered pointer.
Repair and baseline publication share this same comparison implementation.

## Generation and local verification

New/changed entries pass through the existing ingest/filter/media identity/output
publication paths. A durable unchanged entry can skip generation only after
the current effective rule/account/mapping identity matches and the registered
output is present with the expected owner/hash. Missing local files become
explicit repair observations; differing bytes are preserved and reported. A
full pass still verifies all expected outputs. Companion-file handling must
retain its current conservative behavior when metadata cannot prove sameness.

Keep generation counters and activity semantics honest on crash replay. Do not
advance an entry's completion merely because a filesystem write succeeded;
reuse the current checkpoint and output-claim transaction after publication.
Incremental performance evidence must separately report listing request counts,
database writes, generated/skipped outputs, allocations and elapsed time on
repeatable synthetic trees; do not extrapolate it to all providers or live scans.

## Change review and repair

Show additions, observed renames/moves, metadata changes, sources absent from a
complete covered listing, local missing/modified output and outdated playback
entries. Pair a rename only with unambiguous remote identity in the same account
revision; otherwise show independent observations. Source absence never deletes
the local file or writes to the cloud. Local files outside registered output
ownership remain outside repair scope.

Preview binds selected changes to baseline/run IDs, account revision, effective
rule/output mapping, maintenance generation, current media/output owner, expected
registered hash and inspected local content hash or verified absence. It lists
exact effects and skipped conflicts. Require explicit confirmation of that
preview revision. Use the existing maintenance operation mutex plus engine/job
exclusion; re-read every identity/hash/absence immediately before publication.
Changed inputs reject the affected operation and require a new preview.

First delivery supports creating newly observed entries through a normal job
and restoring registered missing STRMs with exact expected bytes. Updates to
existing STRMs, source removals and user-modified files remain review-only.
The current maintenance Rename path does not exclude edits by another process
between its last hash check and rename. Do not reuse it while promising to
preserve every concurrent external modification. A future replacement feature
requires a separately reviewed filesystem protocol and real Linux interleaving
tests; adding another pre-rename hash read does not close that race. No automatic local
deletion, rename, overwrite of user changes, media movement or provider write.

Repair progress is durable per item. Before any effect, persist the immutable
expected owner, content identity, before/after hashes, preview generation and
verified absence. Missing-file repair uses exclusive atomic publication; a file
appearing concurrently is preserved. On replay, identical post-hash bytes alone
are insufficient: verify unchanged owner/identity and transactionally complete
the output registration plus per-item result. Never return success while the
old output hash remains. Revalidate preview generation at every item and retain
conflicts for a new preview. Restored historical
previews cannot execute after backup/rollback changes maintenance generation.

The current saveScan transaction covers job/checkpoint/claims, while media,
output registration and activity writes occur earlier. Implementation must add
the baseline entry completion and item result to an explicit final transaction
with cursor/claims. Filesystem publication remains outside SQLite: durable
pre-item state and verified post-hash reconciliation bridge that window. Use
per-item activity idempotency keys or explicit recovery-confirmed records, with
defined created/skipped semantics; do not promise exact-once counters merely
because a checkpoint advanced. These are necessary changes to the existing
engine seam, not a claim that the seam already provides atomic item commits.

## Required acceptance

- Current schema upgrade/rollback, future version, backup validation/quarantine
  compatibility for the new tables and restored in-progress runs. Update the
  independent migration list and bounded table readers in backup_snapshot.go,
  pauseRestoredWork/quarantineBackupPayloads, and finishRestoreReview so imported
  scan/repair operations cannot replay and no restored preview becomes eligible.
- Empty complete folder versus failed/incomplete listing, pagination duplicate,
  cancellation, limit, account/rule edits, targeted scope and full-root overlap.
- Process death before/after run completion and baseline pointer publication;
  repeated restart never creates false source deletion or duplicate repair.
- Two parents returning the same directory ID or file ID, partial RetryDirs
  success after root failure, public_base/account edits between items and after
  the final item but before publication; none can publish a misleading baseline.
- Concurrent scheduled/manual/event jobs, repair against a changed file, symlink
  and mount boundaries, stale ownership/revisions, and user modification after
  preview. Preserve cloud contents and unrelated output in every case.
- Failure after item intent, file publication, output registration and before
  result/cursor commit; repeat startup with a changed owner and post-hash file,
  preserving conflicts and converging registration without duplicate success.
- Unchanged second scan, metadata-weak providers, renamed identity, missing local
  file and periodic full reconciliation with reproducible before/after evidence.
- Desktop/mobile/error/empty/long-name/uncertain-write review and actual-handler
  tests, plus independent Astra design and implementation/correction reviews.
