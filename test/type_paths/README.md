# Native type-path inventory (E01-05, block 0)

This block records the native transaction paths in the accepted Geth, BSC,
and Bor source archives. It is a source inventory and a work queue for the
native fixture blocks, not a production transaction decoder or an admission
gate. It does not change the shipped adapters. Every runtime cell remains
NOT_RUN. Issue #21 and G.E01 remain open.

Each client's `adapters/<client>/type_mapping/paths.json` pins its native
commit, original source archive, relevant source files, and path inventory.
`verify.py` rejects missing/unknown inventory rows, unsupported native type
lookups, source drift, and attempts to grant runtime credit from this source
check. It reads archive members without extracting them.

Run the portable checks from the repository root:

```sh
python3 test/type_paths/test_type_paths.py
python3 test/type_paths/verify.py check
python3 test/type_paths/verify.py source geth --archive /path/to/geth/source.tar
```

Repeat `source` separately for `bsc` and `bor`. A source check is successful
only against that client's pinned archive. A successful source check prints
`SOURCE_VERIFIED` and `runtime: NOT_RUN`; it never issues a native or public
profile PASS. No source archive is downloaded by this tool.

## Native proof still required

- Native import, observed transaction bytes and boundaries for each allowed
  variant, successful and failed execution, fork-boundary acceptance and
  rejection, and an unknown-type rejection with no skipped delivery.
- Derive type applicability from the native rules of the concrete block and
  pinned network/profile, not the current head or a byte type alone. A
  decodable type is not proof of activation on a network.
- BSC: native Parlia classification positive and negative controls; ordinary
  versus system execution; before/after state for each system transaction
  on current-fork fixtures (D103). Preserve the actual wire type and payload.
- Bor: mandatory post-Madhugiri body StateSync, native array encoding and
  each system transaction's before/after state; pre-Madhugiri synthetic
  receipts remain a distinct optional record, never body transactions.
- ETH: post-merge execution, plus the four protocol-call paths assigned by
  D102. Their outside-transaction effects belong to E04-02.optional_scope.
- All seven public network/profile cells require their own proof; no private
  fixture, source check, or another network's result supplies it.

The accepted smoke corpus (#19, PR #169) supplies older-fork fixture inputs
and independent expected values. Its passing results are not relabeled as
type-path proof. E04-03.state_diff retains full native state-diff semantics;
E01-05 must still prove the current-fork transaction boundaries assigned by
D103. Generated schemas and wire validators belong to E02.
