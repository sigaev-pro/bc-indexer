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

## Commands

Run from a clean isolated worktree, with the host runtime slot assigned by
the coordinator. Paths and digests are mandatory; output directories must
not already exist.

```sh
python3 test/type_paths/bsc/native.py prepare BUILD_MANIFEST --sha256 SHA256 --output PREPARATION
python3 test/type_paths/bsc/native.py run PREPARATION/tool.json --sha256 TOOL_SHA256 --output RUN
python3 test/type_paths/bsc/test_native.py
python3 test/mutate.py test/type_paths/bsc/native.mutants.json --output MUTATION_RUN
```

Preparation extracts a new verified copy of the original source, installs
the test, checks formatting, vets and builds it offline with the accepted
compiler and module cache. It holds the existing host lock and retains all
commands and cleanup results. Preparation is bounded to one hour; the
component run is separately bounded to 180 seconds. Neither phase modifies
accepted source/build artifacts. The run requires the exact prepared probe
and driver, validates the native binary digest, rejects missing reports,
and requires the named Go test to pass once without skips.

## Remaining acceptance

- Real current-fork import and extractor observation, successful and failed
  execution for applicable variants, and import-level activation controls.
- Parlia-generated system transactions with independently expected state
  before and after each system transaction.
- Native fork-transition and malformed-body rejection in the importer.

Until these pass, this BSC block and #21 remain partial. Public profiles
remain NOT_RUN. Native results are NOT_RUN until a retained run exists.
