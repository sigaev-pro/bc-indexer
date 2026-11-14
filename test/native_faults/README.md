# Native early faults (E01-04.early_faults)

This module collects evidence that the early capture seams survive a crash
and a power loss on real native nodes. It covers ETH (Geth), BSC and POLY
(Bor). The seams are admission, pins, EXEC_READY, the native decision, and
the live boundary H. It owns AC-SCP-03-01:

> Given an initial sync that reaches the current H, a canonical mutation is
> held on both sides of the H record and the admission switch. No
> initial-sync block is exported, no window loses the new H+1, and H, its
> hash and the mode agree durably.

The full native-plus-Gateway fault matrix stays with E05 (AC-SCP-03-02 is
E05-03.full_crash_matrix). Public profiles stay NOT_RUN (D96). Under D101
only short measured runs are allowed.

Block 0 (this change) fixes the fault model, the catalog of fault points,
the report format and the judge, with portable controls and mutants. It
runs no native node. The Geth, BSC and Bor blocks add the native probes and
the measured records.

## Fault model

ACCEPTANCE_CASES treats a crash and a power loss as different variants.
Every fault point runs both:

| Variant | What survives | How the probe makes it |
|---|---|---|
| `crash` | Every write made so far, synced or not (the OS cache survives) | Stop the native node at the fault point, then reopen on a file system that keeps every write (see "Restart") |
| `power_loss` | Only the writes that a completed sync made durable | The same stop, then reopen on a file system that keeps only the synced data |

Most fault points lie between a write and its `SyncKeyValue` inside a
shared accepted package (`Begin`, `Admit`, `ExecReady`, `Intend`,
`Resolve`, `Pin`, `Release`, `GoLive`), which a client block may not change.
So the probe arms the fault in a store wrapper that its own `native/` patch
set puts around the chaindata store the packages use:

- a point before a sync: the matching `SyncKeyValue` call returns an error
  without syncing, so the pending write stays unsynced;
- a point after a sync: that sync completes, and the next store call returns
  an error.

The error stops the step, and the probe stops the node. The wrapper counts
the key writes made since the last completed sync; that count is
`unsynced_dropped` for the power-loss variant. It is an upper bound: a WAL
byte sync or a memtable flush may have made some of those writes durable.
Then the probe reopens the node (see "Restart") and lets recovery finish.
After that it imports the smoke corpus blocks and the rest of the short
chain.

### Storage and cache model

The native chaindata store is Pebble: it is the default engine for a new
store in all three accepted trees (`node/database.go`). The Pebble version
differs, so the in-memory file system does too:

| Family | Pebble for a new store | File system | Power loss | `storage_model` |
|---|---|---|---|---|
| ETH (Geth) | v2.1.4 | `vfs.NewCrashableMem` | reopen on `CrashClone(CrashCloneCfg{UnsyncedDataPercent: 0})` | `pebble-v2-crashable-mem` |
| BSC, POLY (Bor) | v1.1.5 | `vfs.NewStrictMem` | `ResetToSyncedState`, then reopen | `pebble-v1-strict-mem` |

Both keep exactly the data that was last synced.

### Restart

A second open of a Pebble store on the same in-memory file system fails
while the first is still open (the file system holds an in-process lock),
and a normal close of a pebble v1 store syncs its WAL. So the probe reopens
like this:

| Family | `power_loss` | `crash` |
|---|---|---|
| ETH (pebble v2) | Reopen on `CrashClone(CrashCloneCfg{UnsyncedDataPercent: 0})`, a new file system with no locks | Reopen on `CrashClone(CrashCloneCfg{UnsyncedDataPercent: 100})` |
| BSC, POLY (pebble v1) | `SetIgnoreSyncs(true)`, close the node, `ResetToSyncedState`, `SetIgnoreSyncs(false)`, reopen | Close the node normally, then reopen: every write is kept | Every capture record of the
early seams lives in this store and is made durable by `SyncKeyValue`: the
admission records, the pins (`state_provider`, hash scheme only),
EXEC_READY, the decision records and markers, and the boundary record. So
the model covers every durable step that this task tests.

