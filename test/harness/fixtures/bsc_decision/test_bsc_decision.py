#!/usr/bin/env python3
"""Portable controls of the BSC decision driver (test/harness/bsc_decision.py).

The sample in this directory is the report and the traces of a native probe
run on the accepted private BSC fixture (see README.md). The baseline must
pass the driver's evaluation; every control changes one fact and must be
refused with the matching message. Where a control changes a record, it
recomputes that record's own digest with the shared decision and admission
references, so only the intended check can refuse it.
"""

import copy
import json
from pathlib import Path
import sys
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1]))
import admission  # noqa: E402
from artifacts import Rejected  # noqa: E402
import bsc_decision as driver  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402
import decision  # noqa: E402

# The fixture inputs are the files of the BSC admission sample.
INPUTS = HERE.parent / "bsc_admission"
GENESIS = (INPUTS / "genesis.json").read_bytes()
RAW = probe14.split_rlp_list_items((INPUTS / "blocks.rlp").read_bytes())
GENESIS_HASH = bytes.fromhex(json.loads((HERE / "genesis-hash.json").read_text())["genesis_hash"])
REPORT = json.loads((HERE / "report.json").read_text())
TRACES = {name: [json.loads(line) for line in (HERE / f"trace-{name}.jsonl").read_text().splitlines()]
          for name in driver.TRACES}


def evaluate(report=None, traces=None, genesis=GENESIS, raw=RAW, genesis_hash=GENESIS_HASH):
    return driver.checked_evaluate(report or copy.deepcopy(REPORT), traces or copy.deepcopy(TRACES), raw,
                                   genesis_hash, genesis)


def scenario(report, name):
    return next(row for row in report["scenarios"] if row["name"] == name)


def events(traces, name, event, **match):
    return [e for e in traces[name] if e.get("event") == event and all(e.get(k) == v for k, v in match.items())]


def first_put(traces, name, scope, record, sequence=0):
    return next(e for e in traces[name] if e.get("event") == "put" and e.get("scope") == scope and
                e.get("record") == record and json.loads(e["value"])["sequence"] == sequence)


def insert_before_reimport(traces, name, event):
    """Insert an event at the end of the recovery segment, before the re-import restart."""
    trace = traces[name]
    last = max(i for i, e in enumerate(trace) if e.get("event") == "restart")
    trace.insert(last, copy.deepcopy(event))


def rewrite_intent(traces, name, change):
    """Change the intent record of decision 0 in a trace and recompute its digest."""
    put = first_put(traces, name, "decision", "intent")
    record = json.loads(put["value"])
    change(record)
    record["digest"] = decision.intent_digest(record)
    put["value"] = json.dumps(record, separators=(",", ":"))


class BaselineTest(unittest.TestCase):
    def test_sample_passes(self):
        summary = evaluate()
        self.assertEqual(summary["live"], {"decisions": 2, "obligations": [1], "markers": [1]})
        self.assertEqual(summary["native-rejection"]["obligations"], [])
        self.assertEqual({summary[mode]["ac"] for mode in driver.CRASH_MODES},
                         {"AC-CAP-07-01", "AC-CAP-07-02", "AC-CAP-07-03", "AC-CAP-07-06"})


