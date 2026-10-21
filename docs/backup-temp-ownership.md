# Backup/restore temporary-data ownership corrections

## Resumed checkpoint — final verification pending

User resumed after 0.12.0 acceptance. This implementation remains local, outside
published0.12.0. Astra's depth64/node800128 P1 is corrected: new admission bounds
distinct parents before materialization; cleanup admits the finite legacy
file/path worst case, bounds its live traversal frontier and closes directory
handles before descending. Existing deep candidates remain supported. A further
live-ancestor-list memory-budget P2 was corrected and independently verified.

Deep export/cancel/interrupted-stage, low-FD legacy cleanup, ambiguous upload
publication, extra multipart rejection cleanup and upload-unlink parent-sync
failure regressions are implemented. Current full persistent Linux matrix and
bounded-tmpfs ENOSPC results, final-source review and deployment are still gates.
Do not treat old interrupted or timed-out runs as passes.

Existing evidence: Windows targeted cleanup/kill/concurrency suites passed;
Linux network-none private persistent focused cleanup/HTTP/replay suites passed
(21.200s and8.632s, source unchanged). Full Go53.493s/vet passed before subsequent
test-only additions. Full persistent matrix was deliberately stopped at user's
request (exit143,247.630s, source unchanged): NOT a pass. Earlier600s timeout is
also not a pass. Dedicated new bounded-tmpfs ENOSPC run remains pending.

## Approved design and current implementation contract

Development baseline: `2e1805a` (not the published phase-one checkout). Addresses
the retained Astra HTTP findings: partial directory deletion can erase its only
cleanup marker; killed exports leave plaintext snapshots indefinitely. This is a
small filesystem-ownership module, not a new task engine or database migration.

## Restore candidates

- Before creating any candidate directory/plaintext, durably publish a bounded,
  checksummed `.restore-control/unarmed-<32hex>.json` ownership record. Its only
  target is the sibling `.restore-control/<same-id>`; no archive/user path is
  accepted. Keep existing internal unarmed marker as the never-used activation
  check, but never rely on it for cleanup identity.
- Preserve the external record while a candidate is retained as an upload or is
  referenced by the active/prior journal chain. Those protected identities always
  win over cleanup. Historical candidates cannot be armed again; retain/retest
  the existing identity guards and add A→B→replay-A regression.
- On explicit cancellation, validate the complete active/prior/upload state,
  ensure external ownership is durable, then durably remove a matching upload
  pointer BEFORE publishing cancellation. Until cancellation publication, the
  active journal still protects the candidate. Reload all protection before
  deleting. Any failure leaves the candidate protected or externally owned.
- Remove only an unprotected owned directory, sync its control parent, then
  remove its external ownership record and sync again. Partial removal/faults
  retain the external record. Recovery/startup and the next expensive operation
  repeat this idempotently. No user media/output or journal baseline is deleted.
- Read the journal/history/upload state before cleanup. Corruption fails closed;
  never guess which session is protected. A surviving protected external record
  is normal, not permission to delete a retained baseline.
- Recognize existing internal-marker candidates only to migrate their ownership
  into an external record BEFORE deleting. Unmarked/unknown directories remain
  untouched. Bound entry counts and ownership-record bytes before allocation.

## Export workspaces

- HTTP export uses one private `.backup-work/<32hex>` workspace containing both
  its `.backup-snapshot-*` child and encrypted temporary archive. Before creating
  the workspace/plaintext, atomically publish and sync the external sibling
  `.backup-work/<same-id>.json` ownership record (format/kind/id only).
  Publication uses a flushed temporary file and an exclusive atomic hard link,
  not a replace-existing rename. A failed publication never creates plaintext.
- A small module owns create, cleanup/retry and close. Use rooted handles, fixed
  generated names, private permissions, strict bounded ownership JSON, no
  symlinks/special files and Linux same-device checks. Close handles before
  removal, including on Windows. Ownership lives outside the deleted tree.
- Run cleanup once under the data-directory process lock before Store/Engine
  startup, and again under the existing backup-operation mutex before creating
  the next export. Those points cannot collect another cooperating live export.
  Normal completion/error/cancel uses the same cleanup implementation.
- Delete the workspace, sync the control parent, then remove the ownership
  record and sync again. Until workspace deletion is durably synced, retain the
  claim. A final sync failure AFTER claim unlink returns an error: the claim may
  be absent or reappear after a crash, both safe because deletion was synced.
  Unknown/malformed records/paths are never broad-deleted; return safe errors.
  Do not scan/delete legacy `.backup-snapshot-*` names without ownership proof.
- Linux requires actual file/directory fsync ordering before writing secrets.
  Windows export promises tested process-crash cleanup only: sync the claim file
  and exclusively hard-link-publish before creating secrets, but do not claim unsupported directory
  power-loss durability or restore activation. No secure-erase claim is made.
- Delete/sync plaintext after closing snapshot readers and BEFORE success
  headers. Keep the encrypted archive and external claim through streaming, close
  archive/root handles before final removal. Cleanup ignores canceled request
  contexts. Post-stream encrypted cleanup errors are logged/notified; they cannot
  retroactively change the HTTP status.
- Cleanup failure is observable. The live operation fails safely; startup does
  not proceed into normal workers while owned plaintext cleanup is unresolved.
  Unrelated existing exports/backups/output and unknown directories are preserved.

## Required evidence

1. Normal export, error and cancellation leave no owned plaintext workspace;
   archive still decrypts/validates with matching keys and administrator verifier.
2. Kill a real export subprocess after plaintext snapshot creation and before
   archive completion; restart the startup-cleanup path twice, preserving original
   database/keys/outputs and unrelated same-prefix directories.
3. Inject partial removal after internal marker deletion; external record survives
   and a fresh manager completes cleanup. Exercise crashes before/after parent
   sync and ownership-record removal, no silent successful cleanup.
4. Cancellation publication/CLI/HTTP import rejection; old active/prior/upload
   sessions remain intact. Verify A→B→replay-A cannot change active journal or
   retained candidate/baseline/prior evidence.
5. Malformed/oversized claim, symlink/root/path mismatch, unknown files and count
   bound, repeated cleanup and concurrent operation exclusion.
6. Exact-source full Go/vet/frontend regression plus Linux no-network private-
   disk recovery/cleanup drills; completed implementation/evidence and all fixes
   receive explicitly assigned Astra/high review. Never infer hardware power-loss
   protection from process fault injection or tmpfs evidence.