Limits of the model, recorded in every report as `storage_model`:

- It tests the two extremes of a power loss: every unsynced write dropped,
  and (as the crash variant) every one kept. The accepted core READMEs allow
  any subset of unsynced writes to survive. Each early step has one pending
  capture write after its previous sync, so the extremes cover its states;
  mixed survival of native writes in the same window is not tested.
- Native writes are not all unsynced until a capture sync: Pebble syncs the
  WAL every `WALBytesPerSync` bytes (geth sets 5 times `IdealBatchSize`), and
  a memtable flush also makes data durable. So `unsynced_dropped` is an
  upper bound of what the power loss dropped, and the states after the
  restart, read from the store, are what counts.
- It models the loss of unsynced data at the store level. It does not model
  torn sector writes, a device that acknowledges a sync it did not do, or
  file system metadata loss.
- The freezer (ancient store) uses plain files, not Pebble. The short
  private chains stay far below the freezer threshold, so no block reaches
  it.
- The store lives in memory, so both variants reopen the node in the same
  process. A process-level crash of a real binary is a different fault. The
  Bor block keeps its process-crash model as well (`bor.power-loss-model`).

## Catalog

[faults.json](faults.json) lists the fault points. Each point has:

- the seam and the exact boundary;
- the source that defines the expected state: a power-loss or crash-point
  table of an accepted core README, the GoLive section of the lifecycle
  README, or the pins evidence, narrowed to the two extremes above;
- whether a write is pending at the point (`pending`);
- the states allowed right after the restart, per variant;
- how H behaves (`h_rule`): `kept` when the boundary is durable at the
  point, `kept_if_present` when it may be lost. It is descriptive; the
  catalog check only requires `kept` points to expect a present boundary.

| Seam | Points |
|---|---|
| H (AC-SCP-03-01) | before the boundary sync; after the boundary sync; after GoLive, before the first live import |
| admission | before the counter sync; before the ADMITTED sync; after the ADMITTED sync |
| pins | before the pin sync; after the pin sync; before the release sync |
| EXEC_READY | before the EXEC_READY sync; after the EXEC_READY sync |
| decision | before the intent sync; inside the native mutation; before the decided sync; after the decided sync |

For a point with a pending write, the power-loss variant allows only the
state from before the write. The crash variant also allows the state the
write would make. The judge requires that a power loss at such a point
dropped at least one write, so a probe cannot pass by syncing too early.
Right after a restart the mode is BOOTSTRAP without a boundary and RECOVERY
with one; the states are read before recovery runs `GoLive`.

`handoffs` lists the open items that earlier decisions gave to this task
(D101, D106), each assigned to one client block. It also gives each block
the other side of AC-SCP-03-01: a canonical import held open while `GoLive`
runs is refused (`ErrOrder`) and finishes before H is set.

Rows of the core tables that are not catalog points:

- admission `Reject` and `Abandon`, and the native rejection before
  `RejectIntent` (AC-CAP-07-06): they need an invalid block, which never
  reaches export, so the export rules of this judge do not apply. The
  accepted unit tables and the #17 native AC-CAP-07-06 runs cover them, and
  the full matrix is E05-03.full_crash_matrix.
- "after the mirror applied, before the cleanup sync": no mirror exists
  until E05-01.registry.

## Report format

Each client block writes one JSON report per family:

