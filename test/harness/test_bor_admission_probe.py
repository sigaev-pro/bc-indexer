#!/usr/bin/env python3
"""Portable tests of the E01-02.admission Bor driver, judge and tree installer.

The judge tests run on judge-sample.tar.gz: the real outputs of the Bor probe
scenarios (fixture chain, scenario records, store dumps). Each negative test
changes one observed value and requires the judge to name that difference.
"""

import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import bor_admission_probe as probe
import bor_admission_tree as tree

FIXTURE = Path(__file__).with_name("fixtures") / "admission_bor"
ROOT = Path(__file__).resolve().parents[2]
COMMIT = "3a018e38467594508ca31ebeaf804837fb92834a"


def load_sample():
    files = {}
    with tarfile.open(FIXTURE / "judge-sample.tar.gz") as stream:
        for member in stream:
            if member.isfile():
                files[member.name.removeprefix("./")] = stream.extractfile(member).read()
    return files


SAMPLE = load_sample()
ORACLE = json.loads((FIXTURE / "expected.json").read_text())
BODIES = {path.name: path.read_bytes() for path in (FIXTURE / "stub").iterdir()}


def scenario(name):
    data = json.loads(SAMPLE[f"scenarios/{name}/scenario.json"])
    for index, row in enumerate(data["attempts"]):
        for key, file in (("result", f"attempt-{index}.json"), ("crash_result", f"attempt-{index}.json.crash.json")):
            raw = SAMPLE.get(f"scenarios/{name}/{file}")
            row[key] = json.loads(raw) if raw is not None else None
    return data


def base_context():
    genesis, config = probe.derived_inputs(SAMPLE["source-genesis.json"], ORACLE)
    return probe.build_context(ORACLE, BODIES, genesis, config, SAMPLE["chain/blocks.rlp"],
                               SAMPLE["chain/genesis-header.rlp"], COMMIT, json.loads(SAMPLE["chain/generated.json"]))


CACHE = {}


SCENARIOS = {plan["name"]: scenario(plan["name"]) for plan in ORACLE["plans"]}


def contexts():
    """Built on first use, so a failure names the test that needed it."""
    if not CACHE:
        base = base_context()
        CACHE.update({plan["name"]: {**base, "checkpoints": probe.checkpoints(
            json.loads(SAMPLE[f"scenarios/{plan['name']}/checkpoints.json"]), SCENARIOS[plan["name"]])}
            for plan in ORACLE["plans"]})
    return CACHE


def judge(name, change=None):
    observed = copy.deepcopy(SCENARIOS[name])
    ctx = dict(contexts()[name])
    if change:
        change(observed, ctx)
    return probe.checked_compare(name, observed, ctx)


def observations_by_block(result):
    return {o["block"]: o for o in result["observations"]}


def row_of(dump, suffix):
    return next(row for row in dump if row["key"].endswith(suffix))


def set_value(dump, suffix, update):
    row = row_of(dump, suffix)
    record = json.loads(row["value"])
    update(record)
    row["value"] = json.dumps(record)


def snap_rows(result, key):
    return result["snapshots"][key]["rows"]


def intent_row(dump, key):
    """The decision intent row whose member is observation `key`."""
    return next(row for row in dump if row["key"].startswith(probe.DECISION) and row["key"].endswith("/intent") and
                any(m["observation"] == key for m in json.loads(row["value"])["members"]))


def decision_row(dump, key, kind):
    seq = json.loads(intent_row(dump, key)["value"])["sequence"]
    return next(row for row in dump if row["key"] == f"{probe.DECISION}{seq:016x}/{kind}")


def marker_row(dump, key):
    seq = json.loads(intent_row(dump, key)["value"])["sequence"]
    return next(row for row in dump if row["key"] == f"{probe.MARKER}{seq:016x}")


def update_json(row, update):
    record = json.loads(row["value"])
    update(record)
    row["value"] = json.dumps(record)


