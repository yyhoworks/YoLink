# Encrypted backup/restore — implementation and verification contract

Status 2026-10-21: manual encrypted export/import, isolated validation, restart
activation, retained rollback, startup lock/hold and settings UI are implemented
on codex/reliability-suite. Published 0.12.0 does not include this work. Independent
gpt-6-astra/high design/implementation reviews and focused tests exist; final
persistent recovery, ENOSPC, frozen-candidate and deployment gates remain. See
backup-temp-ownership.md and restore-activation-design.md for the current
filesystem protocol, and backup-restore.md for operator instructions.

## Format and snapshot

- Pinned `filippo.io/age v1.3.2`, scrypt passphrase encryption (work factor18,
  decryption max18), standard age authenticated streaming format. No new service.
  One backup/restore CPU-heavy operation at a time will be enforced by the API.
- Encrypted uncompressed tar: first `manifest.json` with format version, app
  version, SQLite schema, created time and ordered name/size/SHA256 entries.
  Content hashes are inside the authenticated envelope. Full age EOF must be
  consumed after tar EOF, rejecting truncation/tamper/trailing plaintext.
- Current limits:100,000 files and100,000 distinct parent directories,2GiB per
  file,4GiB total payload,32MiB manifest;
  ciphertext additionally bounded. UI must state limits and required temporary
  disk space. No compression avoids decompression bombs, not all resource risks.
- SQLite `VACUUM INTO` provides consistent snapshot; auth read lock protects the
  current verifier, maintenance mutex protects migration plans/recovery copies.
  Credentials/playback/events keys are immutable during Store lifetime.
- Include DB, three keys, administrator verifier, `plans/` and `strm-backups/`.
  Exclude compose/env, arbitrary files, live WAL/SHM, output media/STRMs, logs,
  prior backup bundles and restore staging. Preserve user-edited media/output.
  Snapshot materializes a salted verifier for env-supplied initial admin password,
  after reauthentication, rather than saving plaintext/env credentials.

## Validation and isolated drill

- Decrypt only into a fresh private staging directory; allowlisted relative paths,
  no links/special files, duplicate/case-collision rejection, length/size/digest
  checks. Remove only freshly allocated staging when validation fails.
- Read-only immutable SQLite inspection, schema/manifest agreement, integrity
  check, bounded serialized rows and decryption checks for account/integration/
  notification configuration. No openStore/newEngine during inspection.
- Reject nested/fold-equivalent duplicate JSON fields, typed field errors and
  SQL/payload ID or status disagreement. Bound bytes before allocation (including
  credentials/settings and their aggregate sizes). Compare all schema objects
  with a fresh database emitted by the exact supported migration chain before
  any staged write; extra triggers/views/virtual tables or altered SQL fail.
- Before staged activation, apply schema migrations to staging only and atomically
  pause queued/running/resumable jobs, disable automatic rules and event clients/
  legacy event access, cancel historical pending/paused deliveries, mark pending
  ingest failed. Keep original archive unchanged. Display counts and consequences.
- Canonicalize Rule/Job through Go's typed readers in bounded batches, including
  case-folded fields; clear resumability on every job. A durable restore marker
  is loaded by openStore and immutable for that process lifetime. Under that hold,
  startup never requeues jobs and Engine starts no background workers. Enqueue,
  direct run/ingest/delivery and event requests are also rejected/suppressed.
  Explicit administrator review/diagnostic permissions, acknowledgement and
  controlled restart are implemented; the immutable hold lasts until restart.
- Drill must load the staged data in an isolated no-network process, prove stable
  signing/credentials/admin login and no automatic job/notification replay, verify
  preservation of original files, and test wrong password, corrupt/truncated archive,
  path attacks, future schema, failed migration, disk errors, interrupted activation.

## Restart activation and rollback

The active data directory may be a Docker mount point, so do not rename that
directory itself. Stage inside /data and activate only on a controlled restart,
before normal Store/Engine/server initialization. Explicit current-admin password,
backup password and confirmation are required; export/import remain admin/CSRF
guarded. A pending plan must be versioned and integrity-checked and have a durable
per-file journal plus a complete rollback directory on the same filesystem.

Before publishing a pending activation, validate and quarantine staging, bind its
manifest/digest to the confirmation and detect changes. On next startup, take an
exclusive data-directory process lock, retain every replaced managed file plus
the previous DB/WAL in rollback, then perform a journaled replacement. Each
incomplete startup must deterministically roll back or fail closed; never start
with mixed old DB/new keys. Retain unrelated files and all existing recovery
copies in the recoverable old snapshot. Do not auto-replay historical work.
Disk space/failure/cancel paths need evidence before activation is exposed.

Rollback must be executable without successful login/new DB open, restore the
previous DB/key/config set together, and leave an audit summary without secrets.
Production deployment must prepare its separate verified backup/rollback too.

## Final verification gates

- Snapshot copying already enforces file/aggregate limits and cancellation;
  further race/permission/disk-failure evidence remains required.
- Fresh staging/snapshot cleanup now reports failures and retains the orphan path
  for administrator cleanup. No silent successful cleanup is claimed on failure.
- Operation exclusion, rate limits, bounded external ownership and startup
  cleanup are implemented. Legacy deep candidates remain collectable; fresh
  archive/snapshot admission bounds distinct parents before creating them.
- Historical journals share a65-node admission/reading limit. A full chain
  rejects the next confirmation before publishing pending or changing history.
- Review current license inventory and frozen-candidate dependency regression.
- Complete exact-source Linux persistent activation/rollback faults, bounded
  tmpfs ENOSPC, UI lifecycle and deployment acceptance; focused unit results
  alone do not release this feature.

## Current local verification

2026-10-21 focused TestBackup* PASS12.908s, including archive attacks, folded-key
and multi-batch quarantine, malformed restore marker, two separate-process starts,
login/credential/signing preservation and unchanged user output. Child processes
deny/count outbound HTTP and provider calls. This is not an OS network namespace
drill, activation test, complete restore rehearsal or full regression result.
After Astra correction verification, no actionable issue remained in the scoped
backup/startup/lock code. Independent TestBackup|TestDataLock PASS11.876s. Parent
full Go regression PASS50.913s; vet and frontend check88 tests/type/lint/generated
PASS. Added second-process rejection, killed-holder takeover and simultaneous
starters (Windows PASS1.115s). Linux Docker network=none suite PASS21.246s elapsed
with source hashes unchanged, no usable external interface and denied outbound
socket connection. Test data was private tmpfs; this verifies namespace/process
behavior, not persistent-disk power-loss durability. Activation design is in
restore-activation-design.md and has scoped Astra approval to begin implementation.
The first multi-batch test assertion omitted the fixture's completed generation
job; corrected to verify 35 paused synthetic jobs plus the preserved completed job.

## Review policy

User requested on2026-10-20: independent critical-node reviewers must explicitly
use `gpt-6-astra`, reasoning `high`, forked with fresh context, read-only. Primary
agent owns all edits and requests correction review. Record requested tool model,
scope/commit, findings and verified dispositions; do not label earlier Sol reviews
as Astra. Review this design now, then implementation+drill, critical migration/
concurrency changes, incremental/change-repair design+implementation, final release.
