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
        def candidate(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/preparing",
                      lambda r: r["candidate"].update(parent_root="00" * 32))
        self.assertFails("native-rejection", candidate, "is not the observation of block 4")

        def capsule(o, c):
            set_value(o["attempts"][1]["store_after"], "bor.run-1.0000000000000004/capsule",
                      lambda r: r["items"][1].update(sha256="00" * 32))
        self.assertFails("native-rejection", capsule, "capsule differs from the independent items")

        def admitted(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/admitted",
                      lambda r: r.update(capsule_digest="00" * 32))
        self.assertFails("native-rejection", admitted, "ADMITTED record differs")

        def pin_digest(o, c):
            row_of(o["attempts"][1]["store_after"], "pin-v1-bor.run-1.0000000000000004")["value_sha256"] = "00" * 32
        self.assertFails("native-rejection", pin_digest, "pin digest is not the ledger record digest")

        def digest(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/admitted", lambda r: r.update(digest="00" * 32))
        self.assertFails("native-rejection", digest, "ADMITTED digest does not recompute")

        def consumed(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/exec-ready",
                      lambda r: r.update(consumed_digest="00" * 32))
        self.assertFails("native-rejection", consumed, "EXEC_READY does not bind")

        def ready_digest(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/exec-ready", lambda r: r.update(digest="00" * 32))
        self.assertFails("native-rejection", ready_digest, "EXEC_READY does not bind")

        def exec_digest(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/exec-ready",
                      lambda r: r.update(exec_digest="00" * 32))
        self.assertFails("native-rejection", exec_digest, "EXEC_READY does not bind")

        def not_ready(o, c):
            dump = o["attempts"][1]["store_after"]
            dump.remove(row_of(dump, "0000000000000005/exec-ready"))
        self.assertFails("native-rejection", not_ready, "block 5 did not reach EXEC_READY")

        def closed(o, c):
            set_value(o["attempts"][1]["store_after"], "bor.run-1.0000000000000004/closed",
                      lambda r: r.update(digest="00" * 32))
        self.assertFails("native-rejection", closed, "closed manifest differs")

        def lost_pin(o, c):
            dump = o["attempts"][1]["store_after"]
            dump.remove(row_of(dump, "pin-v1-bor.run-1.0000000000000004"))
        self.assertFails("native-rejection", lost_pin, "EXEC_READY lost its pin")

        def rejected_pin(o, c):
            dump = o["attempts"][0]["store_after"]
            pin = copy.deepcopy(row_of(dump, "pin-v1-bor.run-1.0000000000000002"))
            pin["key"] = pin["key"].replace("0000000000000002", "0000000000000003")
            dump.append(pin)
        self.assertFails("native-rejection", rejected_pin, "kept EXEC_READY or its pin")

        def checkpoint(o, c):
            c["checkpoints"] = {key: b"{}" for key in c["checkpoints"]}
        self.assertFails("native-rejection", checkpoint, "checkpoint-snapshot item does not follow")

        def head(o, c):
            o["attempts"][1]["result"]["head_hash"] = "0x" + "00" * 32
        self.assertFails("native-rejection", head, "the imported head is not block 6")

        def missing(o, c):
            o["attempts"][1]["result"]["observations"][1]["error"] = "x"
        self.assertFails("native-rejection", missing, "block 5 was not observed and imported")

    def test_lost_before_admitted(self):
        def state(o, c):
            dump = o["attempts"][0]["store_after"]
            admitted = copy.deepcopy(row_of(dump, "0000000000000002/admitted"))
            admitted["key"] = admitted["key"].replace("0000000000000002", "0000000000000003")
            dump.append(admitted)
        self.assertFails("ac-cap-02-01-seal", state, "the lost observation is ADMITTED")

        def pinned(o, c):
            dump = o["attempts"][0]["store_after"]
            pin = copy.deepcopy(row_of(dump, "pin-v1-bor.run-1.0000000000000002"))
            pin["key"] = pin["key"].replace("0000000000000002", "0000000000000003")
            dump.append(pin)
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
            o["attempts"][1]["result"]["pending_at_open"].pop()
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
            dump = o["attempts"][1]["store_after"]
            pin = copy.deepcopy(row_of(dump, "pin-v1-bor.run-1.0000000000000002"))
            pin["key"] = pin["key"].replace("0000000000000002", "0000000000000003")
            dump.append(pin)
        self.assertFails("ac-cap-02-01-pin", kept_pin, "kept a pin, a log entry or another state")

    def test_lost_after_admitted(self):
        def recovery(o, c):
            o["attempts"][1]["result"]["recovery"]["exec_ready"] = []
        self.assertFails("ac-cap-02-02-log", recovery, "did not resolve the ADMITTED obligation")

        def replayed(o, c):
            o["attempts"][1]["result"]["recovery_counted_calls"][0]["origin"] = "fetch"
        self.assertFails("ac-cap-02-02-log", replayed, "recovery: call 0 origin fetch")

        def fetched(o, c):
            o["attempts"][1]["result"]["recovery_counted_calls"][3]["origin"] = "replay"
        self.assertFails("ac-cap-02-02-finish", replayed, "recovery: call 0 origin fetch")
        self.assertFails("ac-cap-02-02-log", fetched, "recovery: call 3 origin replay")

        def changed(o, c):
            o["attempts"][1]["result"]["recovery_counted_calls"].append(
                {"response_sha256": c["body_sha256"]["clerk-b-from-1.json"]})
        self.assertFails("ac-cap-02-02-log", changed, "changed external response reached recovery")

        def admitted(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000003/admitted", lambda r: r.update(pin_digest="11" * 32))
        self.assertFails("ac-cap-02-02-log", admitted, "recovery changed the ADMITTED record")

        def saved(o, c):
            dump = o["attempts"][0]["store_after"]
            dump.remove(row_of(dump, "bor.run-1.0000000000000003/log/0000000000000002"))
        self.assertFails("ac-cap-02-02-log", saved, "durable log entries are [0, 1]")

        def evidence(o, c):
            set_value(o["attempts"][1]["store_after"], "0000000000000004/rejected",
                      lambda r: r.update(evidence_digest="00" * 32))
        self.assertFails("ac-cap-02-02-log", evidence, "was not rejected with its evidence digest")

        def third(o, c):
            o["attempts"][2]["result"]["head_hash"] = "0x" + "00" * 32
        self.assertFails("ac-cap-02-02-finish", third, "third attempt: the imported head")

        def admitted_retry(o, c):
            o["attempts"][1]["result"]["recovery_counted_calls"][0]["response_sha256"] = "00" * 32
        self.assertFails("ac-cap-02-02-admitted", admitted_retry, "recovery: call 0 returned bytes")

    def test_lost_after_exec_ready(self):
        def kept(o, c):
            o["attempts"][1]["result"]["recovery"]["kept"] = []
        self.assertFails("ac-cap-02-02-exec-ready", kept, "did not keep the EXEC_READY observation")

        def resolved(o, c):
            o["attempts"][1]["result"]["recovery"]["exec_ready"] = ["x"]
        self.assertFails("ac-cap-02-02-exec-ready", resolved, "did not keep the EXEC_READY observation")

    def test_native_rejection(self):
        def inserted(o, c):
            o["attempts"][0]["result"]["inserted"] = 4
        self.assertFails("native-rejection", inserted, "did not refuse the block")

        def evidence(o, c):
            set_value(o["attempts"][0]["store_after"], "0000000000000003/rejected",
                      lambda r: r.update(evidence_digest="00" * 32))
        self.assertFails("native-rejection", evidence, "does not bind the native rejection evidence")

        def closed(o, c):
            o["attempts"][0]["store_after"].append({"key": probe.INPUTS + probe.key_of(3) + "/closed",
                                                    "value_sha256": "00" * 32, "value": "{}"})
        self.assertFails("native-rejection", closed, "kept its pin or closed its log")

        def state(o, c):
            dump = o["attempts"][0]["store_after"]
            dump.remove(row_of(dump, "0000000000000003/rejected"))
        self.assertFails("native-rejection", state, "is not NATIVE_REJECTED")

    def test_checkpoint_rio(self):
        def lost(o, c):
            dump = o["attempts"][0]["store_after"]
            dump.remove(row_of(dump, "bor.run-1.000000000000000c/log/0000000000000001"))
        self.assertFails("checkpoint-rio", lost, "two durable entries")

        def recovery(o, c):
            o["attempts"][1]["result"]["recovery_counted_calls"][1]["origin"] = "fetch"
        self.assertFails("checkpoint-rio", recovery, "recovery: call 1 origin fetch")

        def resolved(o, c):
            o["attempts"][1]["result"]["recovery"]["exec_ready"] = []
        self.assertFails("checkpoint-rio", resolved, "did not resolve the post-Rio obligation")

        def walk(o, c):
            o["attempts"][1]["result"]["walk_check"]["truncated_list_error"] = "<nil>"
        self.assertFails("checkpoint-rio", walk, "snapshot walk")

        def walk_complete(o, c):
            o["attempts"][1]["result"]["walk_check"]["complete_list_error"] = "unknown ancestor"
        self.assertFails("checkpoint-rio", walk_complete, "snapshot walk")

        def set_head(o, c):
            o["attempts"][1]["result"]["set_head_error"] = "<nil>"
        self.assertFails("checkpoint-rio", set_head, "SetHead below a pinned header was not refused")

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
            set_value(o["attempts"][0]["store_after"], "bor.run-1.0000000000000004/capsule",
                      lambda r: r["items"][5].update(length=1))
        self.assertFails("checkpoint-rio", ancestors, "capsule differs from the independent items")

    def test_rpc_window(self):
        def result(o):
            return o["attempts"][0]["result"]

        def rpc(o):
            return result(o)["rpc_in_window"]

        def trigger(o, c):
            rpc(o)["trigger_request"] = "GET /bor/spans/latest"
        self.assertFails("rpc-window", trigger, "was not started by the first Heimdall read")

        def target(o, c):
            rpc(o)["target"] = 1033
        self.assertFails("rpc-window", target, "was not started by the first Heimdall read")

        def not_waiting(o, c):
            rpc(o)["waiting_at_gate"] = False
        self.assertFails("rpc-window", not_waiting, "did not wait at the window gate")

        def returned(o, c):
            rpc(o)["returned_in_window"] = True
        self.assertFails("rpc-window", returned, "did not wait at the window gate")

        def snapshot(o, c):
            rpc(o)["snapshot_hash"] = "0x" + "00" * 32
        self.assertFails("rpc-window", snapshot, "did not return the native snapshot")

        def rpc_error(o, c):
            rpc(o)["error"] = "boom"
        self.assertFails("rpc-window", rpc_error, "did not return the native snapshot")

        def foreign(o, c):
            calls = observations_by_block(result(o))[1035]["counted_calls"]
            calls.append({**calls[0], "sequence": len(calls)})
        self.assertFails("rpc-window", foreign, "block 1035: 4 counted calls, expected 3")

        def before(o):
            return result(o)["rpc_before_window"]

        def not_in_flight(o, c):
            before(o)["held_at_observe"] = False
        self.assertFails("rpc-window", not_in_flight, "no counted Heimdall read was in flight")

        def uncounted(o, c):
            before(o)["held_request"] = "GET /milestones/latest"
        self.assertFails("rpc-window", uncounted, "no counted Heimdall read was in flight")

        def before_block(o, c):
            before(o)["block"] = 1036
        self.assertFails("rpc-window", before_block, "no counted Heimdall read was in flight")

        def before_snapshot(o, c):
            before(o)["snapshot_hash"] = "0x" + "00" * 32
        self.assertFails("rpc-window", before_snapshot, "in flight at the window of block 1037 did not return")

        def before_error(o, c):
            before(o)["error"] = "boom"
        self.assertFails("rpc-window", before_error, "in flight at the window of block 1037 did not return")

        def expired(o):
            return result(o)["expired_fetch"]

        def expired_not_held(o, c):
            expired(o)["held_at_observe"] = False
        self.assertFails("rpc-window", expired_not_held, "no detached span fetch of an expired lookup")

        def expired_other_read(o, c):
            expired(o)["held_request"] = "GET /status"
        self.assertFails("rpc-window", expired_other_read, "no detached span fetch of an expired lookup")

        def expired_lookup_waited(o, c):
            expired(o)["lookup_error"] = "<nil>"
        self.assertFails("rpc-window", expired_lookup_waited, "no detached span fetch of an expired lookup")

        def expired_block(o, c):
            expired(o)["block"] = 1038
        self.assertFails("rpc-window", expired_block, "no detached span fetch of an expired lookup")

        def no_waiting_goroutine(o, c):
            rpc(o)["waiting_goroutine"] = ""
        self.assertFails("rpc-window", no_waiting_goroutine, "did not wait at the window gate")

        def uncovered(o, c):
            result(o)["replays"].pop()
        self.assertFails("rpc-window", uncovered, "do not cover every EXEC_READY")

        def replay_error(o, c):
            result(o)["replays"][4]["error"] = "EXEC_READY replay differs"
        self.assertFails("rpc-window", replay_error, "does not replay")

        def replay_fetch(o, c):
            result(o)["replays"][4]["counted_calls"][0]["origin"] = "fetch"
        self.assertFails("rpc-window", replay_fetch, "replay of block 1035: call 0 origin fetch")

        def control(o, c):
            result(o)["rpc_control"]["passed_heimdall_calls"].pop("/status")
        self.assertFails("rpc-window", control, "cold caches outside a window")

        def control_hash(o, c):
            result(o)["rpc_control"]["snapshot_number"] = 1039
        self.assertFails("rpc-window", control_hash, "cold caches outside a window")

    def test_prune_block(self):
        def tool(o, index):
            return o["attempts"][index]["prune_state"]

        def not_refused(o, c):
            tool(o, 1)["refused_live_pins"] = False
        self.assertFails("prune-block-live-pins", not_refused, "did not refuse the live pins first")

        def native_first(o, c):
            tool(o, 1)["output_tail"] += "freezer is empty, abort"
        self.assertFails("prune-block-live-pins", native_first, "did not refuse the live pins first")

        def other_command(o, c):
            tool(o, 1)["command"] = "prune-state"
        self.assertFails("prune-block-live-pins", other_command, "did not refuse the live pins first")

        def ancient(o, c):
            o["attempts"][1]["ancient_after"] = {"chain/x": "00"}
        self.assertFails("prune-block-live-pins", ancient, "changed the store or the ancient directory")

        def store(o, c):
            o["attempts"][1]["store_after"] = o["attempts"][1]["store_after"][1:]
        self.assertFails("prune-block-live-pins", store, "changed the store or the ancient directory")

        def no_pins(o, c):
            for row in (o["attempts"][0]["store_after"], o["attempts"][1]["store_before"], o["attempts"][1]["store_after"]):
                row[:] = [r for r in row if not r["key"].startswith(probe.PIN)]
        self.assertFails("prune-block-live-pins", no_pins, "no pin is live before the prune")

        def unobserved(o, c):
            o["attempts"][0]["result"]["observations"].pop(2)
        self.assertFails("prune-block-live-pins", unobserved, "import: block 3 was not observed")

        def accepted_refused(o, c):
            tool(o, 2)["refused_live_pins"] = True
        self.assertFails("prune-block-live-pins", accepted_refused, "the accepted bor binary did not reach")

        def accepted_elsewhere(o, c):
            tool(o, 2)["output_tail"] = ""
        self.assertFails("prune-block-live-pins", accepted_elsewhere, "the accepted bor binary did not reach")

        def pinned(o, c):
            o["attempts"][0]["store_after"].append({"key": probe.PIN + "x", "value_sha256": "00", "value_length": 1})
        self.assertFails("prune-block-no-pins", pinned, "left an observation or a pin")

        def observed(o, c):
            o["attempts"][0]["result"]["observations"] = [{"block": 1}]
        self.assertFails("prune-block-no-pins", observed, "left an observation or a pin")

        def unimported(o, c):
            o["attempts"][0]["result"]["inserted"] = 5
        self.assertFails("prune-block-no-pins", unimported, "left an observation or a pin")

        def refused(o, c):
            tool(o, 1)["refused_live_pins"] = True
        self.assertFails("prune-block-no-pins", refused, "did not reach the native pruner")

        def guard_text(o, c):
            tool(o, 1)["output_tail"] += "guarded block pruning: live pins forbid"
        self.assertFails("prune-block-no-pins", guard_text, "did not reach the native pruner")

        def elsewhere(o, c):
            tool(o, 1)["output_tail"] = ""
        self.assertFails("prune-block-no-pins", elsewhere, "did not reach the native pruner")

    def test_prune_block_frozen(self):
        name = "prune-block-frozen"

        def frozen(o):
            return o["attempts"][1]["result"]["pins_after_freeze"]

        def tool(o, index):
            return o["attempts"][index]["prune_state"]

        def not_frozen(o, c):
            frozen(o)["ancients"] = 30
        self.assertFails(name, not_frozen, "did not move readable pinned headers")

        def unreadable_before(o, c):
            frozen(o)["pins"][3]["verify_error"] = "pinned native data is unavailable"
        self.assertFails(name, unreadable_before, "did not move readable pinned headers")

        def no_frozen_pin(o, c):
            for pin in frozen(o)["pins"]:
                pin["headers"] = [frozen(o)["ancients"] + 5]
        self.assertFails(name, no_frozen_pin, "did not move readable pinned headers")

        def not_refused(o, c):
            tool(o, 2)["refused_live_pins"] = False
        self.assertFails(name, not_refused, "did not refuse the live pins over a non-empty freezer")

        def native_first(o, c):
            tool(o, 2)["output_tail"] += "ancient chain segments already extracted"
        self.assertFails(name, native_first, "did not refuse the live pins over a non-empty freezer")

        def ancient_changed(o, c):
            o["attempts"][2]["ancient_after"] = {}
        self.assertFails(name, ancient_changed, "changed the store or the ancient directory")

        def empty_freezer(o, c):
            o["attempts"][2]["ancient_before"] = o["attempts"][2]["ancient_after"] = {}
        self.assertFails(name, empty_freezer, "or the freezer was empty")

        def accepted_refused(o, c):
            tool(o, 3)["refused_live_pins"] = True
        self.assertFails(name, accepted_refused, "did not stop at its native store open")

        def accepted_elsewhere(o, c):
            tool(o, 3)["output_tail"] = ""
        self.assertFails(name, accepted_elsewhere, "did not stop at its native store open")

        def accepted_pruned(o, c):
            o["attempts"][3]["ancient_after"] = {}
        self.assertFails(name, accepted_pruned, "did not stop at its native store open")

        def lost_after(o, c):
            o["attempts"][4]["result"]["pins_after_freeze"]["pins"][2]["verify_error"] = "pinned native data is unavailable"
        self.assertFails(name, lost_after, "a pin is unreadable after the prune attempts")

        def import_incomplete(o, c):
            o["attempts"][0]["result"]["observations"].pop(5)
        self.assertFails(name, import_incomplete, "import: block 6 was not observed")

    def test_malformed_observation(self):
        def broken(o, c):
            o["attempts"][1]["store_after"] = None
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