class JudgeTests(unittest.TestCase):
    def assertFails(self, name, change, text):
        failures = judge(name, change)
        self.assertTrue(any(text in failure for failure in failures), f"{text!r} not in {failures}")

    def test_every_sample_scenario_passes(self):
        for plan in ORACLE["plans"]:
            self.assertEqual(judge(plan["name"]), [], plan["name"])

    def test_plan_and_exit_status(self):
        def plan(o, c):
            o["plan"]["attempts"][0]["to"] = 7
        self.assertFails("native-rejection", plan, "did not run the oracle plan")

        def fewer(o, c):
            o["attempts"].pop()
        self.assertFails("native-rejection", fewer, "did not run the oracle plan")

        def not_lost(o, c):
            o["attempts"][0]["status"]["signal"] = ""
        self.assertFails("ac-cap-02-01-begin", not_lost, "was not lost at its crash point")

        def no_crash_result(o, c):
            o["attempts"][0]["crash_result"] = None
        self.assertFails("ac-cap-02-01-begin", no_crash_result, "was not lost at its crash point")

        def accepted(o, c):
            o["attempts"][2]["status"]["exit_code"] = 0
        self.assertFails("checkpoint-rio", accepted, "expected a refusal")

        def failed(o, c):
            o["attempts"][1]["status"]["exit_code"] = 1
        self.assertFails("native-rejection", failed, "exited with")

    def test_counted_calls(self):
        def first(o, c):
            return o["attempts"][1]["result"]["observations"][0]["counted_calls"]

        def request(o, c):
            first(o, c)[0]["request"] = "GET /bor/spans/2"
        self.assertFails("native-rejection", request, "call 0 is")

        def digest(o, c):
            first(o, c)[0]["request_sha256"] = "00" * 32
        self.assertFails("native-rejection", digest, "request digest differs")

        def error(o, c):
            first(o, c)[1]["error"] = "boom"
        self.assertFails("native-rejection", error, "call 1 failed")

        def response(o, c):
            first(o, c)[2]["response_sha256"] = c["body_sha256"]["clerk-b-from-1.json"]
        self.assertFails("native-rejection", response, "returned bytes that differ")

        def origin(o, c):
            first(o, c)[3]["origin"] = "replay"
        self.assertFails("native-rejection", origin, "origin replay")

        def count(o, c):
            first(o, c).pop()
        self.assertFails("native-rejection", count, "3 counted calls, expected 4")

        def sequence(o, c):
            first(o, c)[1]["sequence"] = 0
        self.assertFails("native-rejection", sequence, "call 1 is")

    def test_observation_records(self):
        """The EXEC_READY records are checked on the snapshot taken at their sync."""
        key4, key5 = probe.key_of(4), probe.key_of(5)

        def snap(o, key=key4):
            return snap_rows(o["attempts"][1]["result"], key)

        def candidate(o, c):
            set_value(snap(o), "0000000000000004/preparing", lambda r: r["candidate"].update(parent_root="00" * 32))
        self.assertFails("native-rejection", candidate, "is not the observation of block 4")

        def capsule(o, c):
            set_value(snap(o), "bor.run-1.0000000000000004/capsule", lambda r: r["items"][1].update(sha256="00" * 32))
        self.assertFails("native-rejection", capsule, "capsule differs from the independent items")

        def admitted(o, c):
            set_value(snap(o), "0000000000000004/admitted", lambda r: r.update(capsule_digest="00" * 32))
        self.assertFails("native-rejection", admitted, "ADMITTED record differs")

        def pin_digest(o, c):
            row_of(snap(o), "pin-v1-bor.run-1.0000000000000004")["value_sha256"] = "00" * 32
        self.assertFails("native-rejection", pin_digest, "pin digest is not the ledger record digest")

        def digest(o, c):
            set_value(snap(o), "0000000000000004/admitted", lambda r: r.update(digest="00" * 32))
        self.assertFails("native-rejection", digest, "ADMITTED digest does not recompute")

        for field in ("consumed_digest", "digest", "exec_digest"):
            def ready(o, c, field=field):
                set_value(snap(o), "0000000000000004/exec-ready", lambda r: r.update({field: "00" * 32}))
            self.assertFails("native-rejection", ready, "EXEC_READY does not bind")

        def not_ready(o, c):
            rows = snap(o, key5)
            rows.remove(row_of(rows, "0000000000000005/exec-ready"))
        self.assertFails("native-rejection", not_ready, "block 5 did not reach EXEC_READY")

        def no_snapshot(o, c):
            del o["attempts"][1]["result"]["snapshots"][key5]
        self.assertFails("native-rejection", no_snapshot, "block 5 did not reach EXEC_READY")

        def closed(o, c):
            set_value(snap(o), "bor.run-1.0000000000000004/closed", lambda r: r.update(digest="00" * 32))
        self.assertFails("native-rejection", closed, "closed manifest differs")

        def lost_pin(o, c):
            rows = snap(o)
            rows.remove(row_of(rows, "pin-v1-bor.run-1.0000000000000004"))
        self.assertFails("native-rejection", lost_pin, "EXEC_READY lost its pin")

        def rejected_ready(o, c):
            rows = snap_rows(o["attempts"][0]["result"], probe.key_of(3))
            ready = copy.deepcopy(row_of(snap_rows(o["attempts"][0]["result"], probe.key_of(2)), "0000000000000002/exec-ready"))
            ready["key"] = ready["key"].replace("0000000000000002", "0000000000000003")
            rows.append(ready)
        self.assertFails("native-rejection", rejected_ready, "NATIVE_REJECTED kept EXEC_READY")

        def checkpoint(o, c):
            c["checkpoints"] = {key: b"{}" for key in c["checkpoints"]}
        self.assertFails("native-rejection", checkpoint, "checkpoint-snapshot item does not follow")

        def snapshot_checkpoints(o, c):
            c["checkpoints"] = probe.checkpoints({})
        self.assertFails("native-rejection", snapshot_checkpoints, "checkpoint-snapshot item does not follow")

        def head(o, c):
            o["attempts"][1]["result"]["head_hash"] = "0x" + "00" * 32
        self.assertFails("native-rejection", head, "the imported head is not block 6")

        def missing(o, c):
            o["attempts"][1]["result"]["observations"][1]["error"] = "x"
        self.assertFails("native-rejection", missing, "block 5 was not observed and imported")

    def test_decisions(self):
        """Every imported block has one IMPORT decision computed here."""
        name, key = "known-block-restart", probe.key_of(2)

        def dump(o):
            return o["attempts"][0]["store_after"]

        for change in (lambda r: r.update(kind="CANONICAL"), lambda r: r.update(events=False),
                       lambda r: r["members"][0].update(child_root="00" * 32),
                       lambda r: r["members"][0].update(exec_ready_digest="00" * 32),
                       lambda r: r.update(evidence_digest="00" * 32), lambda r: r["old_head"].update(number=9)):
            def intent(o, c, change=change):
                update_json(intent_row(dump(o), key), change)
            self.assertFails(name, intent, "differs from the independent IMPORT intent")

        def delivery(o, c):
            update_json(decision_row(dump(o), key, "decided"), lambda r: r.update(delivery="CHECKPOINT"))
        self.assertFails(name, delivery, "is not NATIVE_DECIDED with one BATCH obligation")

        def undecided(o, c):
            dump(o).remove(decision_row(dump(o), key, "decided"))
        self.assertFails(name, undecided, "is not NATIVE_DECIDED with one BATCH obligation")

        def marker(o, c):
            marker_row(dump(o), key)["value"] = "{}"
        self.assertFails(name, marker, "native marker of block 3 is not the intent bytes")

        def no_marker(o, c):
            dump(o).remove(marker_row(dump(o), key))
        self.assertFails(name, no_marker, "native marker of block 3 is not the intent bytes")

        def twice(o, c):
            row = copy.deepcopy(intent_row(dump(o), key))
            row["key"] = row["key"].replace("/0000000000000002/", "/00000000000000ff/")
            dump(o).append(row)
        self.assertFails(name, twice, "block 3 has decisions")

        def none(o, c):
            dump(o).remove(intent_row(dump(o), key))
        self.assertFails(name, none, "block 3 has decisions [] over")

        def kept_pin(o, c):
            dump(o).append({"key": probe.PIN + key, "value_sha256": "00" * 32, "value_length": 1})
        self.assertFails(name, kept_pin, "kept a pin, inputs or another admission record")

        def kept_inputs(o, c):
            dump(o).append({"key": probe.INPUTS + key + "/capsule", "value_sha256": "00" * 32, "value_length": 1,
                            "value": "{}"})
        self.assertFails(name, kept_inputs, "kept a pin, inputs or another admission record")

        def kept_record(o, c):
            dump(o).append({"key": probe.ADMISSION + "0000000000000002/exec-ready", "value_sha256": "00" * 32,
                            "value_length": 2, "value": "{}"})
        self.assertFails(name, kept_record, "kept a pin, inputs or another admission record")

        def obligations(o, c):
            o["attempts"][1]["result"]["obligations"].pop()
        self.assertFails(name, obligations, "are not the BATCH decisions")

        def same_block(o, c):
            final = o["attempts"][1]["store_after"]
            other = intent_row(final, probe.key_of(3))
            update_json(other, lambda r: r["members"][0].update(block=json.loads(intent_row(final, key)["value"])[
                "members"][0]["block"]))
        self.assertFails(name, same_block, "a block has two decisions")

    def test_lost_before_admitted(self):
        def state(o, c):
            dump = o["attempts"][0]["store_after"]
            admitted = copy.deepcopy(row_of(snap_rows(o["attempts"][0]["crash_result"], probe.key_of(2)),
                                            "0000000000000002/admitted"))
            admitted["key"] = admitted["key"].replace("0000000000000002", "0000000000000003")
            dump.append(admitted)
        self.assertFails("ac-cap-02-01-seal", state, "the lost observation is ADMITTED")

        def pinned(o, c):
            o["attempts"][0]["store_after"].append({"key": probe.PIN + probe.key_of(3), "value_sha256": "00" * 32,
                                                    "value_length": 1})
        self.assertFails("ac-cap-02-01-begin", pinned, "after a loss at begin the pin is True")

        def sealed(o, c):
            dump = o["attempts"][0]["store_after"]
            dump.remove(row_of(dump, "bor.run-1.0000000000000003/capsule"))
        self.assertFails("ac-cap-02-01-seal", sealed, "after a loss at seal the pin is True and the capsule False")

        def logged(o, c):
            o["attempts"][0]["store_after"].append({"key": probe.INPUTS + probe.key_of(3) + "/log/0000000000000000",
                                                    "value_sha256": "00" * 32})
        self.assertFails("ac-cap-02-01-begin", logged, "durable log entries are [0]")

        def reopened(o, c):
            o["attempts"][1]["result"]["pending_at_open"].append("x")
        self.assertFails("ac-cap-02-01-begin", reopened, "did not reopen the saved run")

        def golive(o, c):
            o["attempts"][1]["result"]["golive_before_recovery"] = "<nil>"
        self.assertFails("ac-cap-02-01-pin", golive, "GoLive was not refused")

        def begin(o, c):
            o["attempts"][1]["result"]["begin_before_recovery"] = "<nil>"
        self.assertFails("ac-cap-02-01-pin", begin, "Begin was not refused")

        def recovery(o, c):
            o["attempts"][1]["result"]["recovery"]["abandoned"] = []
        self.assertFails("ac-cap-02-01-begin", recovery, "did not abandon only")

        def kept_pin(o, c):
            o["attempts"][1]["store_after"].append({"key": probe.PIN + probe.key_of(3), "value_sha256": "00" * 32,
                                                    "value_length": 1})
        self.assertFails("ac-cap-02-01-pin", kept_pin, "the abandoned observation kept a pin")

        def not_abandoned(o, c):
            rows = snap_rows(o["attempts"][1]["result"], probe.key_of(3))
            rows.remove(row_of(rows, "0000000000000003/abandoned"))
        self.assertFails("ac-cap-02-01-pin", not_abandoned, "or was not ABANDONED")

        def decided(o, c):
            o["attempts"][1]["result"]["decision_recovery"]["resolved"] = [3]
        self.assertFails("ac-cap-02-01-pin", decided, "the decision recovery did")

        def lost_decision(o, c):
            dump = o["attempts"][0]["store_after"]
            row = copy.deepcopy(intent_row(dump, probe.key_of(2)))
            update_json(row, lambda r: r["members"][0].update(observation=probe.key_of(3)))
            row["key"] = row["key"].replace("/0000000000000002/", "/00000000000000ff/")
            dump.append(row)
        self.assertFails("ac-cap-02-01-pin", lost_decision, "has a decision before the restart")

    def test_lost_after_admitted(self):
        """AC-CAP-02-02 and AC-CAP-07-01."""
        def result(o):
            return o["attempts"][1]["result"]

        def recovery(o, c):
            result(o)["recovery"]["exec_ready"] = []
        self.assertFails("ac-cap-02-02-log", recovery, "did not resolve the ADMITTED obligation")

        def replayed(o, c):
            result(o)["recovery_counted_calls"][0]["origin"] = "fetch"
        self.assertFails("ac-cap-02-02-log", replayed, "recovery: call 0 origin fetch")
        self.assertFails("ac-cap-02-02-finish", replayed, "recovery: call 0 origin fetch")

        def fetched(o, c):
            result(o)["recovery_counted_calls"][3]["origin"] = "replay"
        self.assertFails("ac-cap-02-02-log", fetched, "recovery: call 3 origin replay")

        def completion(o, c):
            result(o)["recovery_counted_calls"][4]["origin"] = "fetch"
        self.assertFails("ac-cap-02-02-log", completion, "completion replay: call 0 origin fetch")

        def changed(o, c):
            result(o)["recovery_counted_calls"].append({"response_sha256": c["body_sha256"]["clerk-b-from-1.json"]})
        self.assertFails("ac-cap-02-02-log", changed, "changed external response reached recovery")

        def admitted(o, c):
            set_value(snap_rows(result(o), probe.key_of(3)), "0000000000000003/admitted",
                      lambda r: r.update(pin_digest="11" * 32))
        self.assertFails("ac-cap-02-02-log", admitted, "recovery changed the ADMITTED record")

        def not_ready(o, c):
            rows = snap_rows(result(o), probe.key_of(3))
            rows.remove(row_of(rows, "0000000000000003/exec-ready"))
        self.assertFails("ac-cap-02-02-log", not_ready, "the recovered obligation is not EXEC_READY")

        def saved(o, c):
            dump = o["attempts"][0]["store_after"]
            dump.remove(row_of(dump, "bor.run-1.0000000000000003/log/0000000000000002"))
        self.assertFails("ac-cap-02-02-log", saved, "durable log entries are [0, 1]")

        def lost_decision(o, c):
            dump = o["attempts"][0]["store_after"]
            row = copy.deepcopy(intent_row(dump, probe.key_of(2)))
            update_json(row, lambda r: r["members"][0].update(observation=probe.key_of(3)))
            row["key"] = row["key"].replace("/0000000000000002/", "/00000000000000ff/")
            dump.append(row)
        self.assertFails("ac-cap-02-02-finish", lost_decision, "has a decision before the restart")

        def stored(o, c):
            result(o)["stored_at_open"].append(4)
        self.assertFails("ac-cap-02-02-finish", stored, "the lost block was stored before the recovery")

        def report(o, c):
            result(o)["decision_recovery"]["imported"] = []
        self.assertFails("ac-cap-02-02-finish", report, "the decision recovery did")

        def observed_again(o, c):
            result(o)["observations"].insert(0, {"block": 4, "key": probe.key_of(4), "counted_calls": []})
        self.assertFails("ac-cap-02-02-finish", observed_again, "was observed again by the live import")

        def third(o, c):
            o["attempts"][2]["result"]["head_hash"] = "0x" + "00" * 32
        self.assertFails("ac-cap-02-02-finish", third, "the third attempt observed a block again")

        def admitted_retry(o, c):
            result(o)["recovery_counted_calls"][0]["response_sha256"] = "00" * 32
        self.assertFails("ac-cap-02-02-admitted", admitted_retry, "recovery: call 0 returned bytes")

    def test_lost_after_exec_ready(self):
        """AC-CAP-02-02 and AC-CAP-07-02."""
        name = "ac-cap-02-02-exec-ready"

        def result(o):
            return o["attempts"][1]["result"]

        def kept(o, c):
            result(o)["recovery"]["kept"] = []
        self.assertFails(name, kept, "did not keep the EXEC_READY observation")

        def resolved(o, c):
            result(o)["recovery"]["exec_ready"] = ["x"]
        self.assertFails(name, resolved, "did not keep the EXEC_READY observation")

        def durable(o, c):
            result(o)["child_roots_at_open"][probe.key_of(3)] = True
        self.assertFails(name, durable, "the child state of the lost block was durable")

        def unknown(o, c):
            result(o)["child_roots_at_open"] = {}
        self.assertFails(name, unknown, "the child state of the lost block was durable")

        def fetched(o, c):
            result(o)["recovery_counted_calls"][0]["origin"] = "fetch"
        self.assertFails(name, fetched, "completion replay: call 0 origin fetch")

        def replaced(o, c):
            set_value(o["attempts"][0]["store_after"], "0000000000000003/exec-ready", lambda r: r.update(digest="11" * 32))
        self.assertFails(name, replaced, "differs from the independent IMPORT intent")

        def head(o, c):
            result(o)["head_number_at_open"] = 4
        self.assertFails(name, head, "the lost block was stored before the recovery")

    def test_native_rejection(self):
        def inserted(o, c):
            o["attempts"][0]["result"]["inserted"] = 4
        self.assertFails("native-rejection", inserted, "did not refuse the block")

        def evidence(o, c):
            set_value(snap_rows(o["attempts"][0]["result"], probe.key_of(3)), "0000000000000003/rejected",
                      lambda r: r.update(evidence_digest="00" * 32))
        self.assertFails("native-rejection", evidence, "does not bind the native rejection evidence")

        def closed(o, c):
            o["attempts"][0]["store_after"].append({"key": probe.INPUTS + probe.key_of(3) + "/closed",
                                                    "value_sha256": "00" * 32, "value": "{}"})
        self.assertFails("native-rejection", closed, "kept its pin or inputs, or has a decision")

        def state(o, c):
            rows = snap_rows(o["attempts"][0]["result"], probe.key_of(3))
            rows.remove(row_of(rows, "0000000000000003/rejected"))
        self.assertFails("native-rejection", state, "is not NATIVE_REJECTED")

        def kept_pin(o, c):
            o["attempts"][0]["store_after"].append({"key": probe.PIN + probe.key_of(3), "value_sha256": "00" * 32,
                                                    "value_length": 1})
        self.assertFails("native-rejection", kept_pin, "kept its pin or inputs, or has a decision")

    def test_decision_crash(self):
        """AC-CAP-07-02 (child root durable) and AC-CAP-07-03 (losses inside a decision)."""
        key = probe.key_of(3)

        def result(o):
            return o["attempts"][1]["result"]

        def lost(o):
            return o["attempts"][0]["store_after"]

        def volatile(o, c):
            result(o)["child_roots_at_open"][key] = False
        self.assertFails("ac-cap-07-02-child-root", volatile, "the child state was not durable")

        def decided_early(o, c):
            dump = lost(o)
            row = copy.deepcopy(intent_row(dump, probe.key_of(2)))
            update_json(row, lambda r: r["members"][0].update(observation=key))
            row["key"] = row["key"].replace("/0000000000000002/", "/0000000000000003/")
            dump.append(row)
        self.assertFails("ac-cap-07-02-child-root", decided_early, "the block has a decision or is not EXEC_READY")

        for name in ("ac-cap-07-03-intent", "ac-cap-07-03-block-batch"):
            def intent(o, c):
                update_json(intent_row(lost(o), key), lambda r: r.update(evidence_digest="00" * 32))
            self.assertFails(name, intent, "the saved intent is not the independent intent")

            def marker(o, c):
                rec = json.loads(intent_row(lost(o), key)["value"])
                lost(o).append({"key": f"{probe.MARKER}{rec['sequence']:016x}", "value": "{}", "value_sha256": "00"})
            self.assertFails(name, marker, "the marker is")

            def report(o, c):
                result(o)["decision_recovery"]["completed"] = []
            self.assertFails(name, report, "the decision recovery did")

            def replay(o, c):
                result(o)["recovery_counted_calls"][0]["origin"] = "fetch"
            self.assertFails(name, replay, "recovery: call 0 origin fetch")

        def stored(o, c):
            result(o)["stored_at_open"].append(4)
        self.assertFails("ac-cap-07-03-intent", stored, "the block is stored True")

        def not_stored(o, c):
            result(o)["stored_at_open"].remove(4)
        self.assertFails("ac-cap-07-03-block-batch", not_stored, "the block is stored False")

        def head(o, c):
            result(o)["head_number_at_open"] = 4
        self.assertFails("ac-cap-07-03-block-batch", head, "with head 4, expected True with head 3")

        def no_marker(o, c):
            lost(o).remove(marker_row(lost(o), key))
        self.assertFails("ac-cap-07-03-marker", no_marker, "the marker is None")

        def marker_report(o, c):
            result(o)["decision_recovery"]["resolved"] = []
        self.assertFails("ac-cap-07-03-marker", marker_report, "the decision recovery did")

        def not_decided(o, c):
            lost(o).remove(decision_row(lost(o), key, "decided"))
        self.assertFails("ac-cap-07-03-decided", not_decided, "and the decided record None")

        def closed_early(o, c):
            result(o)["pending_at_open"] = []
        self.assertFails("ac-cap-07-03-decided", closed_early, "the decided observation was not open")

        def again(o, c):
            result(o)["observations"].insert(0, {"block": 4, "key": probe.key_of(4), "counted_calls": []})
        self.assertFails("ac-cap-07-03-intent", again, "the decided block was observed again")

        def other_sequence(o, c):
            update_json(intent_row(o["attempts"][1]["store_after"], key), lambda r: r.update(sequence=9))
        self.assertFails("ac-cap-07-03-intent", other_sequence, "differs from the independent IMPORT intent")

    def test_decision_rejected(self):
        """AC-CAP-07-06."""
        key = probe.key_of(3)

        for name, index in (("ac-cap-07-06-live", 0), ("ac-cap-07-06-native-rejected", 1)):
            def dump(o, index=index):
                return o["attempts"][index]["store_after"]

            def result(o, index=index):
                return o["attempts"][index]["result"]

            def decided(o, c):
                rec = json.loads(intent_row(dump(o), key)["value"])
                dump(o).append({"key": f"{probe.DECISION}{rec['sequence']:016x}/decided", "value": "{}",
                                "value_sha256": "00"})
            self.assertFails(name, decided, "is not a native rejection without a marker")

            def evidence(o, c):
                update_json(decision_row(dump(o), key, "rejected"), lambda r: r.update(evidence_digest="00" * 32))
            self.assertFails(name, evidence, "is not a native rejection without a marker")

            def marker(o, c):
                rec = json.loads(intent_row(dump(o), key)["value"])
                dump(o).append({"key": f"{probe.MARKER}{rec['sequence']:016x}", "value": "{}", "value_sha256": "00"})
            self.assertFails(name, marker, "is not a native rejection without a marker")

            def imported(o, c):
                result(o)["import_error"] = ""
            self.assertFails(name, imported, "the refused block was imported or the import did not stop")

            def obligation(o, c):
                result(o)["obligations"].append(3)
            self.assertFails(name, obligation, "the rejected intent has a delivery obligation")

        name = "ac-cap-07-06-native-rejected"

        def saved_marker(o, c):
            rec = json.loads(intent_row(o["attempts"][0]["store_after"], key)["value"])
            o["attempts"][0]["store_after"].append({"key": f"{probe.MARKER}{rec['sequence']:016x}", "value": "{}",
                                                    "value_sha256": "00"})
        self.assertFails(name, saved_marker, "the refused intent is not saved unresolved without a marker")

        def stored(o, c):
            o["attempts"][1]["result"]["stored_at_open"].append(4)
        self.assertFails(name, stored, "the refused block was stored before the recovery")

        def report(o, c):
            o["attempts"][1]["result"]["decision_recovery"]["rejected"] = []
        self.assertFails(name, report, "the decision recovery did")

        def again_decided(o, c):
            dump = o["attempts"][1]["store_after"]
            rec = json.loads(intent_row(dump, probe.key_of(4))["value"])
            dump.append({"key": f"{probe.DECISION}{rec['sequence']:016x}/decided", "value": "{}", "value_sha256": "00"})
        self.assertFails(name, again_decided, "re-import: block 4 is not a native rejection")

        def not_again(o, c):
            result = o["attempts"][1]["result"]
            result["observations"] = [obs for obs in result["observations"] if obs["block"] != 4]
        self.assertFails(name, not_again, "did not execute the refused block again")

    def test_known_block(self):
        name = "known-block-restart"

        def observed(o, c):
            o["attempts"][1]["result"]["observations"] = [{"block": 3}]
        self.assertFails(name, observed, "the re-import of stored blocks opened an observation")

        def failed(o, c):
            o["attempts"][1]["result"]["reimport_error"] = "boom"
        self.assertFails(name, failed, "the re-import of stored blocks opened an observation or failed")

        def changed(o, c):
            o["attempts"][1]["store_after"].append({"key": probe.DECISION + "00000000000000ff/intent", "value": "{}",
                                                    "value_sha256": "00"})
        self.assertFails(name, changed, "changed an admission or decision record")

        def counter(o, c):
            set_value(o["attempts"][1]["store_after"], probe.ADMISSION + "counter", lambda r: r.update(next=99))
        self.assertFails(name, counter, "changed an admission or decision record")

    def test_checkpoint_rio(self):
        def lost(o, c):
            dump = o["attempts"][0]["store_after"]
            dump.remove(row_of(dump, "bor.run-1.000000000000000c/log/0000000000000001"))
        self.assertFails("checkpoint-rio", lost, "two durable entries")

        def restart(o):
            return o["attempts"][1]["crash_result"]

        def recovery(o, c):
            restart(o)["recovery_counted_calls"][1]["origin"] = "fetch"
        self.assertFails("checkpoint-rio", recovery, "recovery: call 1 origin fetch")

        def completion(o, c):
            restart(o)["recovery_counted_calls"][3]["origin"] = "fetch"
        self.assertFails("checkpoint-rio", completion, "completion replay: call 0 origin fetch")

        def resolved(o, c):
            restart(o)["recovery"]["exec_ready"] = []
        self.assertFails("checkpoint-rio", resolved, "did not resolve the post-Rio obligation")

        def walk(o, c):
            o["attempts"][0]["crash_result"]["walk_check"]["truncated_list_error"] = "<nil>"
        self.assertFails("checkpoint-rio", walk, "snapshot walk")

        def walk_complete(o, c):
            o["attempts"][0]["crash_result"]["walk_check"]["complete_list_error"] = "unknown ancestor"
        self.assertFails("checkpoint-rio", walk_complete, "snapshot walk")

        def set_head(o, c):
            restart(o)["set_head_error"] = "<nil>"
        self.assertFails("checkpoint-rio", set_head, "SetHead below a pinned header was not refused")

        def unpinned(o, c):
            dump = o["attempts"][1]["store_after"]
            dump.remove(row_of(dump, "pin-v1-bor.run-1.0000000000000013"))
        self.assertFails("checkpoint-rio", unpinned, "did not keep its pin live")

        def report(o, c):
            restart(o)["decision_recovery"]["imported"] = []
        self.assertFails("checkpoint-rio", report, "the decision recovery did")

        def identity(o, c):
            o["attempts"][2]["result"]["error"] = "other"
        self.assertFails("checkpoint-rio", identity, "attempt 2 was not refused")

        def changed_store(o, c):
            o["attempts"][3]["store_after"] = o["attempts"][3]["store_after"][1:]
        self.assertFails("checkpoint-rio", changed_store, "attempt 3 was not refused")

        def patched(o, c):
            o["attempts"][4]["prune_state"]["refused_live_pins"] = False
        self.assertFails("checkpoint-rio", patched, "did not refuse the live pins")

        def accepted(o, c):
            o["attempts"][5]["prune_state"]["refused_live_pins"] = True
        self.assertFails("checkpoint-rio", accepted, "the accepted bor binary refused")

        def ancestors(o, c):
            rows = snap_rows(o["attempts"][0]["crash_result"], probe.key_of(4))
            set_value(rows, "bor.run-1.0000000000000004/capsule", lambda r: r["items"][5].update(length=1))
        self.assertFails("checkpoint-rio", ancestors, "capsule differs from the independent items")

    def test_rpc_window(self):
        def result(o):
            return o["attempts"][0]["result"]

        def rpc(o):
            return result(o)["rpc_in_window"]

        for change, text in (
                (lambda o: rpc(o).update(trigger_request="GET /bor/spans/latest"), "was not started by the first Heimdall read"),
                (lambda o: rpc(o).update(target=1033), "was not started by the first Heimdall read"),
                (lambda o: rpc(o).update(waiting_at_gate=False), "did not wait at the window gate"),
                (lambda o: rpc(o).update(returned_in_window=True), "did not wait at the window gate"),
                (lambda o: rpc(o).update(waiting_goroutine=""), "did not wait at the window gate"),
                (lambda o: rpc(o).update(snapshot_hash="0x" + "00" * 32), "did not return the native snapshot"),
                (lambda o: rpc(o).update(error="boom"), "did not return the native snapshot"),
                (lambda o: result(o)["rpc_before_window"].update(held_at_observe=False), "no counted Heimdall read was in flight"),
                (lambda o: result(o)["rpc_before_window"].update(held_request="GET /milestones/latest"),
                 "no counted Heimdall read was in flight"),
                (lambda o: result(o)["rpc_before_window"].update(block=1036), "no counted Heimdall read was in flight"),
                (lambda o: result(o)["rpc_before_window"].update(snapshot_hash="0x" + "00" * 32),
                 "in flight at the window of block 1037 did not return"),
                (lambda o: result(o)["rpc_before_window"].update(error="boom"),
                 "in flight at the window of block 1037 did not return"),
                (lambda o: result(o)["expired_fetch"].update(held_at_observe=False), "no detached span fetch"),
                (lambda o: result(o)["expired_fetch"].update(held_request="GET /status"), "no detached span fetch"),
                (lambda o: result(o)["expired_fetch"].update(lookup_error="<nil>"), "no detached span fetch"),
                (lambda o: result(o)["expired_fetch"].update(block=1038), "no detached span fetch"),
                (lambda o: result(o)["replays"].pop(), "do not cover every EXEC_READY"),
                (lambda o: result(o)["replays"][4].update(error="EXEC_READY replay differs"), "does not replay"),
                (lambda o: result(o)["replays"][4]["counted_calls"][0].update(origin="fetch"),
                 "replay of block 1035: call 0 origin fetch"),
                (lambda o: result(o)["rpc_control"]["passed_heimdall_calls"].pop("/status"), "cold caches outside a window"),
                (lambda o: result(o)["rpc_control"].update(snapshot_number=1039), "cold caches outside a window")):
            self.assertFails("rpc-window", lambda o, c, change=change: change(o), text)

        def foreign(o, c):
            calls = observations_by_block(result(o))[1035]["counted_calls"]
            calls.append({**calls[0], "sequence": len(calls)})
        self.assertFails("rpc-window", foreign, "block 1035: 4 counted calls, expected 3")

    def test_prune_block(self):
        def tool(o, index):
            return o["attempts"][index]["prune_state"]

        name = "prune-block-live-pins"
        for change, text in (
                (lambda o: tool(o, 1).update(refused_live_pins=False), "did not refuse the live pins first"),
                (lambda o: tool(o, 1).update(output_tail=tool(o, 1)["output_tail"] + "freezer is empty, abort"),
                 "did not refuse the live pins first"),
                (lambda o: tool(o, 1).update(command="prune-state"), "did not refuse the live pins first"),
                (lambda o: o["attempts"][1].update(ancient_after={"chain/x": "00"}),
                 "changed the store or the ancient directory"),
                (lambda o: o["attempts"][1].update(store_after=o["attempts"][1]["store_after"][1:]),
                 "changed the store or the ancient directory"),
                (lambda o: tool(o, 2).update(refused_live_pins=True), "the accepted bor binary did not reach"),
                (lambda o: tool(o, 2).update(output_tail=""), "the accepted bor binary did not reach")):
            self.assertFails(name, lambda o, c, change=change: change(o), text)

        def no_pins(o, c):
            for row in (o["attempts"][0]["store_after"], o["attempts"][1]["store_before"], o["attempts"][1]["store_after"]):
                row[:] = [r for r in row if not r["key"].startswith(probe.PIN)]
        self.assertFails(name, no_pins, "the live pins before the prune are []")

        def unobserved(o, c):
            o["attempts"][0]["crash_result"]["observations"].pop(2)
        self.assertFails(name, unobserved, "import: block 3 was not observed")

        name = "prune-block-no-pins"
        for change, text in (
                (lambda o: o["attempts"][0]["store_after"].append({"key": probe.PIN + "x", "value_sha256": "00",
                                                                   "value_length": 1}), "left an observation or a pin"),
                (lambda o: o["attempts"][0]["result"].update(observations=[{"block": 1}]), "left an observation or a pin"),
                (lambda o: o["attempts"][0]["result"].update(inserted=5), "left an observation or a pin"),
                (lambda o: tool(o, 1).update(refused_live_pins=True), "did not reach the native pruner"),
                (lambda o: tool(o, 1).update(output_tail=tool(o, 1)["output_tail"] + "guarded block pruning: live pins forbid"),
                 "did not reach the native pruner"),
                (lambda o: tool(o, 1).update(output_tail=""), "did not reach the native pruner")):
            self.assertFails(name, lambda o, c, change=change: change(o), text)

    def test_prune_block_frozen(self):
        name = "prune-block-frozen"

        def frozen(o):
            return o["attempts"][1]["crash_result"]["pins_after_freeze"]

        def tool(o, index):
            return o["attempts"][index]["prune_state"]

        for change, text in (
                (lambda o: frozen(o).update(ancients=30), "did not freeze the finalized blocks"),
                (lambda o: frozen(o)["pins"].append({"verify_error": "pinned native data is unavailable"}),
                 "did not freeze the finalized blocks"),
                (lambda o: tool(o, 2).update(refused_live_pins=False), "did not refuse the live pins over a non-empty freezer"),
                (lambda o: tool(o, 2).update(output_tail=tool(o, 2)["output_tail"] + "ancient chain segments already extracted"),
                 "did not refuse the live pins over a non-empty freezer"),
                (lambda o: o["attempts"][2].update(ancient_after={}), "changed the store or the ancient directory"),
                (lambda o: o["attempts"][2].update(ancient_before={}, ancient_after={}), "or the freezer was empty"),
                (lambda o: tool(o, 3).update(refused_live_pins=True), "did not stop at its native store open"),
                (lambda o: tool(o, 3).update(output_tail=""), "did not stop at its native store open"),
                (lambda o: o["attempts"][3].update(ancient_after={}), "did not stop at its native store open"),
                (lambda o: o["attempts"][4]["result"]["pins_after_freeze"].update(
                    pins=[{"verify_error": "pinned native data is unavailable"}]), "a pin is unreadable after the prune"),
                (lambda o: o["attempts"][0]["result"]["observations"].pop(5), "import: block 6 was not observed"),
                (lambda o: o["attempts"][4]["result"]["decision_recovery"].update(imported=[]), "the decision recovery did")):
            self.assertFails(name, lambda o, c, change=change: change(o), text)

        def unpinned(o, c):
            dump = o["attempts"][1]["store_after"]
            dump.remove(row_of(dump, "pin-v1-bor.run-1.0000000000000027"))
        self.assertFails(name, unpinned, "did not keep its pin live")

    def test_malformed_observation(self):
        def broken(o, c):
            o["attempts"][1]["result"]["snapshots"] = 5
        self.assertTrue(judge("native-rejection", broken)[0].startswith("native-rejection: malformed observation"))


