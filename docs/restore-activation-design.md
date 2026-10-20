# Restore activation and rollback — implementation plan

Status2026-10-21: proposed, awaiting independent Astra design review. The archive,
validation and restricted startup primitives exist; this activation module and
its HTTP/UI integration are not implemented yet.

## Interface and operational scope

One restore module owns candidate preparation, arming, startup recovery and
rollback. The server receives safe summaries plus an opaque candidate ID and
content revision; it never passes arbitrary destination paths to the module.
Startup calls the module after acquiring the persistent data-directory process
lock and before opening Store, providers, engine, authentication or HTTP.

Activation initially supports Linux local filesystems (the existing NAS Docker
deployment). It requires working file and directory fsync plus same-filesystem
rename; unsupported durability operations fail before live replacement. Windows
supports export/isolated validation and kernel lock tests; activation stays
unavailable until its directory durability semantics have separate evidence.
The process lock excludes cooperating YoLink processes. Existing old versions
must be stopped before deploying the first lock-aware version.

## Candidate preparation and confirmation

1. Authenticated/same-origin operation with current-admin reauthentication, a
   distinct backup passphrase and explicit overwrite confirmation. One CPU-heavy
   backup/import operation at a time; reject competing requests and rate-limit
   authentication failures. Passwords stay out of URLs, settings, logs and plans.
2. Fresh private candidate below a fixed data control directory, same filesystem
   as active data. Decrypt, validate manifest/schema/rows/key consistency, migrate
   candidate only, quarantine all historical work, materialize an administrator
   verifier if only an initial-password file exists, and remove that plaintext
   fallback from the candidate. The encrypted archive remains unchanged.
3. Force a candidate WAL checkpoint, check its busy/log/checkpointed counters,
   switch the final staged database out of WAL mode and close all SQLite connections. Reject any
   surviving WAL/SHM in the final new inventory. Revalidate the current schema,
   hold marker and typed quarantine postconditions, then hash all managed files.
   Fsync every final file after migration/verifier conversion, then fsync every
   newly created directory bottom-up through the control directory and data root.
   Only after that ancestry barrier may pending be published. Apply the identical
   barrier to the old rollback baseline before prepared publication.
4. Return counts, timestamp, format/schema versions, required temporary disk
   space, effect on automatic rules/events/deliveries and the content revision.
   Operator confirms that exact revision before a pending plan is armed.
5. Persist pending plan by bounded strict JSON in a checksummed envelope; fsync
   file, atomic rename, fsync directory. Its names are generated IDs, not user
   paths. Validate both content revision and actual candidate hashes on startup.
   A corrupt/unknown plan fails closed before Store initialization.

## Managed set and preservation

Only database, credentials/playback/events keys, administrator verifier/fallback,
and allowlisted files under plans/ and strm-backups/ can be replaced. The old
rollback set additionally includes the stopped database's WAL/SHM. Top-level
unrelated files, compose/env, media and all output mounts remain untouched.
Inventory contains relative names, byte sizes and SHA256, with case/path/type,
file-count and total-size bounds. Reject links or special files in every parent
and leaf. Work through rooted directory handles; ownership/mode0700 and the
kernel process lock are part of the operational assumptions.

Current managed files absent from the candidate must be removed from the active
set during replacement, while retaining their immutable rollback copies. Files
originally absent are removed on rollback. Track created directories so empty
directories can also be removed without recursive deletion of unrelated data.
All existing maintenance plans and recovery STRMs remain in the old rollback set.

## Durable states

The control directory retains one active journal and per-session candidate,
rollback and displaced-file areas. Journal uses a versioned, bounded,
checksummed envelope and explicit inventories. Every state publication uses
file fsync, atomic rename and directory fsync. No database connection opens
while an unresolved replacement/rollback state exists.

| State | Durable evidence | Startup behavior |
|---|---|---|
| pending | Validated candidate revision and immutable inventory | Verify; create complete rollback copy of stopped live managed files |
| prepared | Complete old inventory/copies fsynced and verified | Verify both inventories; publish installing before any live modification |
| installing | Complete rollback baseline retained, replacement may be partial | Always roll back before normal startup, even if new files appear complete |
| installed | Entire new managed set installed, dirs synced, hashes/postconditions verified | Open new Store restricted; retain rollback baseline |
| rollback_pending | Manual rollback requested, previous runtime state retained | Capture complete stopped current managed set including DB/WAL and new files into a separate post-state area |
| rollback_prepared | Verified, fsynced post-state inventory plus immutable old baseline | Allocate rollback reserve, then publish rolling_back before touching active files |
| rolling_back | Immutable old baseline and original-absence map retained | Reconstruct old managed set idempotently; no login/new-DB open needed |
| rolled_back | Old set verified and synced | Open old Store restricted; leave audit and retained evidence |
| released_new / released_old | Review completion and operation identity durably recorded | Open the corresponding mutable runtime, with no replay of the install/rollback operation |

