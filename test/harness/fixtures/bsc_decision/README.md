# BSC decision driver: portable controls

`test_bsc_decision.py` (run by `bootstrap/ci`) checks the independent
evaluation of `test/harness/bsc_decision.py` on a recorded sample, and
`bsc_decision_driver.mutants.json` lists the single-check mutants of that
evaluation (`python3 test/mutate.py ...`).

Sample files:

- The fixture inputs `genesis.json` and `blocks.rlp` are read from
  `../bsc_admission` (the accepted private BSC fixture, chain 714,
  test-only, copied byte for byte from the ProfileManifest inputs).
- `genesis-hash.json`: the genesis hash of that fixture, from the
  ProfileManifest identity.
- `report.json`, `trace-*.jsonl`: the report and the event traces of the
  native probe (`TestNativeBSCDecision`).

Provenance: the evidence file `.project/evidence/E01-02.decision_bridge.bsc.md`
names the run that produced the sample and the SHA-256 of its report.