class RuleTests(unittest.TestCase):
    def setUp(self):
        self.ctx = contexts()["checkpoint-rio"]
        self.config = self.ctx["config"]

    def test_request_path(self):
        self.assertEqual(probe.request_path("GET /clerk/time?from_id=3&to_time=x"), "/clerk/time")
        self.assertEqual(probe.request_path("GET /bor/spans/latest"), "/bor/spans/latest")

    def test_sprint_rio_and_time(self):
        self.assertEqual([probe.sprint(self.config, n) for n in (1, 31, 32, 1040)], [4, 4, 2, 2])
        self.assertEqual([probe.is_rio(self.config, n) for n in (1031, 1032)], [False, True])
        self.assertEqual(probe.to_time(1589549799, 0), "2020-05-15T13:36:39Z")
        self.assertEqual(probe.to_time(1589549799, 9), "2020-05-15T13:36:30Z")

    def test_expected_requests(self):
        chain = self.ctx["chain"]
        names = lambda n, v="a": [(c["request"].split("?")[0], c["body"]) for c in
                                  probe.expected_requests(ORACLE, self.config, chain[n - 1], v)]
        self.assertEqual(names(3), [])
        self.assertEqual(names(4), [("GET /bor/spans/1", "span-1.json"), ("GET /status", "status.json"),
                                    ("GET /clerk/time", "clerk-a-from-1.json"), ("GET /clerk/time", "clerk-empty.json")])
        self.assertEqual(names(4, "b")[2:], [("GET /clerk/time", "clerk-b-from-1.json"),
                                             ("GET /clerk/time", "clerk-empty.json")])
        self.assertEqual(names(8), [("GET /status", "status.json"), ("GET /clerk/time", "clerk-empty.json")])
        self.assertEqual(names(1031), [])
        self.assertEqual(names(1033), [("GET /status", "status.json"), ("GET /bor/spans/latest", "span-latest.json"),
                                       ("GET /bor/spans/1", "span-1.json")])
        self.assertEqual(len(names(1032)), 4)
        self.assertIn("from_id=4&", probe.expected_requests(ORACLE, self.config, chain[3], "b")[3]["request"])
        request = probe.expected_requests(ORACLE, self.config, chain[7], "a")[1]["request"]
        self.assertIn(f"to_time={probe.to_time(chain[7]['time'], 0)}&", request)

    def test_ancestor_rule(self):
        numbers = lambda n: [probe.decoded_header(h)[8] for h in
                             probe.ancestors(self.ctx["chain"], self.ctx["genesis_header"], self.config, n)]
        self.assertEqual(numbers(1), [b""])
        self.assertEqual(len(numbers(1024)), 1024)
        self.assertEqual(numbers(1025), [(1024).to_bytes(2, "big")])
        self.assertEqual(len(numbers(1031)), 7)
        self.assertEqual(numbers(1032), [(1031).to_bytes(2, "big")])
        self.assertEqual(numbers(1040), [(1039).to_bytes(2, "big")])

    def test_checkpoint_snapshot(self):
        ctx = contexts()["checkpoint-rio"]
        items = {block: data for (block, _), data in ctx["checkpoints"].items()}
        self.assertTrue(probe.checkpoint_snapshot_ok(items[1025], ctx, 1025))
        self.assertTrue(probe.checkpoint_snapshot_ok(items[1021], ctx, 1021))
        self.assertFalse(probe.checkpoint_snapshot_ok(items[1021], ctx, 1025))
        self.assertTrue(probe.checkpoint_snapshot_ok(b"", ctx, 1033))
        self.assertFalse(probe.checkpoint_snapshot_ok(items[1025], ctx, 1033))
        self.assertFalse(probe.checkpoint_snapshot_ok(b"not json", ctx, 1025))
        other = json.loads(items[1025])
        other["hash"] = "0x" + "00" * 32
        self.assertFalse(probe.checkpoint_snapshot_ok(json.dumps(other).encode(), ctx, 1025))
        wrong = json.loads(items[1025])
        wrong["validatorSet"]["validators"][0]["signer"] = "0x" + "00" * 20
        self.assertFalse(probe.checkpoint_snapshot_ok(json.dumps(wrong).encode(), ctx, 1025))

    def test_digests(self):
        frame = lambda tag, *v: hashlib.sha256(tag.encode() + b"\0" + b"".join(
            len(x.encode()).to_bytes(8, "big") + x.encode() for x in v)).hexdigest()
        self.assertEqual(probe.result_digest("h", "s", "r", 0), frame("extractor-bor-native-result-v1", "h", "s", "r", "0"))
        self.assertEqual(probe.rejection_digest("k", "h", "e"), frame("extractor-bor-native-rejection-v1", "k", "h", "e"))
        self.assertNotEqual(probe.result_digest("ab", "", "r", 0), probe.result_digest("a", "b", "r", 0))

    def test_derived_inputs(self):
        genesis, config = probe.derived_inputs(SAMPLE["source-genesis.json"], ORACLE)
        document = json.loads(genesis)
        self.assertEqual(document["config"], json.loads(config))
        self.assertEqual(int(document["timestamp"], 16), 1589549790)
        self.assertEqual(document["config"]["bor"]["rioBlock"], 1032)
        self.assertEqual(document["config"]["bor"]["stateSyncConfirmationDelay"], {"0": 0})
        self.assertEqual(probe.admission.canonical(genesis), genesis)
        source = json.loads(SAMPLE["source-genesis.json"])
        source["config"]["bor"]["rioBlock"] = 5
        with self.assertRaises(probe.Rejected):
            probe.derived_inputs(json.dumps(source).encode(), ORACLE)

    def test_build_context_checks(self):
        genesis, config = probe.derived_inputs(SAMPLE["source-genesis.json"], ORACLE)
        generated = json.loads(SAMPLE["chain/generated.json"])
        args = (ORACLE, BODIES, genesis, config, SAMPLE["chain/blocks.rlp"], SAMPLE["chain/genesis-header.rlp"], COMMIT)
        changed = copy.deepcopy(generated)
        changed["blocks"][3]["hash"] = "0x" + "00" * 32
        with self.assertRaises(probe.Rejected):
            probe.build_context(*args, changed)
        with self.assertRaises(probe.Rejected):
            probe.build_context(*args[:3], config.replace(b"1032", b"1033"), *args[4:], generated)
        with self.assertRaises(probe.Rejected):
            probe.build_context({**ORACLE, "chain_blocks": 9}, *args[1:], generated)
        with self.assertRaises(probe.Rejected):
            probe.sealed_chain(SAMPLE["chain/blocks.rlp"], SAMPLE["chain/blocks.rlp"][3:600])
        # Block 1 with another parent hash: the numbers still follow, the link does not.
        blocks = SAMPLE["chain/blocks.rlp"]
        first, end = probe.rlp_item(blocks, 0)
        first[0][0] = b"\x01" * 32
        with self.assertRaises(probe.Rejected):
            probe.sealed_chain(probe.rlp_encode(first) + blocks[end:], SAMPLE["chain/genesis-header.rlp"])

    def test_go_test_passed(self):
        ok = "ok  \tgithub.com/ethereum/go-ethereum/extractor/a\t0.1s\nok  \tgithub.com/ethereum/go-ethereum/extractor/b\t1s\n"
        probe.go_test_passed(ok, ("extractor/a", "extractor/b"))
        with self.assertRaises(probe.Rejected):
            probe.go_test_passed(ok, ("extractor/a", "extractor/c"))
        with self.assertRaises(probe.Rejected):
            probe.go_test_passed(ok + "--- FAIL: TestX (0.00s)\n", ("extractor/a",))
        with self.assertRaises(probe.Rejected):
            probe.go_test_passed(ok + "FAIL\tgithub.com/x\n", ("extractor/a",))


