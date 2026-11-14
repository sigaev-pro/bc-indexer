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
| `crash` | Every write made so far, synced or not (the OS cache survives) | Stop the native node at the fault point without closing the store, then reopen the same store |
| `power_loss` | Only the writes that a completed sync made durable | The same stop, then drop every unsynced write before the reopen |

The probe stops the node at the fault point: an armed hook in the native
caller returns an error there, so nothing after the point runs. Then it
reopens the node on the same store and lets recovery finish. After that it
imports the smoke corpus blocks and the rest of the short chain.

### Storage and cache model

The native chaindata store is Pebble: it is the default engine for a new
store in all three accepted trees (`node/database.go`, pebble v1.1.5). The probe opens it on Pebble's strict in-memory file
system (`vfs.NewStrictMem`). That file system keeps unsynced data apart.
`ResetToSyncedState` drops it, which is the power-loss variant. A reopen
without that call is the crash variant. Every capture record of the early
seams lives in this store and is made durable by `SyncKeyValue`: the
admission records, the pins (`state_provider`), EXEC_READY, the decision records and markers,
and the boundary record. So the model covers every durable step that this
task tests.

Limits of the model, recorded in every report as `storage_model`:

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
- the source table that defines the expected state, which is the power-loss
  table of the accepted core README;
- whether a write is pending at the point (`pending`);
- the states allowed right after the restart, per variant;
- how H behaves (`h_rule`).

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

`handoffs` lists the open items that earlier decisions gave to this task
(D101, D106), each assigned to one client block.

## Report format

Each client block writes one JSON report per family:

```text
{
  "schema_version": 1, "kind": "native-faults-report",
  "family": "eth" | "bsc" | "poly",
  "storage_model": "pebble-strict-mem",
  "source": {<accepted patched native build, patch set, tool and record digests>},
  "cases": [{
    "point": <catalog id>, "variant": "crash" | "power_loss",
    "fault": {"hit": true, "block": <number of the fault block>},
    "storage": {"reset_to_synced": <bool>, "unsynced_dropped": <count>},
    "after_restart": {
      "boundary": null | {"number", "hash", "root"},
      "mode": "BOOTSTRAP" | "RECOVERY" | "LIVE",
      "admission": "none" | "preparing" | "admitted" | "exec_ready" | "decided",
      "pin": "none" | "live" | "released",
      "decision": "none" | "intent" | "intent_marker" | "decided"
    },
    "final": {
      "boundary": {"number", "hash", "root"},
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
point, or the live block that hit the point. `final.corpus` is the
`corpus_compare.py` result for the corpus blocks imported after recovery.

## Judge rules

For every case:

1. The fault point was reached.
2. A power loss reset the store to its synced state and, at a pending
   point, dropped at least one write. A crash dropped nothing.
3. The boundary presence and each state after the restart are allowed for
   the variant. The mode is BOOTSTRAP exactly when no boundary exists.
4. At the end a durable boundary H exists. The exported blocks are exactly
   H+1 to the head: none at or below H (the initial sync is not exported),
   H+1 is present (no loss window), and none is exported twice.
5. The native result of the corpus blocks matches the smoke corpus.
6. A boundary that was durable after the restart is the final one (H, hash
   and root unchanged). For an H point it is the head of the fault. When no
   boundary survived, the new H is at or above the fault block.
7. For every point outside H, the fault block is a live block: above H and
   at or below the head.

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

- Arm each fault point in the native caller of the accepted patched tree,
  through the client's own patch set under `native/`. Change no shared
  `admission`, `decision`, `lifecycle`, `source_inputs` or `state_provider`
  (pins) file.
- Open the chaindata on the strict in-memory file system, and record
  `unsynced_dropped` from the store, not from the probe's own count.
- Read the `after_restart` states from the durable records through the
  accepted packages, before recovery changes them.
- Import the smoke corpus blocks after recovery, and compare them with
  `corpus_compare.py`.
- Answer every handoff of the family in the report.
- Keep failed attempts as D83 records.
