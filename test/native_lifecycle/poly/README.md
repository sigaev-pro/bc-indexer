# POLY reorg and lifecycle evidence (E05-02.reorg)

`test/native_lifecycle/poly` is the POLY slot of E05-02.reorg (#55): the
reorg, reapply, known-branch, reordered-event and StateSync lifecycle
evidence of the Bor adapter on an in-memory chain, the real E03-01
producer journal (`core/journal/producer` over the memory storage seam)
and the real E05-01 Polygon finality markers
(`adapters/bor/finality_observer`). It is test evidence over the composed
seams, not a production consumer; the ETH and BSC slots are their own
blocks.

## What each test proves

| Test | Case |
|---|---|
| `TestReorgOneDecisionOneBatchCrashAndRepeat` | AC-CAP-08-02, POLY: one native reorg decision replaces several blocks; the manifest is built from bounded chunks; a power loss inside the build (every write from a chosen one on fails atomically) leaves the batch below READY with no lifecycle change; the repeat of the same native marker over the reopened journal restores the one former batch_id with the full ordered Removed/Applied set — replaying the marker returns the same batch and the next decision consumes exactly the next sequence, so the events are never split into per-block batches; the applied snapshots carry the real Polygon milestone marker |
| `TestRemovedRegardlessOfPubAck` | A READY Applied activation gets its Removed event whether its batch's PubAck fully arrived (SEALED + verified IngestAck) or never arrived; the reorg batch completes READY while the unacked batch keeps pending delivery |
| `TestReapplyNewCanonicalPeriod` | A block removed by a reorg that returns on a longer known branch opens a **new** canonical period under the new decision (`block.NewCanonicalPeriod`); the old period stays terminal (`ErrTerminal` on any direct event), each period keeps its own immutable record |
| `TestReorderedEventsOneResult` | Reordered delivery converges: StatusChanged events before the Applied fold through PENDING_APPLIED to the same ACTIVE state, revision and latest full evidence as the in-order delivery; a stale lower revision is `ErrStaleRevision`, an equal revision with different evidence is `ErrRevisionConflict` |
| `TestStateSyncLifecycle` | Sprint-end blocks carry their observed StateSync count and `END_OF_BLOCK_COMMIT` mode (plain blocks carry none); a PubAcked sprint end still gets its Removed on reorg; the reapply opens a second period and the removed record stays byte-identical; the promoted replacement sprint end observes its own StateSync work |
| `TestSideChainNoLifecycleChange` | A side block stored without a head change creates no activation and moves no head; a milestone whitelist anchored on the side branch is refused by the observer's canonical check (`ErrInvariant`); promoting the known branch is one decision with exactly the Removed + Applied pair |

## Fixture

`driver.go` holds the in-memory Bor chain (canonical chain, side branches,
sprint length 4 with the observed StateSync work at the sprint ends) and
the crash-injecting storage wrapper; `sources.go` the pinned
sources_file and the two supporting-source seams of the observer fixture
(the L1 GC lookup itself is E05-01.polygon_observer's evidence, exercised
in its own module); `lifecycle.go` the driver: one import decision per
block, one reorg decision per canonical change (Removed events for every
live activation of the removed suffix — ascending — then Applied events
for the promoted path, reapply through a new canonical period), the
kind-dependent manifest descriptors, the chunked commit through the real
journal (allocation, attempt, data and manifest chunks, the closing READY
commit) and the fold into the `protocol/block` lifecycle automaton that
runs only after READY.

Go module `github.com/sigaev-pro/bc-indexer/test/native_lifecycle/poly`
(`go 1.25`), standard library plus the four repository-local modules
(`protocol/block`, `core/journal/producer`, `core/journal/storage`,
`adapters/bor/finality_observer`), all wired with `replace` directives;
memory storage only; `gofmt`/`go vet`/`go test -race` clean. The runtime
native verdicts (E01/E05 harness) stay NOT_RUN; this module is the
composed-seam evidence of the POLY slot.
