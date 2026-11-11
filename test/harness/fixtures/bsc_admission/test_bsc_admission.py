#!/usr/bin/env python3
"""Portable controls of the BSC admission driver (test/harness/bsc_admission.py).

The sample in this directory is the report, the traces and the geth outputs of
a native probe run on the accepted private BSC fixture (see README.md). The
baseline must pass the driver's evaluation; every control changes one fact and
must be refused with the matching message. The digests are recomputed in this
file with the shared admission and source_inputs references, so a mutated
driver formula fails the baseline instead of agreeing with itself.
"""

import copy
import gzip
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1]))
import admission  # noqa: E402
from artifacts import Rejected  # noqa: E402
import bsc_admission as driver  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402

GENESIS = (HERE / "genesis.json").read_bytes()
RAW = probe14.split_rlp_list_items((HERE / "blocks.rlp").read_bytes())
CHAIN_CONFIG = json.loads((HERE / "chain_config.json").read_text())
COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
REPORT = json.loads((HERE / "report.json").read_text())
for _scenario in REPORT["scenarios"]:
    if _scenario["name"] == "live":
        _scenario["extra"]["genesis_canonical_hex"] = admission.canonical(GENESIS).hex()
        GENESIS_HASH = "0x" + probe14.keccak256(bytes.fromhex(_scenario["extra"]["genesis_header_rlp"])).hex()
PREPARED = [{"number": n + 1, "hash": "0x" + probe14.keccak256(probe14.rlp_list(raw)[0]).hex()} for n, raw in enumerate(RAW)]
GETH = {f"{name}_{field}": value["exit"] if field == "exit" else "\n".join(value["output_lines"])
        for name, value in json.loads((HERE / "geth.json").read_text()).items() for field in ("exit", "output")}


def load_traces():
    traces = {}
    for name in driver.TRACES:
        traces[name] = [json.loads(line) for line in (HERE / f"trace-{name}.jsonl").read_text().splitlines()]
    for mode in driver.CRASH_MODES:
        events = traces[mode]
        mark = [index for index, event in enumerate(events) if event.get("event") == "restart"][0]
        traces["child-" + mode], traces[mode] = events[:mark], events[mark + 1:]
    return traces


TRACES = load_traces()
LONG_REPORT = json.loads((HERE / "long-report.json").read_text())
LONG_BLOCKS = gzip.decompress((HERE / "long-blocks.rlp.gz").read_bytes())
EXTRA = {name: [json.loads(line) for line in gzip.decompress((HERE / f"trace-{name}.jsonl.gz").read_bytes()).splitlines()]
         for name in driver.LONG_TRACES}
EXTRA["parent-state"] = [json.loads(line) for line in (HERE / "trace-parent-state.jsonl").read_text().splitlines()]


def scenario(report, name):
    return next(row for row in report["scenarios"] if row["name"] == name)


def evaluate(report=None, traces=None, geth=None, genesis=GENESIS, chain_config=CHAIN_CONFIG, prepared=PREPARED):
    return driver.checked_evaluate(report or copy.deepcopy(REPORT), traces or copy.deepcopy(TRACES), RAW, genesis,
                                   COMMIT, GENESIS_HASH, chain_config, prepared, geth or dict(GETH))


def changed_snapshot(handoff):
    """Another snapshot response of the same block: one more JSON field."""
    return json.dumps(dict(json.loads(bytes.fromhex(handoff["response_hex"])), extra=1)).encode()


def record(sc, sequence, kind):
    return sc["records"][f"{sequence:016x}/{kind}"]


def rebind_exec_ready(sc, sequence):
    """Recompute the EXEC_READY digest of one record after a changed input."""
    admitted, ready = record(sc, sequence, "admitted"), record(sc, sequence, "exec-ready")
    ready["digest"] = admission.exec_ready_digest(admitted["digest"], ready["consumed_digest"], ready["exec_digest"])