Installed/rolled_back explicitly hand off to a MUTABLE runtime. Their sealed
inventories are historical evidence and rollback inputs, never ongoing hash
invariants for active DB/auth files. Later startup must not reinstall or roll
back a terminal session just because normal settings/password/diagnostic writes
changed those files. Startup reads an external hold and locally generated
maintenance generation from the journal BEFORE openStore via its startup options.
This hold applies both after installation and rollback, even if the old database
has no restore marker. Terminal-session identity is retained when a later restore
is armed, and is not reused as a new operation.

Preparation can be retried or cancelled before installing without touching active
files. Disk failure while creating rollback never arms installing. If a failure
occurs after installing publication, return an error, retain the journal and let
the same startup recovery path restore old data. No opportunistic continuation
into Engine with a mixture of files is permitted.

## Replacement and rollback details

Before installing, copy the complete stopped old managed set into immutable
rollback storage and verify its digest; the old DB/WAL are copied as one closed
set. Check free-space budget for candidate + old rollback + replacement scratch.
For every new file, copy into fresh same-filesystem scratch, fsync and verify it,
then replace its allowlisted active name. Record/sync directory creation and
deletions as part of the session. Original old files removed/replaced are already
recoverable from the verified immutable baseline.

Before publishing installing, physically allocate and fsync a rollback reserve
for the entire old payload plus bounded journal/metadata overhead; a sparse
truncate is insufficient. If allocation is unsupported or fails, reject activation
before live replacement. Recovery releases this reserve before copying old files
back. This provides space for the rollback even when the install hits ENOSPC;
device errors, quotas or another writer consuming that space still fail closed
with the complete baseline retained. Remove the installation reserve only after
installed is durable; manual rollback allocates its own reserve before its first
mutation. Include reserve/copies/scratch in the disk-space UI estimate.

Rollback never consumes or renames away the sole immutable old baseline. For
each old file it builds verified scratch then atomically installs a copy; every
name originally absent is removed from active. Preserve changed post-restore
data in a separate session area before a manual rollback. Its complete closed
DB/WAL, keys/auth and all current managed files (including newly created plan
and recovery files) must have a verified inventory plus bottom-up fsync barrier
published as rollback_prepared BEFORE rolling_back. A capture crash leaves the
current active set intact and retryable; no partial capture is a recovery copy.
A retry after process
death must handle existing scratch/displaced files using verified identities,
not assumptions about whether the previous rename completed. Unknown files,
hash changes or journal corruption fail closed and retain all recovery material.

After installed publication, automatic workers remain suppressed by the durable
restore hold. A separate authenticated review screen allows safe local inventory,
login and explicitly initiated read-only diagnostics. Any output repair, job,
historical maintenance apply/rollback or external event requires completion of
review. Finishing review re-quarantines historical work and persists the cleared
DB hold first; then it durably publishes released_new/released_old to clear the
external hold. A crash between those writes still starts restricted. A controlled
restart starts workers. Automatic rules/events stay disabled until individually
re-enabled. Every restore/rollback generates a fresh local maintenance generation
outside the archive. Newly saved previews carry it; every maintenance apply and
rollback path compares it before dispatching to links/migration/inplace code.
Historical plans remain readable, but cannot execute after the hold clears.

## Failure evidence required before exposing activation

- Real Linux no-network process startup, stable credentials/signatures/login,
  zero provider/output/notification replay and unchanged original output files.
- Process death and injected errors before/after each journal publish, copy,
  file fsync, directory fsync, rename and removal; repeated startup convergence.
- Wrong passphrase/tamper/truncation/future schema, migration rollback,
  oversized/ambiguous inputs, stale revision/corrupt plan and cancellation.
- Full disk, read-only directories, incomplete old snapshot, missing/changed
  rollback file, originally absent managed file, WAL recovery, and retained
  unrelated/recovery files. Corrupt baseline must block startup.
- Two simultaneous starters and killed lock-holder takeover on Linux; verify
  every path keeps Store/Engine closed until recovery completes.
- Activate, mutate public-base/password/diagnostic settings, release review and
  restart twice: no false hash-corruption, replay or unintended rollback.
- Manual rollback with old queued jobs/pending deliveries stays held; capture
  post-state fully before any replacement, including new plans and changed auth.
- Submit an old plan's apply/rollback URLs after review completion: require a new
  preview despite an identical public base. Verify fresh previews still execute.
- Namespace/process tests using tmpfs are not persistent-filesystem power-loss
  evidence. Run the journal durability/fault matrix on a private local disk mount.
- Independent Astra review of the finalized design, implementation and fault
  drill evidence, followed by correction verification. All release gates remain.
