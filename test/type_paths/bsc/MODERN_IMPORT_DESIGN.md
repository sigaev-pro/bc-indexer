# Modern private BSC import fixture

Status: design; execution, initialization and import proof are NOT_RUN.
Base: accepted PR185 (`6cb31bcc498f054559a37fa7bc39456efc423e4e`).
This fixture must not turn the accepted decoder/classifier component result into
an execution claim. Issue #21 remains open.

## Native preconditions

Use the accepted patched BSC source and capture profile from #19, separately
from the original-source Parlia component binary. Install a new test in
`extractor/bsc/decision`, reusing the accepted decision test helpers unchanged.
A probe-local node constructor can attach `core.Config.VmConfig.Tracer` while
retaining the same native extractor hook and system-call backend.

The private chain may activate forks at private heights/times, but its config
must activate the complete applicable BSC fork sequence through Pasteur, pass
native `CheckConfigForkOrder`, and retain Jenner as future. Record every fork,
chain ID, header time, genesis allocation and validator key. Do not enable
modern transaction rules while preserving older consensus/system rules.

Use the real modern extra-data layout: validator count, validator address and
BLS vote address, turn length, and seal. Keep the target blocks away from an
epoch boundary, a finality-reward interval and the UTC breathe-block boundary.
These branches remain explicit uncovered rows, not implicit deferrals.

## System-state initialization gate

Pinned `core/systemcontracts/upgrade.go` selects upgrade maps by genesis hash.
A private genesis uses `defaultNet`; modern upgrade maps have no default entry.
Consequently a modern fork schedule over the old admission genesis silently
retains old contract code and cannot establish this proof.

Construct an explicit private modern system allocation from the pinned native
upgrade code constants, retaining each address, latest applicable code source,
upstream commit provenance and digest. Bootstrap initialization in a separate
fixture-construction phase with a fully recorded input state and ordered native
calls. Validate initialized storage and contract queries before freezing the
final genesis. Do not mutate the global system-contract genesis identity to
masquerade as mainnet. Do not replace modern system contracts with toy bytecode.

The difficult prerequisite is independently specifying initialized storage:
the pinned BSC archive includes bytecode but no corresponding Solidity sources
or modern private genesis. If initialization assumptions cannot be verified,
retain the failed attempt and report this gate as blocked; do not emit an
apparently successful corpus from empty storage or no-code accounts.

## Import and independent expectations

Generate and seal native Parlia blocks, import their RLP into a fresh captured
database, then reopen/reimport using the accepted lifecycle helpers. Include
successful and reverting user transactions of types 0 through 4; type 3 needs
real valid blob sidecars and type 4 needs signed nonempty authorization lists.
The accepted component's decoder-only envelopes cannot be reused as executable
fixtures. Assert native type/hash, receipts, ordering, state and observation
events against fixture expectations.

Capture system transactions during real `InsertChain`: `OnSystemTxStart`
selects the row; `OnTxStart` exposes the live VM state; `OnTxEnd` observes the
post-call state after native nonce increment. Record balances, nonces, selected
contract storage, receipt status, logs and wire identity for each transaction.
Compare to an independently specified transition for that method, not the
generator's output. In particular the validator's nonce increases by one and
the system call charges no ordinary gas fee. Reward distribution transfers
funds to coinbase before the system transaction starts; the before-state must
therefore be read at the hook, not inferred from the preceding user receipt.
Deposit storage/balance effects depend on the verified modern contract state
and must be derived before expected values are frozen.

Node-ID maintenance calls in `stakehub.go` are outside these non-epoch import
paths. D106 is not a blanket exemption for modern transaction/system-state
proof. Any unsupported system call actually encountered during capture is a
reported blocker, not silently skipped. Public profiles remain NOT_RUN.

## Execution arrangement

Keep all new probe/driver/fixture files under `test/type_paths/bsc/`, with any
adapter-specific projection under `adapters/bsc/type_mapping/native/`. Evidence
updates use the existing BSC evidence document. No shared helper edits needed.
Use a separate preparation directory and the accepted immutable source archive,
compiler image and read-only module cache; preserve original-source component
artifacts. Reuse cache inputs, never overwrite another run's binary or logs.

First native attempt should only validate config, modern allocation and ordered
initialization; compile and run only after the coordinator grants the exclusive
slot and after checking available disk. Freeze and commit that attempt's source
before preparation. Full import follows only when initialization is explained
and reproducible. Every attempt retains commands, source heads, exits, hashes
and cleanup status. No long/load runs.