class TreeTests(unittest.TestCase):
    def test_apply_edit(self):
        edit = {"name": "e", "file": "f.go", "mode": "before", "anchor": "B\n", "text": "X\n"}
        self.assertEqual(tree.apply_edit("A\nB\nC\n", edit), "A\nX\nB\nC\n")
        self.assertEqual(tree.apply_edit("A\nB\nC\n", {**edit, "mode": "after"}), "A\nB\nX\nC\n")
        self.assertEqual(tree.apply_edit("A\nB\nC\n", {**edit, "mode": "replace"}), "A\nX\nC\n")
        for text in ("A\nC\n", "B\nB\n"):
            with self.assertRaises(tree.TreeError):
                tree.apply_edit(text, edit)

    def test_patch_set_anchors_and_paths(self):
        names = [edit["name"] for path in tree.PATCH_SETS for edit in tree.load_patches(path)["edits"]]
        self.assertEqual(len(set(names)), len(names))
        self.assertEqual([path.name for path in tree.PATCH_SETS], ["patches.json", "patches.json"])
        self.assertEqual([path.parent for path in tree.PATCH_SETS], [tree.NATIVE, tree.DECISION])
        for value in ("", "/abs", "a/../b", "a\\b"):
            with self.assertRaises(tree.TreeError):
                tree.relative(value)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "patches.json"
            for broken in ({"schema_version": 2, "files": {}, "edits": []},
                           {"schema_version": 1, "files": {}, "edits": [{"name": "a"}]},
                           {"schema_version": 1, "files": {}, "edits": [
                               {"name": "a", "file": "x", "mode": "insert", "anchor": "y", "text": ""}]},
                           {"schema_version": 1, "files": {}, "edits": [
                               {"name": "a", "file": "x", "mode": "after", "anchor": "", "text": ""}]},
                           {"schema_version": 1, "files": {}, "edits": [
                               {"name": "a", "file": "x", "mode": "after", "anchor": "y", "text": ""},
                               {"name": "a", "file": "x", "mode": "after", "anchor": "z", "text": ""}]}):
                path.write_text(json.dumps(broken))
                with self.assertRaises(tree.TreeError):
                    tree.load_patches(path)

    def fake_tree(self, directory, patches=None):
        """A fake native tree holding every edited file of both patch sets
        with each anchor once."""
        edits = [edit for path in tree.PATCH_SETS for edit in tree.load_patches(path)["edits"]]
        for edit in edits:
            path = directory / edit["file"]
            path.parent.mkdir(parents=True, exist_ok=True)
            old = path.read_text() if path.exists() else ""
            path.write_text(old + "// before\n" + edit["anchor"] + "// after\n")

    def test_install_records_every_file(self):
        patches = tree.load_patches()
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            self.fake_tree(source, patches)
            record = tree.install(source)
            kinds = {row["path"]: row["kind"] for row in record["files"]}
            self.assertEqual(kinds["extractor/borimport/borimport.go"], "package")
            self.assertEqual(kinds["core/zz_extractor_observer.go"], "new")
            self.assertEqual(kinds["core/blockchain.go"], "edited")
            self.assertEqual(kinds["extractor/decision/decision.go"], "package")
            self.assertEqual(kinds["extractor/decision/native/decider/decider.go"], "package")
            self.assertEqual(kinds["extractor/bordecision/bordecision.go"], "package")
            self.assertEqual(kinds["core/zz_extractor_decision.go"], "new")
            decision_edits = [edit["name"] for edit in tree.load_patches(tree.DECISION / "patches.json")["edits"]]
            self.assertEqual(record["edits"], [edit["name"] for edit in patches["edits"]] + decision_edits)
            self.assertEqual([row["path"] for row in record["patch_sets"]],
                             ["adapters/bor/admission/native/patches.json", "adapters/bor/decision/native/patches.json"])
            text = (source / "core/vm/interpreter.go").read_text()
            self.assertIn("OnExtractorSafePoint(byte(op), callContext)", text)
            text = (source / "core/blockchain.go").read_text()
            for call in ("bc.extractorWriteDecided(decider", "extractorCommitOnce(statedb, statedb.CommitWithUpdate",
                         "bc.extractorMarkBatch(blockBatch, block, false)", "bc.extractorMarkBatch(batch, block, true)"):
                self.assertIn(call, text)
            row = next(r for r in record["files"] if r["path"] == "core/blockchain.go")
            self.assertEqual(row["sha256"], hashlib.sha256((source / "core/blockchain.go").read_bytes()).hexdigest())
            self.assertNotEqual(row["sha256"], row["sha256_before"])
            with self.assertRaises(tree.TreeError):
                tree.install(source)

    def test_install_refuses_an_edit_name_in_two_patch_sets(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            self.fake_tree(source)
            with self.assertRaises(tree.TreeError):
                tree.install(source, patch_sets=(tree.NATIVE / "patches.json", tree.NATIVE / "patches.json"))

    def test_bor_decision_assemble(self):
        import bor_decision
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "module"
            packages = bor_decision.assemble(target)
            self.assertIn("extractor/decision/native/decider", packages)
            self.assertIn("extractor/decision", packages)
            for name in bor_decision.DECIDER:
                self.assertEqual((target / "extractor/decision/native/decider" / name).read_bytes(),
                                 (ROOT / "adapters/bor/decision/native/decider" / name).read_bytes())
            table = json.loads((target / "extractor/decision/native/decider/decider.mutants.json").read_text())
            self.assertEqual(table["root"], "../../../..")
            self.assertTrue((target / "extractor/decision/native/decider" / table["root"] / "go.mod").is_file())
            for mutant in table["mutants"]:
                source = (target / "extractor/decision/native/decider" / table["root"] / mutant["file"]).read_text()
                self.assertEqual(source.count(mutant["original"]), 1, mutant["name"])
            with self.assertRaises(ValueError):
                bor_decision.assemble(target)

    def test_install_never_replaces_a_native_file(self):
        patches = tree.load_patches()
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            self.fake_tree(source, patches)
            (source / "core/zz_extractor_observer.go").write_text("native\n")
            with self.assertRaises(tree.TreeError):
                tree.install(source)
            self.assertEqual((source / "core/zz_extractor_observer.go").read_text(), "native\n")

    def test_install_refuses_a_changed_native_file(self):
        patches = tree.load_patches()
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            self.fake_tree(source, patches)
            (source / "core/vm/interpreter.go").write_text("changed upstream\n")
            with self.assertRaises(tree.TreeError):
                tree.install(source)
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            self.fake_tree(source, patches)
            (source / "eth/backend.go").unlink()
            with self.assertRaises(tree.TreeError):
                tree.install(source)


if __name__ == "__main__":
    unittest.main()
