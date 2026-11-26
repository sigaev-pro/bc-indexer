# Block event golden vectors

Golden vectors of the BSC block event builder
`core/normalization/bsc/blockrecord`: one fixed input built from a real
Chapel block, and the semantic digest, record digest and fragment digests
that an independent implementation computes from it. The Go test
`vectors_test.go` of the builder reads `expected.json` and fails when the
builder produces anything else.

Files:

| File | Content |
|---|---|
| `chapel-block.json` | The stored responses of the public Chapel endpoint for one block, its parent and grandparent, and the genesis hash |
| `generate.py` | The independent implementation that writes `expected.json` |
| `expected.json` | The input, the registry tables and the expected digests |

## The block

The endpoint is the public RPC that `configs/bsc-testnet.yml` names for
verification: `https://bsc-testnet-rpc.publicnode.com`. The endpoint is used
for fixture acquisition only; the product reads its data from the capture
node.

| Fact | Value |
|---|---|
| Acquired | 2026-11-25T14:16:41Z, four blocks below the head 144802472 |
| Chain id | 97 |
| Block | 144802468 |
| Hash | `0xf69fc5269ddd1fa621fcecb81be49e76e1fcdd2ff6a0e88cd4883901454c620f` |
| Genesis hash | `0x6d3c66c5357ec91d5c43af47e234a939b22557cbb552dc45bebbceeed90fbe34` |
| Transactions | 3: two ordinary and one Parlia system transaction |

The header is a block of the Jenner era. All six optional fork fields are
present in the native header, three of them as present zeros
(`baseFeePerGas`, `blobGasUsed`, `excessBlobGas`) and `parentBeaconBlockRoot`
as a present zero hash. Its timestamp follows the BSC millisecond rule: time
1795616202 times 1000 plus the low 64 bits of the mix digest (450) is the
`milliTimestamp` the endpoint reports.

## How the input is derived

`generate.py` builds the input of the builder from the stored responses:

- **Header.** Every field comes from the block response. An optional field
  is present exactly when the response carries it.
- **Source RLP.** `rlp(types.Header)` of the pinned BSC client, encoded from
  the response fields in the order of the client's generated encoder. The
  script proves the encoding: Keccak-256 of the bytes equals the block hash.
  The Keccak function is part of the script, as in the other harness scripts
  of the repository.
- **System transaction count.** The criterion of
  `core/normalization/bsc/classification.go` applied to the response: the
  recipient is a system contract, the gas price is zero and the sender is
  the coinbase. One transaction of the block meets it.
- **Network.** Chain id 97 in 32 big-endian bytes and the genesis hash of
  the genesis response.
- **Identities.** Test-only values: `node_instance_id`, `producer_id`,
  `run_id` and `writer_epoch` are the first 16 bytes of SHA-256 of a name.
  No live identity is in the fixture.
- **Sequences.** `source_sequence` 1, `decision_sequence` 12,
  `observation_sequence` 11, event sequence 1.
- **Heads.** The old head is the parent, the new head the block. The
  justified and finalized heads of the decision evidence are the parent and
  the grandparent.
- **Attestation.** Status `ATTESTED`; its justified and finalized anchors
  are the parent and the grandparent; its digest follows the attestation
  digest formula of `adapters/bsc/lifecycle`. These are fixture values: the
  attestation of this block was not read from a node.

## What the script computes

The script reads no Go code. It restates from the documents:

- the logical schema registry of schema_version 2: 25 messages with 113
  fields, 10 enums and the registered profile, from the README of
  `protocol/proto`;
- the logical Envelope tree of the block event, in depth-first preorder,
  with the mapping rules of the builder (MESSAGE_CONTRACT section 2 and 4);
- the D73 RecordFragment encoding: canonical varints and field keys, the
  TypedItem and BlobChunk tables, blob chunks of 16384 bytes, items packed
  into fragments of at most 65536 bytes, the header, the seal and the record
  digest (MESSAGE_CONTRACT section 6.1);
- the semantic descriptor digest (README of `protocol/semantic`);
- the manifest of the one-event batch (CONTROL_WIRE_CONTRACT, "READY manifest
  and reservation").

The only library it uses is `hashlib`.

## Expected values

| Vector | Value |
|---|---|
| Nodes, blobs, blob bytes | 99, 45, 2511 |
| Fragments | 4: header 137 bytes, items 2886, blobs 2926, seal 87 |
| Record length, content bytes | 6036, 5949 |
| Semantic digest | `d21df1dbe34a0de0d8d95d29ad41219129e76e3295b7f314c2aa11c0bbefadc2` |
| Record digest | `31262aaf5a6db88c9297a03df030eef0c25b60ea286d338133113c8c3f9443a2` |
| Fragment manifest digest | `033b550d90fef5ee3144bd9e61a2baf3059937a9a70940a34da3ff08d5ec31b3` |
| Manifest digest | `255e2b6c66126bc3b4241cfb6e258698291ee386fcf76e2f924bffe0bd285968` |

The Go test compares all of them, and fragment by fragment the encoded
length and the SHA-256, which makes the four fragments byte-identical.

## How the two implementations reached agreement

The first comparison found two defects, both in this script and none in the
builder: the script opened an intermediate message once per leaf instead of
once, and it wrote the optional header fields out of the tag order of the
registry. The script now asserts ascending field tags under a message, the
first rule of the D73 tree, so the second defect cannot return. The builder
was not changed after its first comparison with a correct tree.

## Commands

Regenerate and verify, from this directory:

```sh
python3 -I generate.py          # writes expected.json
python3 -I generate.py --check  # exits 1 when expected.json differs
```

The Go side, from `core/normalization/bsc`:

```sh
go test -count=1 -run TestGolden ./blockrecord/
```

## Limits

- One block, read once. The fixture pins this header; it is not evidence
  about other blocks or about a capture node.
- The attestation values and the identities are fixture values.
- The genesis hash was read from the endpoint, not copied from a document.
