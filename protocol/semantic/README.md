# semantic (E02-01.semantic_descriptors, #22)

`protocol/semantic` is the manifest semantic identity of the current
delivery: the `SemanticDescriptor` of one public event and the
decision-to-batch `BatchAllocation` of a native decision. The semantic
manifest fixes the event kind, the source provenance and sequence, the
activation refs and the revisions **before DELIVERY_STAGING**; delivery
attempts never change them ([CONTROL_WIRE_CONTRACT](../../.project/docs/CONTROL_WIRE_CONTRACT.md),
"Базовые кодирования и идентичности"; D79 in
[MESSAGE_CONTRACT](../../.project/docs/MESSAGE_CONTRACT.md) section 4).

Task card: `E02-01.semantic_descriptors` in
[.project/docs/TASK_DECOMPOSITION.md](../../.project/docs/TASK_DECOMPOSITION.md);
split: stream A of
[.project/evidence/E02-parallel-plan-20261121.md](../../.project/evidence/E02-parallel-plan-20261121.md).

The package imports only the Go standard library and is its own module,
`github.com/sigaev-pro/bc-indexer/protocol/semantic` (the path convention
of `core/capture`).

## What this block does and does not do

It defines and checks the identity contract: registry values (family v1:
tx=1, block=2, balance.system=3, bor.legacy_receipt=4; kind: applied=1,
removed=2, status=3), the activation identity
(`activation_id = native_decision_sequence || entity_ordinal`, qualified by
producer and run; block ordinal 0, transaction body index+1, optional
entities after the native body count), the revision rules the contract
states (Applied 0, StatusChanged > 0), the source provenance
(`node_instance_id`, `writer_epoch`, `source_sequence` as a separate
durable counter), the 40-byte `batch_id` (`producer16 || run16 ||
batch_sequence`, counter starts at 1), deterministic SHA-256 digests over a
canonical encoding, and the exactly-once allocation model.

It does not persist anything: the durable decision-to-batch commit, the
attempt allocation `{attempt_generation, event_sequence_base,
event_count}`, the READY manifest and the producer store are E03-01
 .producer_store. The event descriptor v2 of the READY manifest
(record digests, fragment counts) is E02-02/E03 territory; it sits on top
of these identities. The lifecycle transition rules (reordering, revision
conflicts, reinclusion) are E02-03.lifecycle_validator. This block runs no
native client and gives no AC verdict.

## Design

### SemanticDescriptor

```
SemanticDescriptor = { family, kind, network{chain_id, genesis_hash},
                       activation{producer, run, decision_sequence,
                                  entity_ordinal, block_hash, tx binding?},
                       revision, source{node_instance, writer_epoch,
                                        source_sequence} }
```

The activation ref is the `BlockActivationRef` of a block event or the
`ActivationRef` of a transaction event with its inclusion binding (native
body position and transaction hash), per MESSAGE_CONTRACT section 2. The
network family is EVM only; the digests encode it as byte `0x01`, as the
network digest formulas of CONTROL_WIRE_CONTRACT do.

A new application of a branch has a new decision sequence, so two
activations of one branch never share an `activation_id`: one
`(run, block_number, block_hash)` is not enough for two activations of one
branch (MESSAGE_CONTRACT section 5). The descriptor carries no attempt,
delivery or transport field, so a retry before READY — which may change
only public event IDs and the envelope attempt — cannot move the semantic
identity, and after READY the IDs and bytes are immutable.

### Digests

SHA-256 over a domain tag, one zero byte and the fields in a fixed order;
unsigned big-endian integers, raw identity bytes. `u16be`/`u64be` are
unsigned big endian; `entity` is 0 for the block entity, 1 for a
transaction entity (followed by its inclusion binding) and 2 for an
optional entity.

```
descriptor_digest = SHA256(UTF8("extractor.semantic.v1") || 0x00 ||
  u16be(family) || u16be(kind) ||
  0x01 || chain_id32 || genesis32 ||
  producer16 || run16 ||
  u64be(decision_sequence) || u64be(entity_ordinal) || block32 ||
  entity || [u64be(native_position) || tx32] ||
  u64be(revision) ||
  node_instance16 || writer_epoch16 || u64be(source_sequence))

allocation_digest = SHA256(UTF8("extractor.allocation.v1") || 0x00 ||
  producer16 || run16 || u64be(decision_sequence) || batch40)

batch_id = producer16 || run16 || u64be(batch_sequence)   // 40 bytes
```

`SemanticDescriptor.Digest` and `BatchAllocation.Digest` validate first and
refuse to digest a malformed value, in the style of the E01-02 decision
digests. The digest binds every identity field; the tests flip each field
once.

### Batch allocation (D79, exactly once)

