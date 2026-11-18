# BSC native type-path fixtures

Part of E01-05.type_paths (#21). This first probe calls the pinned native
decoder, block-specific signer and Parlia system classifier. It covers the
five wire types, byte-preserving roundtrips, native signer rejection before
activation, every unknown typed ID from 5 through 127, and positive/negative
system classification. Classification does not manufacture a new type ID.

The fixture uses a public deterministic test key and the unmodified pinned
BSC chain configuration. The current-signer sample is at height 100,000,000
and timestamp 1,795,046,400 (November 19, 2026), after Pasteur and before
Jenner. The chosen height is a synthetic local input, not a public block.

Signer acceptance is not transaction execution validity. In particular, the
blob envelope and empty SetCode authorization list exercise decoding and
signature recovery only. They are not claimed to pass block import. This
probe is installed only as a native test; it changes no production source.

## Remaining acceptance

- Real current-fork import and extractor observation, successful and failed
  execution for applicable variants, and import-level activation controls.
- Parlia-generated system transactions with independently expected state
  before and after each system transaction.
- Native fork-transition and malformed-body rejection in the importer.

Until these pass, this BSC block and #21 remain partial. Public profiles
remain NOT_RUN. Native results are NOT_RUN until a retained run exists.