class Evaluate(unittest.TestCase):
    def refused(self, message, **changes):
        with self.assertRaises(Rejected) as caught:
            evaluate(**changes)
        self.assertIn(message, str(caught.exception))
        if "Malformed" not in message:
            self.assertNotIn("Malformed", str(caught.exception), "a check failure was reported as a malformed report")

    def test_baseline_passes(self):
        summary = evaluate()
        self.assertEqual(sorted(summary["scenarios"]), sorted(driver.TRACES))
        self.assertEqual(summary["scenarios"]["reject"]["rejected"], ["coinbase do not match with signature"])
        self.assertEqual(summary["scenarios"]["admitted-unsynced-drop"]["abandoned"], [2])

    def test_digests_are_independent(self):
        live = scenario(REPORT, "live")
        ready = record(live, 1, "exec-ready")
        admitted = record(live, 1, "admitted")
        self.assertEqual(ready["digest"], admission.exec_ready_digest(admitted["digest"], ready["consumed_digest"],
                                                                      ready["exec_digest"]))
        header = probe14.rlp_list(RAW[1])[0]
        receipts = [bytes.fromhex(row.split(":")[1]) for row in live["extra"]["receipts"].split(",")
                    if row.startswith("2:")]
        data = b"extractor-bsc-exec-result-v1\x00" + probe14.keccak256(header)
        fields = probe14.rlp_list(header)
        data += probe14.rlp_bytes(fields[3]) + probe14.rlp_bytes(fields[5]) + len(receipts).to_bytes(8, "big")
        for receipt in receipts:
            data += len(receipt).to_bytes(8, "big") + receipt
        self.assertEqual(ready["exec_digest"], hashlib.sha256(data).hexdigest())
        pin = bytes.fromhex(live["pins"][driver.key_of(1)])
        self.assertEqual(admitted["pin_digest"], hashlib.sha256(pin).hexdigest())

    def test_genesis_checks(self):
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["extra"]["genesis_header_rlp"] = "c0"
        self.refused("Genesis header does not hash", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["extra"]["genesis_canonical_hex"] = GENESIS.hex()
        self.refused("Canonical genesis differs", report=report)

    def test_prepared_blocks(self):
        prepared = copy.deepcopy(PREPARED)
        prepared[1]["hash"] = "0x" + "11" * 32
        self.refused("Prepared hash 2", prepared=prepared)

    def test_fork_table_and_unknown_keys(self):
        table = copy.deepcopy(CHAIN_CONFIG)
        table["keys"].remove("brunoBlock")
        self.refused("ChainConfig does not know", chain_config=table)
        table = copy.deepcopy(CHAIN_CONFIG)
        table["forks"]["eulerBlock"] = "time"
        self.refused("capsule differs from the fixture items", chain_config=table)

    def test_capsule_item(self):
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["capsules"][driver.key_of(0)]["items"][6]["sha256"] = "00" * 32
        self.refused("live/bsc-probe.run-1.0000000000000000: capsule differs", report=report)

    def test_candidate(self):
        report = copy.deepcopy(REPORT)
        record(scenario(report, "live"), 1, "preparing")["candidate"]["parent_root"] = "22" * 32
        self.refused("live/bsc-probe.run-1.0000000000000001: candidate", report=report)

    def test_admitted_digest(self):
        report = copy.deepcopy(REPORT)
        record(scenario(report, "live"), 1, "admitted")["digest"] = "33" * 32
        self.refused("admitted digest", report=report)
        report = copy.deepcopy(REPORT)
        record(scenario(report, "live"), 1, "admitted")["capsule_digest"] = "33" * 32
        self.refused("admitted capsule digest", report=report)

    def test_pin_digest_and_record(self):
        report = copy.deepcopy(REPORT)
        live = scenario(report, "live")
        pin = json.loads(bytes.fromhex(live["pins"][driver.key_of(1)]))
        live["pins"][driver.key_of(1)] = json.dumps(pin, separators=(",", ":")).encode().hex() + "20"
        self.refused("pin digest", report=report)
        # A consistent pin record that names the wrong headers.
        report = copy.deepcopy(REPORT)
        live = scenario(report, "live")
        pin["headers"] = pin["headers"][:1]
        encoded = json.dumps(pin, separators=(",", ":")).encode()
        live["pins"][driver.key_of(1)] = encoded.hex()
        admitted, prep = record(live, 1, "admitted"), record(live, 1, "preparing")
        admitted["pin_digest"] = hashlib.sha256(encoded).hexdigest()
        admitted["digest"] = admission.admitted_digest(driver.key_of(1), 1, prep["candidate"], admitted["pin_digest"],
                                                       admitted["capsule_digest"])
        record(live, 1, "exec-ready")["admitted_digest"] = admitted["digest"]
        rebind_exec_ready(live, 1)
        self.refused("pin record", report=report)

    def test_consumed_from_handoffs(self):
        traces = copy.deepcopy(TRACES)
        handoff = next(e for e in traces["live"] if e.get("event") == "handoff" and e["observation"] == driver.key_of(1))
        changed = changed_snapshot(handoff)
        handoff["response_hex"], handoff["response_sha256"] = changed.hex(), hashlib.sha256(changed).hexdigest()
        self.refused("live/bsc-probe.run-1.0000000000000001: EXEC_READY digests", traces=traces)
        traces = copy.deepcopy(TRACES)
        handoff = next(e for e in traces["live"] if e.get("event") == "handoff")
        handoff["response_sha256"] = "00" * 32
        self.refused("Hand-off digest", traces=traces)

    def test_snapshot_requests(self):
        def tampered(field, value):
            traces = copy.deepcopy(TRACES)
            handoff = next(e for e in traces["live"] if e.get("event") == "handoff" and
                           e["observation"] == driver.key_of(1))
            handoff[field] = value(handoff)
            return traces
        self.refused("request is not a snapshot request", traces=tampered(
            "request_hex", lambda h: (b"bsc-parlia-db-get-v1\x00parlia-" + b"\x01" * 32).hex()))
        self.refused("snapshot of a header outside the capsule", traces=tampered(
            "request_hex", lambda h: (driver.SNAPSHOT_TAG + (5).to_bytes(8, "big") + b"\x01" * 32).hex()))
        other = lambda h: json.dumps(dict(json.loads(bytes.fromhex(h["response_hex"])), number=7)).encode().hex()
        self.refused("response is the snapshot of another block", traces=tampered("response_hex", other))

    def test_handoff_sequence(self):
        traces = copy.deepcopy(TRACES)
        handoff = next(e for e in traces["live"] if e.get("event") == "handoff")
        handoff["sequence"] = 5
        self.refused("hand-off sequence", traces=traces)

    def test_exec_digest_and_record_digest(self):
        report = copy.deepcopy(REPORT)
        live = scenario(report, "live")
        rows = live["extra"]["receipts"].split(",")
        rows[0] = rows[0][:-2] + ("00" if not rows[0].endswith("00") else "01")
        live["extra"]["receipts"] = ",".join(rows)
        self.refused("EXEC_READY digests", report=report)
        report = copy.deepcopy(REPORT)
        record(scenario(report, "live"), 1, "exec-ready")["digest"] = "44" * 32
        self.refused("live/bsc-probe.run-1.0000000000000001: EXEC_READY digests", report=report)

    def test_exec_ready_for_both_blocks(self):
        report = copy.deepcopy(REPORT)
        live = scenario(report, "live")
        for kind in ("preparing", "admitted", "exec-ready"):
            del live["records"][f"{0:016x}/{kind}"]
        del live["pins"][driver.key_of(0)]
        self.refused("live: EXEC_READY blocks [2]", report=report)

    def test_recovery_completes_the_saved_obligation(self):
        report, traces = copy.deepcopy(REPORT), copy.deepcopy(TRACES)
        source = scenario(report, "before-admitted-put-drop")
        target = scenario(report, "admitted-synced")
        for field in ("records", "capsules", "pins"):
            target[field] = source[field]
        traces["admitted-synced"] = traces["before-admitted-put-drop"]
        self.refused("admitted-synced: another obligation", report=report, traces=traces)

    def test_input_preparing_is_abandoned(self):
        report = copy.deepcopy(REPORT)
        record(scenario(report, "admitted-unsynced-drop"), 1, "preparing")["candidate"]["number"] = 1
        self.refused("admitted-unsynced-drop: INPUT_PREPARING not abandoned", report=report)

    def test_no_exec_ready(self):
        report = copy.deepcopy(REPORT)
        del scenario(report, "live")["records"][f"{1:016x}/exec-ready"]
        self.refused("no EXEC_READY", report=report)

    def test_rejected_record(self):
        traces = copy.deepcopy(TRACES)
        next(e for e in traces["reject"] if e.get("event") == "rejected")["cause"] = "other"
        self.refused("NATIVE_REJECTED digest", traces=traces)
        report = copy.deepcopy(REPORT)
        scenario(report, "reject")["pins"][driver.key_of(1)] = "7b7d"
        self.refused("rejected candidate or pin", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "reject")["import_errors"][1] = ""
        self.refused("Reject scenario", report=report)

    def test_abandoned(self):
        report = copy.deepcopy(REPORT)
        scenario(report, "admitted-unsynced-drop")["pins"][driver.key_of(1)] = "7b7d"
        self.refused("abandoned with other records or a pin", report=report)

    def test_pins_match_exec_ready(self):
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["pins"]["bsc-probe.run-1.00000000000000ff"] = "7b7d"
        self.refused("live pins are not exactly", report=report)

    def test_order_execution_after_admitted_sync(self):
        traces = copy.deepcopy(TRACES)
        events = traces["live"]
        key = driver.key_of(1)
        put = next(i for i, e in enumerate(events) if e.get("record") == "admitted" and e.get("observation") == key)
        execute = next(i for i, e in enumerate(events) if e.get("event") == "execute" and e.get("observation") == key)
        events.insert(put, events.pop(execute))
        self.refused("execution before the closing ADMITTED sync", traces=traces)
        traces = copy.deepcopy(TRACES)
        events = traces["live"]
        put = next(i for i, e in enumerate(events) if e.get("record") == "admitted" and e.get("observation") == key)
        del events[put + 1]
        self.refused("executed without a synced ADMITTED record", traces=traces)

    def test_order_prepare_steps(self):
        traces = copy.deepcopy(TRACES)
        events = traces["live"]
        key = driver.key_of(1)
        pinned = next(i for i, e in enumerate(events) if e.get("event") == "pinned" and e.get("observation") == key)
        events.insert(0, events.pop(pinned))
        self.refused("begin, pin and seal are not in order", traces=traces)
        traces = copy.deepcopy(TRACES)
        events = traces["live"]
        put = next(i for i, e in enumerate(events) if e.get("record") == "admitted" and e.get("observation") == key)
        last_capsule = max(i for i, e in enumerate(events) if e.get("record") in ("capsule", "capsule-chunk") and
                           e.get("observation") == key)
        traces["live"] = events[:last_capsule + 1] + [e for e in events[last_capsule + 1:put] if e.get("event") != "sync"] \
            + events[put:]
        self.refused("capsule not synced before ADMITTED", traces=traces)

    def test_order_exec_ready_sync(self):
        traces = copy.deepcopy(TRACES)
        events = traces["live"]
        put = next(i for i, e in enumerate(events) if e.get("record") == "exec-ready")
        del events[put + 1]
        self.refused("EXEC_READY record without its sync", traces=traces)
        traces = copy.deepcopy(TRACES)
        events = traces["live"]
        put = next(i for i, e in enumerate(events) if e.get("record") == "exec-ready")
        closed = max(i for i, e in enumerate(events[:put]) if e.get("record") == "closed")
        traces["live"] = events[:closed + 1] + [e for e in events[closed + 1:put] if e.get("event") != "sync"] + events[put:]
        self.refused("log closed without a sync", traces=traces)

    def test_child_crash_points(self):
        for mode in driver.CRASH_MODES:
            traces = copy.deepcopy(TRACES)
            traces["child-" + mode][-1]["point"] = "elsewhere"
            self.refused(f"{mode}: no crash", traces=traces)
        # Any other event right before the crash moves the crash point.
        for mode in driver.CRASH_MODES[1:]:
            traces = copy.deepcopy(TRACES)
            traces["child-" + mode].insert(-1, {"event": "noise"})
            # An EXEC_READY record that is not followed by the crash needs its sync.
            message = "EXEC_READY record without its sync" if mode == "exec-ready-unsynced-drop" else f"{mode}: ["
            self.refused(message, traces=traces)
        traces = copy.deepcopy(TRACES)
        traces["child-exec-ready-unsynced-drop"][-2]["observation"] = driver.key_of(0)
        self.refused("exec-ready-unsynced-drop: [", traces=traces)
        traces = copy.deepcopy(TRACES)
        events = traces["child-before-admitted-put-drop"]
        events.insert(-1, {"event": "put", "observation": driver.key_of(1), "record": "admitted"})
        self.refused("ADMITTED was written", traces=traces)

    def test_crash_recovery_expectations(self):
        report = copy.deepcopy(REPORT)
        sc = scenario(report, "admitted-synced")
        sc["observations_after_restart"] = [row for row in sc["observations_after_restart"]
                                            if row["candidate"]["number"] != 2]
        self.refused("admitted-synced: block 2 ADMITTED after restart", report=report)
        report = copy.deepcopy(REPORT)
        sc = scenario(report, "admitted-unsynced-drop")
        sc["observations_after_restart"] = [row for row in sc["observations_after_restart"]
                                            if row["candidate"]["number"] != 2]
        self.refused("lost its INPUT_PREPARING record", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "first-handoff")["observations_after_restart"].append(
            {"key": "x", "state": "EXEC_READY", "candidate": {"number": 2}})
        self.refused("first-handoff: states after restart", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "first-handoff")["go_live_error"] = ""
        self.refused("first-handoff: GoLive was not refused", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "first-handoff")["mode_at_start"] = "LIVE"
        self.refused("first-handoff: GoLive was not refused", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "admitted-synced")["alt_block_error"] = "other"
        self.refused("a new candidate passed the obligation", report=report)

    def test_scenario_set(self):
        report = copy.deepcopy(REPORT)
        report["scenarios"] = report["scenarios"][:-1]
        self.refused("Scenarios", report=report)

    def test_head_safe_points_and_system_transactions(self):
        report = copy.deepcopy(REPORT)
        scenario(report, "reject")["head_number"] = 1
        self.refused("reject: head", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["safe_points_outside"] = 1
        self.refused("outside an observation", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["system_txs_outside"] = 1
        self.refused("outside an observation", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["system_txs"] = {driver.key_of(0): 8}
        self.refused("System transactions", report=report)
        traces = copy.deepcopy(TRACES)
        next(e for e in traces["live"] if e.get("event") == "exec-ready")["safe_points"] = 0
        self.refused("Safe points by observation", traces=traces)

    def test_consumed_equal_across_scenarios(self):
        report, traces = copy.deepcopy(REPORT), copy.deepcopy(TRACES)
        # Give the reject scenario a block 2 log with another, consistent response.
        sc = scenario(report, "reject")
        events = traces["reject"]
        key = driver.key_of(2)
        last = max(i for i, e in enumerate(events) if e.get("event") == "handoff" and e.get("observation") == key)
        changed = changed_snapshot(events[last])
        events[last]["response_hex"], events[last]["response_sha256"] = changed.hex(), hashlib.sha256(changed).hexdigest()
        handoffs = driver.attempt_handoffs(events, key)
        ready = record(sc, 2, "exec-ready")
        ready["consumed_digest"] = driver.consumed(record(sc, 2, "admitted")["capsule_digest"], handoffs)
        rebind_exec_ready(sc, 2)
        self.refused("Block 2 consumed digests differ", report=report, traces=traces)

    def test_live_refusals(self):
        for field, message in (("import_before_recovery", "import before recovery"),
                               ("unobserved_execution", "Unobserved execution"),
                               ("rewind_below_pin", "Rewind below a pin"), ("foreign_run", "Foreign run"),
                               ("preflight_other_store", "Preflight of another store")):
            report = copy.deepcopy(REPORT)
            scenario(report, "live")["extra"][field] = "accepted"
            self.refused(message, report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["extra"]["restart_observations"] = "0"
        self.refused("Restart does not list", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["extra"]["not_on_head"] = ""
        self.refused("A block off the head was admitted", report=report)
        report = copy.deepcopy(REPORT)
        scenario(report, "live")["extra"]["known_block"] = ";3"
        self.refused("A known block was observed again", report=report)

    def test_geth_checks(self):
        for field, value, message in (("prune_exit", 0, "prune-state was not refused"),
                                      ("prune_output", "other error", "prune-state was not refused"),
                                      ("start_exit", 1, "did not start on pins"),
                                      ("start_output", "0", "did not start on pins"),
                                      ("bloom_exit", 0, "started with a pending prune"),
                                      ("bloom_output", "Fatal: other", "started with a pending prune")):
            geth = dict(GETH)
            geth[field] = value
            self.refused(message, geth=geth)

    def test_malformed_report_is_a_rejection(self):
        report = copy.deepcopy(REPORT)
        del scenario(report, "live")["extra"]
        self.refused("Malformed probe report", report=report)


class Patches(unittest.TestCase):
    def test_patch_files_parse(self):
        paths = driver.patch_files()
        self.assertEqual([path.name[:3] for path in paths], ["01-", "02-", "03-", "04-", "05-", "06-"])
        touched = [row[0] for path in paths for row in driver.parse_patch(path.read_text())]
        self.assertEqual(len(touched), len(set(touched)), "a native file is patched twice")
        self.assertIn("core/blockchain.go", touched)

    def test_apply_is_exact(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "a.go").write_text("one\ntwo\nthree\nfour\n")
            patch = ("--- a/a.go\n+++ b/a.go\n@@ -2,2 +2,3 @@\n two\n-three\n+THREE\n+extra\n four\n"
                     "--- /dev/null\n+++ b/n.go\n@@ -0,0 +1,1 @@\n+new\n")
            rows = driver.apply_patch(root, patch)
            self.assertEqual((root / "a.go").read_text(), "one\ntwo\nTHREE\nextra\nfour\n")
            self.assertEqual((root / "n.go").read_text(), "new\n")
            self.assertEqual([row["path"] for row in rows], ["a.go", "n.go"])
            with self.assertRaisesRegex(Rejected, "context differs"):
                driver.apply_patch(root, patch)
            with self.assertRaisesRegex(Rejected, "creates an existing file"):
                driver.apply_patch(root, "--- /dev/null\n+++ b/n.go\n@@ -0,0 +1,1 @@\n+new\n")
            (root / "b.go").write_text("x\n")
            (root / "c.go").write_text("x\n")
            for bad, message in (("--- a/b.go\n+++ b/b.go\n@@ -1,1 +1,1 @@\n-y\n+z\n", "context differs"),
                                 ("--- a/b.go\n+++ b/c.go\n@@ -1,1 +1,1 @@\n-x\n+z\n", "renames a file"),
                                 ("--- a/../b.go\n+++ b/../b.go\n@@ -1,1 +1,1 @@\n-x\n+z\n", "Unsafe patch path"),
                                 ("--- a/b.go\n+++ b/b.go\n@@ -1,2 +1,1 @@\n-x\n", "Unsupported hunk line"),
                                 ("--- a/b.go\n+++ b/b.go\n", "No hunk"), ("nothing\n", "Empty patch"),
                                 ("--- a/b.go\n+++ b/b.go\n@@ -1,1 +1,1 @@\n-x\n+z", "end with a newline")):
                with self.assertRaisesRegex(Rejected, message):
                    driver.apply_patch(root, bad)
            self.assertEqual((root / "b.go").read_text(), "x\n")
            self.assertEqual((root / "c.go").read_text(), "x\n")

    def test_chain_config_table_mirrors_gather_forks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "params").mkdir()
            (root / "params" / "config.go").write_text(
                "package params\n\ntype ChainConfig struct {\n"
                "\tChainID *big.Int `json:\"chainId\"`\n"
                "\tHomesteadBlock *big.Int `json:\"homesteadBlock,omitempty\"` // comment\n"
                "\tShanghaiTime   *uint64  `json:\"shanghaiTime,omitempty\"`\n"
                "\tOddTime        *big.Int `json:\"oddTime,omitempty\"`\n"
                "\tTerminalTotalDifficulty *big.Int `json:\"terminalTotalDifficulty,omitempty\"`\n"
                "\tDepositContractAddress common.Address `json:\"depositContractAddress,omitempty\"`\n"
                "\tParlia *ParliaConfig `json:\"parlia,omitempty\"`\n}\n")
            table = driver.chain_config_forks(root)
        self.assertEqual(table["forks"], {"homesteadBlock": "block", "shanghaiTime": "time", "oddTime": "block"})
        self.assertEqual(table["keys"], sorted(["chainId", "homesteadBlock", "shanghaiTime", "oddTime",
                                                "terminalTotalDifficulty", "depositContractAddress", "parlia"]))

    def test_fork_id_mainnet_vectors(self):
        config = {"chainId": 1, "homesteadBlock": 1150000, "daoForkBlock": 1920000, "eip150Block": 2463000,
                  "eip155Block": 2675000, "eip158Block": 2675000, "byzantiumBlock": 4370000,
                  "constantinopleBlock": 7280000, "petersburgBlock": 7280000, "istanbulBlock": 9069000,
                  "muirGlacierBlock": 9200000, "berlinBlock": 12244000, "londonBlock": 12965000}
        genesis = json.dumps({"config": config, "timestamp": "0x0"}).encode()
        mainnet = bytes.fromhex("d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3")
        for head, digest, following in ((0, "fc64ec04", "1150000"), (1149999, "fc64ec04", "1150000"),
                                        (1150000, "97c2c34c", "1920000"), (1920000, "91d1f948", "2463000"),
                                        (2675000, "3edd5b10", "4370000"), (7280000, "668db0af", "9069000"),
                                        (12244000, "0eb440f6", "12965000"), (12965000, "b715077d", "0")):
            self.assertEqual(json.loads(driver.fork_id(genesis, mainnet, head, 0, CHAIN_CONFIG)),
                             {"hash": digest, "next": following})
        # The kind comes from the table, not from the key name.
        table = {"forks": {"londonBlock": "time"}, "keys": ["chainId", "londonBlock"]}
        timed = json.dumps({"config": {"chainId": 1, "londonBlock": 5}, "timestamp": "0x0"}).encode()
        self.assertEqual(json.loads(driver.fork_id(timed, mainnet, 100, 4, table))["next"], "5")
        with self.assertRaises(Rejected):
            driver.fork_id(json.dumps({"config": {"chainId": 1, "x": 1}, "timestamp": "0x0"}).encode(), mainnet, 0, 0,
                           table)
        with self.assertRaises(Rejected):
            driver.fork_id(json.dumps({"config": {"chainId": 1, "londonBlock": "5"}, "timestamp": "0x0"}).encode(),
                           mainnet, 0, 0, table)

    def test_ancestor_floor(self):
        for number, floor in {2: 0, 256: 0, 257: 1, 1024: 768, 1025: 769, 1500: 1244, 2049: 1793}.items():
            self.assertEqual(driver.ancestor_floor(number), floor)

    def test_install_list(self):
        installed = [relative for _, relative in driver.bsc_files()]
        self.assertIn("extractor/bsc/seam.go", installed)
        self.assertIn("extractor/bsc/retention/retention.go", installed)
        go = (driver.GO_DIR / "native_test.go").read_text()
        for mode in driver.CRASH_MODES:
            self.assertIn(f'"{mode}"', go)
        self.assertIn(f'"{driver.PRODUCER}"', go)
        self.assertIn(driver.STORE_ID, go)




def evaluate_long(report=None, traces=None, blocks=LONG_BLOCKS):
    return driver.checked_evaluate(report or copy.deepcopy(LONG_REPORT), traces or copy.deepcopy(EXTRA), blocks,
                                   bytes.fromhex(GENESIS_HASH[2:]), function=driver.evaluate_long)


class LongChain(unittest.TestCase):
    def refused(self, message, **changes):
        with self.assertRaises(Rejected) as caught:
            evaluate_long(**changes)
        self.assertIn(message, str(caught.exception))

    def test_baseline_passes(self):
        summary = evaluate_long()
        self.assertEqual(summary["observed"], driver.LONG_LENGTH - driver.LONG_OBSERVED_FROM + 1)
        self.assertEqual(summary["system_calls"], {str(driver.LONG_EPOCH_BLOCK): 1})
        self.assertEqual(summary["window_of_last_block"], [driver.LONG_LENGTH - 1, driver.LONG_LENGTH - 256])

    def changed(self, run, field, value):
        report = copy.deepcopy(LONG_REPORT)
        report[run][field] = value(report[run][field])
        return report

    def test_report_fields(self):
        self.refused("cold: states", report=self.changed("cold", "states", lambda v: {"EXEC_READY": 35, "ADMITTED": 1}))
        self.refused("warm: system calls", report=self.changed("warm", "system_calls_on_parent", lambda v: {"1001": 1}))
        self.refused("warm: stored checkpoint", report=self.changed("warm", "stored_checkpoint_number", lambda v: 0))
        self.refused("warm: window", report=self.changed("warm", "window_of_last_block", lambda v: [driver.LONG_LENGTH - 1, 1024]))
        self.refused("cold: restarts", report=self.changed("cold", "restarts", lambda v: 0))
        self.refused("Warm and cold imports differ",
                     report=self.changed("cold", "exec_digests", lambda v: dict(v, **{str(driver.LONG_LENGTH): "00" * 32})))
        report = copy.deepcopy(LONG_REPORT)
        report["hashes"]["1024"] = "0x" + "00" * 32
        self.refused("Long chain hash 1024", report=report)

    def test_blocks_link(self):
        items = probe14.split_rlp_list_items(LONG_BLOCKS)
        self.refused("Long chain length", blocks=b"".join(items[:-1]))
        self.refused("does not link", blocks=b"".join(items[:500] + items[501:] + items[500:501]))

    def test_snapshot_rows(self):
        traces = copy.deepcopy(EXTRA)
        row = next(e for e in traces["long-warm"] if e.get("event") == "handoff")
        row["request_hex"] = (driver.SNAPSHOT_TAG + (5).to_bytes(8, "big") + b"\x01" * 32).hex()
        self.refused("snapshot of a header outside the capsule", traces=traces)
        traces = copy.deepcopy(EXTRA)
        rows = [e for e in traces["long-cold"] if e.get("event") == "handoff"]
        rows[-1]["response_sha256"] = "00" * 32
        self.refused("snapshot digests differ from the report", traces=traces)


    def test_snapshot_requested_once(self):
        traces, report = copy.deepcopy(EXTRA), copy.deepcopy(LONG_REPORT)
        events = traces["long-warm"]
        keys = [e["observation"] for e in events if e.get("event") == "begin"]
        key = next(k for k in keys if sum(e.get("event") == "handoff" and e["observation"] == k for e in events) > 1)
        rows = [e for e in events if e.get("event") == "handoff" and e["observation"] == key]
        for field in ("request_hex", "response_hex", "response_sha256"):
            rows[1][field] = rows[0][field]
        number = str(next(e["number"] for e in events if e.get("event") == "begin" and e["observation"] == key))
        report["warm"]["snapshot_response_sha256"][number][1] = rows[0]["response_sha256"]
        self.refused("snapshot requested twice", report=report, traces=traces)


class ParentState(unittest.TestCase):
    def evaluate(self, events):
        return driver.checked_evaluate(events, function=driver.evaluate_parent_state)

    def test_baseline_passes(self):
        self.assertEqual(self.evaluate(copy.deepcopy(EXTRA["parent-state"]))["key"], driver.key_of(0))

    def test_refusals(self):
        key = driver.key_of(0)
        base = EXTRA["parent-state"]
        for message, events in {
            "was rejected": base + [{"event": "rejected", "observation": key}],
            "did not stop on the parent state": [dict(e, error="other") if e.get("event") == "observation-error" else e
                                                 for e in base],
            "recovery did not resolve": [e for e in base if e.get("event") != "exec-ready"],
        }.items():
            with self.assertRaises(Rejected) as caught:
                self.evaluate(copy.deepcopy(events))
            self.assertIn(message, str(caught.exception))


if __name__ == "__main__":
    unittest.main()