One native decision with public events creates exactly one delivery batch
with the full ordered set of semantic descriptors of that decision. A
decision without public events records a decision checkpoint and allocates
nothing. WriteBatch/RPC limits never split a semantic decision into
additional batches.

`Allocator` is the in-memory model of the durable binding of one producer
run:

- `EnsureBatch(decision)` allocates the next batch sequence on the first
  call and returns the stored batch on every later call for the same
  decision (the repeated-marker path), so a retry restores the previous
  binding instead of allocating a second batch. The caller allocates only
  for decisions with public events.
- `AdoptBatch(decision, batch)` restores a saved binding after a restart.
  Adopting the same batch again is idempotent. A different batch for a
  decision that already has one is `ErrReallocation`: a decision keeps the
  batch first allocated to it. A batch of another producer run is
  `ErrIdentity`. The counter moves past an adopted sequence, so no batch
  sequence is ever reused.
- Counters are checked u64: the batch counter starts at 1, overflow is an
  explicit error, never a wrap. The native decision sequence starts at 0,
  as the E01-02 decision bridge records it.

## Digest vectors and fixtures

The expected digests in `semantic_test.go` come from an independent
Python implementation of the formulas above (`hashlib` only), in the
discipline of `test/harness/decision.py`; neither reads the other's code.

Per-family fixtures, with the retained E01 evidence each value comes from:

| Fixture | Evidence |
|---|---|
| ETH, chain 31337, genesis `5bce29fa…d3e98d`, IMPORT over the empty block 2 `7a977bf1…7e98a79` | `test/native_faults/eth/sample_report.json` (chain, genesis, decision, block); producer `producer-1.run-1` and store `native-store-1` from the retained unit vectors `…/runs/d95-20261107/e01-02-decision-unit-04-b21064f` |
| BSC, chain 714, genesis `16d03930…41e77`, IMPORT decision 1 over block 2 `5f7d8050…88d6` (intent `0ae1c83a…`, decided `556fdf70…`) | `test/harness/fixtures/bsc_decision/report.json` (the measured run-02 report; producer `bsc-probe.run-1`, store `0123456789abcdef0123456789abcdef`) |
| BSC tx entity, chain 714, genesis `2151f9f5…f17e`, IMPORT decision 0 over block 1 `621ceaf8…c445e`, body tx 0 `7817b17d…47502` | `test/native_faults/bsc/sample_report.json` (`bsc.skip-block` handoff: decisions block 1, block 2; block 1 body has 12 transactions) |
| POLY, chain 15001, genesis `824c042c…9848`, IMPORT decision 3 over block 3 `50c0bd3e…a127` | retained Bor decision run 03 `…/runs/d95-20261107/e01-02-decision-bor-run-03-10f90e9` (producer `bor.run-1`, store `0123456789abcdef0123456789abcdef`) |
| POLY StatusChanged, revision 1, decision 7 (later decision of the same run) | same run; a contract example of a status snapshot, not a measured record |

The E01 evidence predates the public `Id16` identities, so the fixtures
derive test identities: `Id16 = SHA256(name)[:16]` for string names
(producer, run, writer epoch), and the literal 16 store bytes where the
evidence carries them (`0123456789abcdef0123456789abcdef`). This
derivation is a test-only mapping, not a production rule. No E01 record
carries a writer epoch or a source sequence; the fixtures use documented
test values for them.

The ETH decision sequence 0/2 shape of `TestEnsureBatchAllocatesOnce`
follows the retained geth decision run (an IMPORT, then a FINALITY
checkpoint without events, then a CANONICAL decision with events).

## Checks

`go test ./...` (also with `-race -cover`) is portable and offline:
digest vectors against the independent values, digest determinism, retry
stability with every identity field flipped, registry and revision
validation, exactly-once allocation, adopt/restore, conflicting
re-allocation, and batch identity rules. `gofmt -l` and `go vet` are clean
(go1.27.1 darwin/amd64).

## Open questions

1. **Revision of Removed events.** MESSAGE_CONTRACT section 5 fixes
   Applied at revision 0 and every StatusChanged at a positive revision,
   and CONTROL_WIRE_CONTRACT fixes "revision Applied=0, updates>0", but no
   document states whether a Removed event carries a revision and which
   values are legal. `Validate` currently accepts any revision for
   `KindRemoved`. Decision needed for E02-03.lifecycle_validator; until
   then this package deliberately enforces nothing.
2. **Public Id16 mapping of E01 string identities.** The contract requires
   cryptographic-RNG `Id16` identities created with the adapter store; the
   retained E01 runs predate them and name producers, runs and stores with
   strings. The fixture derivation above is test-only. The production
   mapping (where the E03 producer store mints these identities) is not
   written down yet and belongs to E03-01.