class ControlTest(unittest.TestCase):
    def refused(self, message, report=None, traces=None, genesis=GENESIS, raw=RAW, genesis_hash=GENESIS_HASH):
        with self.assertRaises(Rejected) as caught:
            evaluate(report, traces, genesis, raw, genesis_hash)
        self.assertIn(message, str(caught.exception))

    def traces(self):
        return copy.deepcopy(TRACES)

    def report(self):
        return copy.deepcopy(REPORT)

    # ---- digests ----

    def test_evidence_digest(self):
        traces = self.traces()
        events(traces, "live", "evidence")[0]["digest"] = "00" * 32
        self.refused("Evidence digest", traces=traces)

    def test_finality_result(self):
        traces = self.traces()
        event = events(traces, "live", "evidence")[0]
        event["finalized"] = event["new_head"]
        event["digest"] = driver.evidence_digest("IMPORT", event["old_head"], event["new_head"], event["justified"],
                                                 event["finalized"])
        self.refused("Finality result", traces=traces)

    def test_intent_evidence(self):
        traces = self.traces()
        rewrite_intent(traces, "intent-synced", lambda r: r.update(evidence_digest="11" * 32))
        self.refused("Intent 0 evidence digest", traces=traces)

    def test_intent_child_root(self):
        traces = self.traces()
        rewrite_intent(traces, "intent-synced", lambda r: r["members"][0].update(child_root="22" * 32))
        self.refused("member block or child root", traces=traces)

    def test_intent_heads(self):
        traces = self.traces()
        rewrite_intent(traces, "intent-synced", lambda r: r["new_head"].update(number=7))
        self.refused("Intent 0 heads", traces=traces)

    def test_intent_kind(self):
        traces = self.traces()
        rewrite_intent(traces, "intent-synced", lambda r: r.update(kind="SIDE", events=False))
        self.refused("is not an IMPORT", traces=traces)

    def test_intent_digest(self):
        traces = self.traces()
        put = first_put(traces, "intent-synced", "decision", "intent")
        record = json.loads(put["value"])
        record["digest"] = "33" * 32
        put["value"] = json.dumps(record)
        self.refused("Intent 0 digest", traces=traces)

    def test_intent_identity(self):
        traces = self.traces()
        rewrite_intent(traces, "intent-synced", lambda r: r.update(native_store_id="other-store"))
        self.refused("Intent 0 identity", traces=traces)

    def test_decided_digest(self):
        traces = self.traces()
        put = first_put(traces, "intent-synced", "decision", "decided")
        record = json.loads(put["value"])
        record["digest"] = "44" * 32
        put["value"] = json.dumps(record)
        self.refused("NATIVE_DECIDED 0", traces=traces)

    def test_admission_decided(self):
        traces = self.traces()
        put = next(e for e in traces["live"] if e.get("event") == "put" and e.get("scope") == "admission" and
                   e.get("record") == "decided")
        record = json.loads(put["value"])
        record["decision_digest"] = "55" * 32
        record["digest"] = admission.decided_digest(record["exec_ready_digest"], record["decision_digest"])
        put["value"] = json.dumps(record)
        self.refused("Admission DECIDED", traces=traces)

    def test_member_exec_ready(self):
        traces = self.traces()
        rewrite_intent(traces, "exec-ready-synced", lambda r: r["members"][0].update(exec_ready_digest="66" * 32))
        self.refused("member EXEC_READY digest", traces=traces)

    def test_marker_bytes(self):
        report = self.report()
        sc = scenario(report, "intent-synced")
        data = bytearray.fromhex(sc["markers"]["0000000000000000"])
        data[-3] ^= 1
        sc["markers"]["0000000000000000"] = data.hex()
        self.refused("Marker 0 bytes", report=report)

    def test_obligation_digest(self):
        report = self.report()
        scenario(report, "intent-synced")["obligations"][0]["decided_digest"] = "77" * 32
        self.refused("Obligation 0 digests", report=report)

    def test_obligation_twice(self):
        report = self.report()
        sc = scenario(report, "intent-synced")
        sc["obligations"].append(copy.deepcopy(sc["obligations"][0]))
        self.refused("obligations", report=report)

    def test_marker_after_rejection(self):
        report = self.report()
        live = scenario(report, "live")
        scenario(report, "native-rejection")["markers"] = {"0000000000000001": live["markers"]["0000000000000001"]}
        self.refused("Marker 1 has no intent", report=report)

    # ---- order ----

    def test_native_write_before_intent(self):
        traces = self.traces()
        trace = traces["live"]
        index = trace.index(events(traces, "live", "native-block")[0])
        intent = trace.index(events(traces, "live", "intent")[0])
        trace.insert(intent, trace.pop(index))
        self.refused("before the intent", traces=traces)

    def test_intent_without_barrier(self):
        traces = self.traces()
        trace = traces["live"]
        trace.remove(events(traces, "live", "barrier")[0])
        self.refused("without the barrier", traces=traces)

    def test_recovery_order(self):
        traces = self.traces()
        traces["exec-ready-synced"].remove(events(traces, "exec-ready-synced", "replayed")[0])
        self.refused("recovery order", traces=traces)

    def test_live_order(self):
        traces = self.traces()
        traces["live"].remove(events(traces, "live", "decided")[0])
        self.refused("live: protocol order", traces=traces)

    def test_child_events(self):
        traces = self.traces()
        trace = traces["block-written"]
        trace.remove(events(traces, "block-written", "native-block")[0])
        self.refused("the child events", traces=traces)

    def test_crash_marker(self):
        traces = self.traces()
        events(traces, "head-written", "crash")[0]["point"] = "block-written"
        self.refused("crash marker", traces=traces)

    def test_bad_block_fixture(self):
        traces = self.traces()
        traces["native-rejection"].remove(events(traces, "native-rejection", "native-bad-block")[0])
        self.refused("bad-block fixture", traces=traces)

    # ---- one decision per block and the EXEC_READY record ----

    def test_second_intent(self):
        traces = self.traces()
        put = first_put(traces, "intent-synced", "decision", "intent")
        restart = next(i for i, e in enumerate(traces["intent-synced"]) if e.get("event") == "restart")
        traces["intent-synced"].insert(restart + 1, copy.deepcopy(put))
        self.refused("more than one intent", traces=traces)

    def test_exec_ready_rewritten(self):
        traces = self.traces()
        put = first_put(traces, "exec-ready-synced", "admission", "exec-ready")
        restart = next(i for i, e in enumerate(traces["exec-ready-synced"]) if e.get("event") == "restart")
        traces["exec-ready-synced"].insert(restart + 1, copy.deepcopy(put))
        self.refused("EXEC_READY writes", traces=traces)

    def test_replay_differs(self):
        traces = self.traces()
        events(traces, "exec-ready-synced", "replayed")[0]["exec_digest"] = "88" * 32
        self.refused("replay differs", traces=traces)

    def test_resumed_intent(self):
        traces = self.traces()
        events(traces, "intent-synced", "intent-resumed")[0]["digest"] = "99" * 32
        self.refused("resumed intent", traces=traces)

    def test_decided_twice(self):
        traces = self.traces()
        put = first_put(traces, "intent-synced", "decision", "decided")
        insert_before_reimport(traces, "intent-synced", put)
        self.refused("NATIVE_DECIDED writes of block 1", traces=traces)

    # ---- restart state ----

    def test_child_root_on_disk(self):
        report = self.report()
        scenario(report, "exec-ready-synced")["block1_root_on_disk_at_restart"] = True
        self.refused("child root or head at the restart", report=report)

    def test_head_at_restart(self):
        report = self.report()
        scenario(report, "head-written")["head_at_restart"] = 0
        self.refused("child root or head at the restart", report=report)

    def test_observation_at_restart(self):
        report = self.report()
        scenario(report, "first-handoff")["observations_after_restart"][0]["state"] = "EXEC_READY"
        self.refused("observation at the restart", report=report)

    def test_replay_on_moved_head(self):
        report = self.report()
        scenario(report, "head-written")["extra"]["replay_on_moved_head"] = ""
        self.refused("replay on a moved head", report=report)

    def test_settle_stored(self):
        traces = self.traces()
        events(traces, "block-written", "settle")[0]["stored"] = False
        self.refused("stored at the settle", traces=traces)

    def test_pending_refusals(self):
        report = self.report()
        scenario(report, "intent-synced")["extra"]["other_block_while_pending"] = ""
        self.refused("did not refuse", report=report)

    # ---- rejection ----

    def test_rejection_evidence(self):
        traces = self.traces()
        events(traces, "native-rejection", "settle")[0]["evidence"] = "aa" * 32
        self.refused("bad-block evidence", traces=traces)

    def test_rejection_record(self):
        traces = self.traces()
        put = first_put(traces, "native-rejection", "decision", "rejected")
        record = json.loads(put["value"])
        record["digest"] = "bb" * 32
        put["value"] = json.dumps(record)
        self.refused("rejected intent record", traces=traces)

    def test_rejection_event(self):
        traces = self.traces()
        events(traces, "native-rejection", "intent-rejected")[0]["digest"] = "cc" * 32
        self.refused("rejection event", traces=traces)

    def test_decision_after_rejection(self):
        traces = self.traces()
        live = first_put(traces, "live", "decision", "decided")
        insert_before_reimport(traces, "native-rejection", live)
        self.refused("rejected intent record", traces=traces)

    # ---- final state and re-import ----

    def test_final_head(self):
        report = self.report()
        scenario(report, "barrier-synced")["head_number"] = 1
        self.refused("final head", report=report)

    def test_capsule_left(self):
        report = self.report()
        scenario(report, "barrier-synced")["capsules"] = [f"{driver.OWNER}.0000000000000000"]
        self.refused("open observations, capsules or pins", report=report)

    def test_reimport_counter(self):
        report = self.report()
        scenario(report, "live")["reimport"]["decision_counter"][1] += 1
        self.refused("re-import", report=report)

    def test_reimport_observed(self):
        traces = self.traces()
        traces["live"].append({"event": "begin", "observation": "x", "number": 1})
        self.refused("observed or decided a block", traces=traces)

    def test_mirror_applied_other(self):
        report = self.report()
        scenario(report, "live")["extra"]["applied_decision_0"] = "dd" * 32
        self.refused("the mirror applied another decision", report=report)

    def test_import_before_recovery(self):
        report = self.report()
        scenario(report, "live")["extra"]["import_before_recovery"] = ""
        self.refused("import before recovery was not refused", report=report)

    def test_guard(self):
        report = self.report()
        scenario(report, "live")["extra"]["begin_without_guard"] = ""
        self.refused("without the guard was not refused", report=report)

    def test_scenarios(self):
        report = self.report()
        report["scenarios"].pop()
        self.refused("Scenarios", report=report)

    def test_plato(self):
        genesis = json.loads(GENESIS)
        genesis["config"]["platoBlock"] = 2
        self.refused("before Plato", genesis=json.dumps(genesis).encode())

    # ---- fixture, records and segments ----

    def test_fixture_blocks(self):
        self.refused("Fixture must hold two blocks", raw=RAW[:1])

    def test_fixture_numbers(self):
        self.refused("Fixture block numbers", raw=RAW[::-1])

    def test_fixture_genesis(self):
        self.refused("not linked to the genesis", genesis_hash=bytes(32))

    def test_intent_members(self):
        traces = self.traces()
        rewrite_intent(traces, "intent-synced", lambda r: r.update(members=r["members"] * 2))
        self.refused("Intent 0 members", traces=traces)

    def test_evidence_heads(self):
        traces = self.traces()
        event = events(traces, "live", "evidence")[0]
        event["old_head"] = event["new_head"]
        event["digest"] = driver.evidence_digest("IMPORT", event["old_head"], event["new_head"], event["justified"],
                                                 event["finalized"])
        self.refused("Evidence heads", traces=traces)

    def test_admission_decided_missing(self):
        traces = self.traces()
        put = first_put(traces, "intent-synced", "admission", "decided")
        traces["intent-synced"].remove(put)
        self.refused("No admission DECIDED record", traces=traces)

    def test_reimport_head(self):
        report = self.report()
        scenario(report, "live")["reimport"]["head"] = 1
        self.refused("re-import head", report=report)

    def test_reimport_restart(self):
        traces = self.traces()
        trace = traces["live"]
        last = max(i for i, e in enumerate(trace) if e.get("event") == "restart")
        del trace[next(i for i in range(last, len(trace)) if trace[i].get("event") == "bridge-open")]
        self.refused("restart of the re-import", traces=traces)

    def test_live_import_error(self):
        report = self.report()
        scenario(report, "live")["import_errors"] = ["refused"]
        self.refused("live: import error", report=report)

    def test_live_decisions(self):
        traces = self.traces()
        traces["live"] = [e for e in traces["live"] if not (e.get("event") == "put" and e.get("scope") == "decision"
                                                            and e.get("owner", "").endswith("/0000000000000001"))]
        self.refused("live: decisions", traces=traces)

    def test_live_member_exec_ready(self):
        traces = self.traces()
        put = first_put(traces, "live", "admission", "exec-ready")
        record = json.loads(put["value"])
        record["digest"] = "ee" * 32
        put["value"] = json.dumps(record)
        self.refused("live: member EXEC_READY", traces=traces)

    def test_live_applied_marker(self):
        report = self.report()
        intent = json.loads(first_put(TRACES, "live", "decision", "intent")["value"])
        scenario(report, "live")["markers"]["0000000000000000"] = decision.marker_value(intent).hex()
        self.refused("the applied decision kept its marker", report=report)

    def test_live_records(self):
        report = self.report()
        scenario(report, "live")["decision_records"]["0000000000000000/intent"] = {}
        self.refused("records of the applied decision", report=report)

    def test_mode_at_restart(self):
        report = self.report()
        scenario(report, "intent-synced")["mode_at_start"] = "LIVE"
        self.refused("mode at the restart", report=report)

    def test_recovery_error(self):
        report = self.report()
        scenario(report, "intent-synced")["recover_error"] = "failed"
        self.refused("recovery error", report=report)

    def test_native_write_after_rejection(self):
        traces = self.traces()
        insert_before_reimport(traces, "native-rejection", {"event": "decided", "observation": "x", "sequence": 0})
        self.refused("a native write or decision after the rejection", traces=traces)

    def test_import_after_rejection(self):
        report = self.report()
        scenario(report, "native-rejection")["import_errors"] = [""]
        self.refused("records after the rejection", report=report)

    def test_import_after_recovery(self):
        report = self.report()
        scenario(report, "intent-synced")["import_errors"] = ["refused"]
        self.refused("live import after recovery", report=report)

    def test_block_2_decided(self):
        traces = self.traces()
        traces["intent-synced"] = [e for e in traces["intent-synced"]
                                   if not (e.get("event") == "put" and e.get("scope") == "decision" and
                                           e.get("owner", "").endswith("/0000000000000001"))]
        self.refused("intent-synced: decisions", traces=traces)

    def test_malformed(self):
        report = self.report()
        del scenario(report, "live")["markers"]
        self.refused("Malformed probe report", report=report)


if __name__ == "__main__":
    unittest.main()
