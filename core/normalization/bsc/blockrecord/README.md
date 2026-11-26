# blockrecord

`core/normalization/bsc/blockrecord` builds the public BlockApplied event of
one decided BSC block import (C2 of the live capture plan): the typed
`block.BlockRecord` with its BSC finality markers, the semantic descriptor
fixed before DELIVERY_STAGING, the canonical RecordFragment bytes of the
logical Envelope, and the READY descriptors of the manifest.

One call does all of it:

```go
func BuildBlockEvent(in Input, eventID proto.EventID) (BuiltEvent, error)
```

`eventID` is `producer_id16 || run_id16 || event_sequence` and must carry
the producer and the run of `in`. The result is a pure function of `in` and
`eventID`: the same pair always builds the same bytes, and a retry with
another event id leaves the semantic descriptor unchanged. One decision is
one batch with one event, ordinal 0, family block, kind applied, entity
ordinal 0, revision 0.

The package imports `core/normalization/bsc/block`, `protocol/block`,
`protocol/semantic`, `protocol/proto` and `protocol/codec`, and the standard
library only.

## What the caller does with the result

- `BuiltEvent.Fragments` go to `WriteAttemptData`, concatenated in index
  order.
- `BuiltEvent.Event` and `BuiltEvent.Frags` go to
  `codec.ManifestBuilder.AddEvent`.
- `BuiltEvent.Semantic` and `BuiltEvent.Record` stay with the attempt; a
  STAGING retry keeps the descriptor and gets new event ids.
- `proto.ValidateEnvelope(0, &proto.Envelope{...})` over the semantic
  descriptor, the fragments and the record returns exactly `Event` and
  `Frags` (tested).

The fragments own their bytes. The record shares the header bytes of the
input (`extra_data`, `source_rlp`), which the collector captured as owned
values.

## The input

Every fact arrives as an owned value, captured on the native goroutine.
The table says where each field of `Input` goes.

| Field | In the event | Checked |
|---|---|---|
| `Network` | Envelope `network`, activation `network`, semantic network | family `EVM`, chain id and genesis hash non-zero, a profile registered for the chain id |
| `NodeInstanceID`, `WriterEpoch`, `SourceSequence` | Envelope `source`, semantic source | non-zero; `SourceSequence` from 1 |
| `ProducerID`, `RunID` | Envelope, activation, admission, native decision, semantic activation | non-zero, equal to the halves of the event id |
| `DecisionSequence` | activation id, native decision, semantic activation | none beyond the record: 0 is a value |
| `ObservationSequence` | admission | none beyond the record: 0 is a value |
| `Header` | metadata header | source RLP present, block hash non-zero, millisecond timestamp without overflow, parent hash non-zero above the genesis; number and hash equal to `NewHead` |
| `TxCount`, `SystemTxCount` | `source_transaction_count`, `parlia_system_transaction_count` | `SystemTxCount` nil gives no node |
| `OldHead` | nowhere | hash non-zero |
| `NewHead` | attested `snapshot_head`, `observed_execution_head` | hash non-zero |
| `Justified` | nowhere | hash non-zero |
| `Finalized` | `effective_finalized` | hash non-zero |
| `Attestation.Status` | selects the markers | `ATTESTED` or `GENESIS_FALLBACK` |
| `Attestation.Justified`, `Attestation.Finalized` | attested `justified`, attested `finalized` | hash non-zero |
| `Attestation.Digest` | nowhere | 64 lowercase hex digits |

`OldHead`, `Justified` and `Attestation.Digest` are facts of the decision
evidence that `block.BlockRecord` has no field for. The builder checks that
they are present and publishes nothing of them.

Optional header fields follow native pointer presence. A present zero (a
pre-London base fee, a zero parent beacon root) stays a present node; an
absent field is never a zero node. The builder derives no fork rule.

## Finality markers

`markers.go` projects the decision evidence and the attestation read onto
`block.BscMarkers`; it derives no finality of its own and invents no
finalized anchor.

