# Modern validator initialization prerequisite

This native probe calls the pinned Parlia system-transaction execution method
against explicitly allocated modern system bytecode. It is not a blockchain
import, an extractor observation, or complete system initialization. Those
fields must remain `NOT_RUN`, even if this probe passes.

The context uses the unchanged native BSC configuration at height 100000001,
time 1795046401 (Pasteur active, Jenner future). The deterministic public key1
signer is absent from the mainnet validator initialization list. Consequently
this probe explicitly covers **deprecated-validator deposit**, with map index0,
total incoming0, turn length0, and anti-MEV ratio0. Active-validator income and
the private modern genesis remain outstanding.

`provenance.json` binds each allocated contract to its latest native code file
through Pasteur, decoded-bytecode SHA256 and upstream contract revision. The
native report must match every bytecode digest. The independent oracle comes
from `BSCValidatorSet.sol` and inherited `System.sol` at the exact Lorentz
revision `44ebc6c17a00bd24db3240141a78091528dcebbb`; retained fetched copies are
listed by SHA256 in the manifest. Native bytecode is taken from the accepted
BSC source, not rebuilt from an arbitrary contract revision.

The native `init()` creates the embedded validator array and sets `alreadyInit`.
The next call deposits10000 wei: system reward625, burn1000, validator8375;
coinbase nonce increments once per system transaction and ordinary gas fees
are zero. Python independently checks both complete selected before/after
snapshots. Solidity slots0/1/3/6/15/20/23/24 and the slot4 signer mapping bind
initialization, incoming, reward/burn settings and branch preconditions.
Native receipt success is required. No expected values come from another run.

Run portable controls with:

```sh
python3 -m unittest discover -s test/type_paths/bsc/modern_init -p 'test_*.py'
```

The separate `native.py prepare` and `native.py run` use the same command shape
as the accepted parent BSC component driver. They require coordinator runtime
authorization, absolute manifest/tool/output paths, and the input SHA256.
Never reuse or overwrite a previous output directory. Keep all failed attempts.
