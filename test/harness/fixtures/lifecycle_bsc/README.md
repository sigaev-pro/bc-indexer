# BSC lifecycle probe (E01-02.lifecycle_seams, BSC block)

The design is in
[`adapters/bsc/lifecycle/native/README.md`](../../../../adapters/bsc/lifecycle/native/README.md).

| File | Role |
|---|---|
| `bsc_lifecycle.py` | `prepare` builds the probe binary from the accepted BuildManifest; `run` executes it under the host lock and judges it; `judge` judges a retained run again |
| `test_bsc_lifecycle.py` | Portable controls of the judge (run by `bootstrap/ci`) |
| `bsc_lifecycle.mutants.json` | Single-check mutants of the judge |
| `sample/` | The probe outputs of a development run (`report.json`, `trace.jsonl`, `blocks.rlp`), used by the portable controls; not measured evidence |

## What the probe does

The genesis is the accepted private BSC genesis of the ProfileManifest (chain
714, one validator). Native Parlia seals the blocks with the public test
validator key of the BSC source. A1 to A7 extend genesis, S3 forks from A2 and
S4 extends S3. Each block holds one transfer of public test key 1, so every
state root differs, and timestamps are the parent's plus 3 seconds (S3 plus
4), so the blocks are the same in every run. Plato is active at block 7.

1. Profile refusals outside the gate: `CheckProfile` with snap sync, voting,
   the builder role and the path scheme; `eth.New` with snap sync; `Start`
   with a Parlia engine without the system-call API.
2. Native caller path: the shipped node (`eth.New` with the capture config).
   Imports go through `admin_importChain`, rewinds through `debug_setHead`,
   and restarts close and open the node. The shared scenario:
   - BOOTSTRAP: import A1 A2 A3, import the side block S3, `setHead 2`, then
     `GoLive` (H = A2).
   - The fixture step: S3 stored above the head again, as a snap-sync
     rollback leaves it (README "Limits"), then `InsertChain S3`. The
     known-block pass stops with `ErrCorpus`, and the head stays A2.
   - LIVE: import A3 A4 A5 (three IMPORT decisions), `setHead 4` (CANONICAL),
     import A5 again (IMPORT).
   - Restart 1: the shutdown decision, RECOVERY, live at H.
   - Import S4 on S3, whose state the restart dropped: `ErrImportProfile`.
   - Import A6 A7.
   - Restart 2 with the A7 state root deleted: native startup repair rewinds
     to A6, and the reconciliation decides it (CANONICAL A7 to A6). Then
     `InsertChain A7` (IMPORT).
   - Then the rest of the caller map on the node: the Engine API, snap sync,
     header sync, the miner write, `StartMining`, the chain reset and
     `debug_executionWitness`, each refused.
3. Harness path: a direct `BlockChain` with `bsclifecycle.Attach`, the same
   scenario through direct calls, then a deadlock smoke (five guarded
   callers concurrently, ten calls each).
4. The attestation lookup error (AC-LIF-06-05) on a third store. The native
   snapshot lookup of A3 runs on a reader without A3's header. The import
   fails with `ErrSource` and A3 stays EXEC_READY. `SetHead 1` is then
   refused by the pin ledger after its intent, and the intent is rejected at
   run time. The manual database guard of the shipped geth
   (`utils.ExtractorGuardChaindata`) refuses the closed store while A3's pin
   is live. A restart without the fault replays and decides A3. The guard
   then refuses the store for its open obligations, and passes a store
   without decisions.
5. Event feeds (D101): a reference chain without the extractor and a capture
   store import A1 to A7 one block per call; A3 to A7 are decided.
6. Crash points of a SetHead decision, each in a child process that syncs
   the store and exits: after the intent (the restart rejects it), and after
   the native rewind, before the marker (the restart completes it).

The report holds, per step, the error, the phase, the head, the canonical
chain, the stored finalized block, the new decisions, the open observations
and the lifecycle view. It also holds the coverage of every gate, the
refusals outside the gate and `lifecycle.Gaps`. The trace holds the events
of the harness stores.

## The judge

`judge` reads `report.json`, `trace.jsonl` and `blocks.rlp` and checks them
with its own rules:
- the headers in `blocks.rlp`: hash (Keccak-256), parent, number, time,
  difficulty and validator, against the genesis hash of the ProfileManifest;
- each shared step of both paths, against an expected table: error class,
  phase, head, canonical chain, finalized block, view, open observations,
  and decisions (kind, heads, members, marker);
- each IMPORT evidence digest, recomputed with the decision bridge formula
  from the genesis fallback;
- each CANONICAL evidence digest, recomputed for its caller;
- each attestation event (status, heads, digest);
- the gaps, recomputed from the coverage and the repository caller map;
- the refusals, the attestation fault, the event feeds and the crash points.

It writes the qa matrix: AC-CAP-01-01 per caller row, and AC-LIF-05-02,
05-03, 06-04 and 06-05.

## Commands

From the repository root, in a clean frozen checkout (`prepare` and `run`
take `/tmp/extractor-harness-runner.lock`):

```sh
python3 -I test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.py prepare --build-manifest M --sha256 SHA --output PREP
python3 -I test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.py run PREP/tool.json --sha256 SHA_OF_TOOL --profile-manifest P SHA_OF_P --output RUN
python3 -I test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.py judge RUN --output NEW
python3 -I test/harness/fixtures/lifecycle_bsc/test_bsc_lifecycle.py
python3 -I test/mutate.py test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.mutants.json --output DIR
```