```text
{
  "schema_version": 1, "kind": "native-faults-report",
  "family": "eth" | "bsc" | "poly",
  "storage_model": "pebble-v2-crashable-mem" (eth) | "pebble-v1-strict-mem" (bsc, poly),
  "source": {<accepted patched native build, patch set, tool and record digests>},
  "cases": [{
    "point": <catalog id>, "variant": "crash" | "power_loss",
    "fault": {"hit": true, "block": <number of the fault block>},
    "storage": {"reset_to_synced": <bool>, "unsynced_dropped": <count>},
    "after_restart": {
      "boundary": null | {"number", "hash", "root"},
      "native_h": <with a boundary: the native canonical {"number", "hash", "root"} at its number>,
      "mode": "BOOTSTRAP" | "RECOVERY",
      "admission": "none" | "preparing" | "admitted" | "exec_ready" | "decided",
      "pin": "none" | "live" | "released",
      "decision": "none" | "intent" | "intent_marker" | "decided"
    },
    "final": {
      "boundary": {"number", "hash", "root"},
      "native_h": {"number", "hash", "root"},
      "mode": "LIVE",
      "head": <number>,
      "exported": [<block numbers with a decided import, in any order>],
      "duplicates": <count of blocks exported more than once>,
      "corpus": "PASS" | "FAIL"
    }
  }],
  "handoffs": [{"id": <catalog id>, "verdict": "PASS" | "FAIL" | "NOT_RUN", "evidence": <text>}]
}
```

Hashes and roots are 64 lowercase hex digits without `0x`. The
`after_restart` states describe the fault block: the head at GoLive for an H
point, or the live block that hit the point. `native_h` is read from the
native chain (canonical hash and header state root at the boundary number),
not from the boundary record. `final.corpus` is the
`corpus_compare.py` result for the corpus blocks imported after recovery.

## Judge rules

For every case:

1. The fault point was reached.
2. A power loss reset the store to its synced state and, at a pending
   point, the wrapper counted at least one unsynced write. A crash reports
   a count of zero.
3. The boundary presence and each state after the restart are allowed for
   the variant. The mode is BOOTSTRAP without a boundary and RECOVERY with
   one. A boundary after the restart equals the native canonical header at
   its number.
4. At the end a durable boundary H exists, equals the native canonical
   header at H, and the capture path is LIVE. The exported blocks are exactly
   H+1 to the head: none at or below H (the initial sync is not exported),
   H+1 is present (no loss window), and none is exported twice.
5. The native result of the corpus blocks matches the smoke corpus.
6. A boundary that was durable after the restart is the final one (H, hash
   and root unchanged). For an H point it is the fault block. When no
   boundary survived, nothing was captured, so the new H may be anywhere;
   rules 4 and 5 still apply to it.
7. For every point outside H, the fault block is a live block: above H and
   at or below the head.

Report-level checks: the report kind and schema, a known family, the
`storage_model` of that family, known points and variants, no case or
handoff reported twice, and only handoffs of that family. A malformed case
or field fails; it never stops the judge.

The verdict is PASS when every point and variant passes and every handoff
of the family has a PASS verdict. It is INCOMPLETE when a case or handoff
is missing or NOT_RUN, and FAIL when any rule fails. AC-SCP-03-01 for the
family is PASS when both variants of the three H points pass.

## Commands

From the repository root:

```sh
python3 test/native_faults/faults_judge.py check
python3 test/native_faults/test_native_faults.py
python3 test/native_faults/faults_judge.py judge REPORT.json --output VERDICT.json
python3 test/mutate.py test/native_faults/native_faults.mutants.json --output RUN_DIR
```

`test/mutate.py` reads every file under the repository root. Run it in a
clean checkout or worktree without `.project/cache`.

## Client block duties

- Arm each fault point in the store wrapper (see "Fault model"), through the
  client's own patch set under `native/`. Change no shared
  `admission`, `decision`, `lifecycle`, `source_inputs` or `state_provider`
  (pins) file.
- Open the chaindata on the in-memory file system of its Pebble version, and
  take `unsynced_dropped` from the store wrapper's count of writes since the
  last completed sync.
- Read the `after_restart` states from the durable records through the
  accepted packages, before recovery changes them.
- Import the smoke corpus blocks after recovery, and compare them with
  `corpus_compare.py`.
- Answer every handoff of the family in the report.
- Keep failed attempts as D83 records.
