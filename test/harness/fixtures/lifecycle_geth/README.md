# Geth lifecycle probe (E01-02.lifecycle_seams, Geth block)

| File | Role |
|---|---|
| `lifecycle_geth.py` | `prepare` builds the probe from the accepted Geth decision-bridge preparation plus the lifecycle patch set; `run` executes it under the host lock and judges the report; `judge` judges a saved report |
| `probe.go` | The probe, installed as `cmd/extractor-lifecycle-geth` |
| `genesis.json` | The private ETH fixture (chain 31337, test key 1 with 10 ETH) with the merge at genesis (`terminalTotalDifficulty` 0), because the Engine API forkchoice path needs proof of stake |
| `test_lifecycle_geth.py` | Portable controls of the driver (run by `bootstrap/ci`) |
| `lifecycle_geth.mutants.json` | Single-check mutants of the driver |
| `sample_report.json` | The report of a development run of the probe, used by the portable tests; not measured evidence |

## What the probe does

Chains: A1..A6 extend genesis; B3 B4, C3 C4, S3 S4, T3 T4 and G3 fork from
A2, H5 H6 and J5 J6 from A4, each block with one transfer, so every state root
differs.

1. Profile and identity, outside the gate: `CheckFlags` with `--dev`,
   `--synctarget` and snap sync; `eth.New` with snap sync; `eth.New` with a
   config whose `native_commit` is not the binary's.
2. Native caller path: a shipped node (`eth.New` with the capture config, no
   peers) and the Engine API (`NewPayloadV1`, `ForkchoiceUpdatedV1`).
   - BOOTSTRAP: A1 A2 A3 and the side blocks S3 T3, forkchoice A3; the live
     service goes live (H = A3).
   - The shared LIVE scenario: `newPayload` A4 (SIDE), forkchoice A4
     (CANONICAL), safe A2 and finalized A1 (two FINALITY), zero hashes
     (AC-LIF-05-02), the same references again (no decision), finalized A2
     with an unknown safe hash (AC-LIF-05-03), the fork B3 B4 and the
     forkchoice reorganization, then forkchoice back to A4, whose
     observation is closed (`ErrCorpus`).
   - `InsertChain` of the stored side blocks C3 C4 (`geth.known-block`), the
     refusals through the gate (`InsertReceiptChain`, `Syncer.Sync`,
     `NewSimulatedBeacon`), the chain reset, a safe change, and a deadlock
     smoke (five guarded callers concurrently, ten calls each).
   - Restart: RECOVERY, the startup reconciliation of the safe reset, live at
     the stored H. `InsertChain` of T4 on the pruned T3 (`geth.side-chain`),
     `InsertBlockWithoutSetHead` of S4 on the pruned S3
     (`geth.recover-ancestors`), forkchoice S4, `SetHead(3)`, and a retained
     side block G3.
   - The manual-command guard (`RequireNoOpenCapture`) on the closed store.
3. Harness path: a second store, the same BOOTSTRAP and LIVE scenario
   through direct calls of the `BlockChain` methods that the Engine API
   calls. AC-CAP-01-01 compares, per step, the decisions (kind, old and new
   head, members, state) and the failures, and the final native result.
4. Coverage of both native-store opens plus the refusals outside the gate,
   and `lifecycle.Gaps` for client `geth`.
5. Crash scenarios on a third store, each in a child process that syncs the
   store at its crash point and exits, then a restart with the shipped
   attach: a FINALITY intent (the setter's batch never written: rejected); a
   SetHead rewind with finalized A3 and safe A5, after the native rewind and
   before its marker (head is the new head: completed, then the safe reset is
   decided by the reconciliation); a CANONICAL intent before the head write
   (head is the old head: rejected); a forkchoice from A4 to H6 between the
   head batch of H5 and the final head batch of H6 (completed from the old
   head through `ExtractorCompleteHead`); a head marker rewound to A4 under
   a canonical index that keeps H5 H6 (the rewind is decided at the restart,
   and a forward forkchoice to H6 is refused for lack of corpus); a crash
   before the final head batch of J6 with the head lowered to A3, below the
   fork point (the restart stops instead of completing). Every FINALITY
   decision of the
   main scenario is named by its field, caller and references, recomputed
   from its evidence digest.

## Commands

From the repository root, in a clean checkout (both take
`/tmp/extractor-harness-runner.lock`):

```sh
python3 test/harness/fixtures/lifecycle_geth/lifecycle_geth.py prepare --accepted-preparation PREPARATION.json --sha256 SHA --output OUT
python3 test/harness/fixtures/lifecycle_geth/lifecycle_geth.py run OUT/preparation.json --sha256 SHA --output RUN
python3 test/harness/fixtures/lifecycle_geth/test_lifecycle_geth.py
python3 test/mutate.py test/harness/fixtures/lifecycle_geth/lifecycle_geth.mutants.json --output RUN_DIR
```

`run` writes `probe/report.json` and `run-record.json`, with the native
failures and the qa matrix. It is PASS only when every self-check holds and
every applicable row passes.