| Attestation status | `attested` | `effective_finalized` | `effective_basis` |
|---|---|---|---|
| `ATTESTED` | `AttestedSnapshot{snapshot_head: NewHead, justified: Attestation.Justified, finalized: Attestation.Finalized}` | `Known(Finalized, NATIVE_LIVE)` | `HEADER_SNAPSHOT` |
| `GENESIS_FALLBACK` | absent | `Known(Finalized, NATIVE_LIVE)` | `GENESIS_FALLBACK` |

BSC has two native finality surfaces. The attested snapshot is the
header-derived vote attestation, so it carries the anchors of the
attestation read. The effective finalized reference is what the decision
step observed, which the VotePool may have advanced beyond the attested
source. When the two differ, the record keeps both and the basis stays
`HEADER_SNAPSHOT`, as the plan fixes it; the `VOTE_POOL_QUORUM` basis needs
the early vote evidence of E05-01.bsc_finality and is not claimed.

## The tree

`tree.go` maps the typed model onto the Envelope tree of the logical schema
registry, field by field. The tag, the kind, the message type key, the enum
and the exact byte length of every node come from the registry. A field the
registry does not name, a field mapped with another kind, bytes of another
length or an unregistered enum value stop the mapping with a named error,
so a node outside the registry cannot be built. The first failure stops the
mapping.

The tree holds the Envelope fields of MESSAGE_CONTRACT section 2 and, under
the payload variant `block_applied`, the record: activation, admission,
metadata and the initial snapshot with the BSC markers.

## Errors

A missing or invalid mandatory fact is a rejection that wraps
`block.ErrInvalidValue` and names the fact. A wrong event id and a network
without a registered profile wrap `proto.ErrInvalidValue`; the latter also
wraps `proto.ErrClosedRegistry`. A rejected input returns no event. The
rejections of `bscblock.Project` keep their own sentinels
(`ErrEmptySourceRLP`, `ErrMilliOverflow`) under `block.ErrInvalidValue`.

## Decisions on points the plan leaves open

1. `ObservedExecutionHead` of the initial snapshot is `NewHead`: the head
   after the import.
2. The attested snapshot takes its anchors from the attestation read, not
   from the justified and finalized heads of the decision evidence. The
   plan names the heads without saying which read; CURRENT_SEMANTICS
   defines the attested snapshot as the merged VoteData, and the full
   evidence model of `adapters/bsc/finality` keeps the header-derived
   finalized anchor apart from the native effective result.
3. The builder does not check that `OldHead` is the parent of the header
   or that the heads of the decision evidence agree with the attestation
   read. The plan lists no such check, and a false refusal would stop the
   node on a correct import.
4. `Attestation.Digest` is checked for its form only. Its formula belongs to
   the native tree, which this package cannot import.
5. Only the BSC Chapel testnet (chain id 97) has a registered profile. A
   record for another chain id is refused instead of being published under
   a borrowed profile. BSC mainnet needs a registry entry.
6. The profile strings are fixed in the registry. The client commit is the
   pinned commit of the build; the builder has no input for the
   `native_commit` the node reads from its configuration.
7. `DecisionSequence` and `ObservationSequence` start at 0 in the native
   tree, so zero is a value for them and is not checked.

## Checks

Portable and offline, in `core/normalization/bsc`:

```sh
go test -count=1 -race ./blockrecord/
```

The tests cover the READY descriptors, the round trip through
`codec.ReadRecord`, the agreement with `proto.ValidateEnvelope`, one
negative control per mandatory fact, the optional header fields present
and absent, determinism across attempts, the finality markers of both
attestation statuses, and every refusal of the tree writer. The golden
vectors of `test/harness/fixtures/blockrecord` come from an independent
implementation: the digests, the fragments and the registry tables.

`python3 -I test/mutate.py core/normalization/bsc/blockrecord/blockrecord.mutants.json
--output <new directory>` runs the single-check mutants of the package. Six
mutants are labeled equivalent before the run: each ignores an error that no
input reaches, because the validators repeat what earlier checks made.
