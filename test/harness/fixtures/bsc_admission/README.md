# BSC admission driver: portable controls

`test_bsc_admission.py` (run by `bootstrap/ci`) checks the independent
evaluation of `test/harness/bsc_admission.py` on a recorded sample, and
`bsc_admission_driver.mutants.json` lists the single-check mutants of that
evaluation (`python3 test/mutate.py ...`).

Sample files:

- `genesis.json`, `blocks.rlp`: the accepted private BSC fixture inputs
  (`private-bsc-transfer-empty`, chain 714, test-only), copied byte for byte
  from the ProfileManifest inputs.
- `chain_config.json`: `bsc_admission.chain_config_forks` of the pinned BSC
  source (`params/config.go` at `54afaff`).
- `report.json`, `trace-*.jsonl`: the report and the event traces of the
  native probe (`TestNativeBSCAdmission`). `genesis_canonical_hex` is removed
  from the report to keep it small; the test puts it back from `genesis.json`
  with the shared canonical JSON rule.
- `geth.json`: the exit codes and the non-INFO output lines of the three
  patched geth checks.

Provenance: the evidence file `.project/evidence/E01-02.admission.bsc.md`
names the run that produced the sample and the SHA256 of its report.
