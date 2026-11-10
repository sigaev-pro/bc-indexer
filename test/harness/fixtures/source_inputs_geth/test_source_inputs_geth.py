"""Portable checks for the E01-02.inputs Geth probe oracle and tree install. No native runtime claim."""

import copy
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
import source_inputs  # noqa: E402
import source_inputs_geth as geth  # noqa: E402

ORACLE = json.loads((geth.FIXTURE / "expected.json").read_text())
COMMIT = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
HASHES = ["0x" + hashlib.sha256(f"block {number}".encode()).hexdigest() for number in range(1, 5)]
HEADER_SHA = hashlib.sha256(b"header").hexdigest()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def capsule():
    data = {"header-rlp": None, "parent-hash": bytes.fromhex(HASHES[2][2:]), "native-commit": COMMIT.encode(),
            "normalization-profile": ORACLE["normalization_profile"].encode()}
    refs = []
    for name in ORACLE["capsule_items"]:
        content = data.get(name, name.encode() + b"-bytes")
        refs.append({"name": name, "length": 32 if content is None else len(content),
                     "sha256": HEADER_SHA if content is None else sha(content)})
    items_digest = source_inputs.tag("extractor-source-inputs-capsule-v1") + source_inputs.u64(len(refs))
    for ref in refs:
        items_digest += (source_inputs.u64(len(ref["name"])) + ref["name"].encode() + source_inputs.u64(ref["length"])
                         + bytes.fromhex(ref["sha256"]))
    return {"observation": ORACLE["observation"], "items": refs, "digest": sha(items_digest)}


def log(poisoned=False):
    ref = HASHES[3]
    return [(f"geth-blockhash-v1 ref={ref} number={number}",
             sha(f"changed external response {number}".encode()) if poisoned else HASHES[number - 1][2:])
            for number in ORACLE["requested_numbers"]]


def put(key):
    return {"op": "put", "key": f"extractor-source-inputs-v1/{ORACLE['observation']}/{key}", "length": 1}


SYNC = {"op": "sync"}


def seal_events(existed):
    if existed:
        return [SYNC, {"op": "sealed"}]
    chunks = [put(f"capsule/{index:016x}/0000000000000000") for index in range(len(ORACLE["capsule_items"]))]
    return chunks + [SYNC, put("capsule"), SYNC, {"op": "sealed"}]


def dependency_events(sequence, fetched, handed=True, poisoned=False):
    request, response = log(poisoned)[sequence]
    rows = [{"op": "request", "sequence": sequence, "request": request}]
    if fetched:
        rows += [{"op": "fetch", "sequence": sequence, "poisoned": poisoned},
                 put(f"log/{sequence:016x}/0000000000000000"), put(f"log/{sequence:016x}"), SYNC, put("frontier"), SYNC]
    if handed:
        rows.append({"op": "handoff", "sequence": sequence, "response": response})
    return rows


def consumed(capsule_digest):
    chain = source_inputs.chain_start(bytes.fromhex(capsule_digest))
    for sequence, (request, response) in enumerate(log()):
        chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, request.encode(), bytes.fromhex(response)))
    digest = source_inputs.consumed_digest(bytes.fromhex(capsule_digest), 3, chain).hex()
    return {"observation": ORACLE["observation"], "capsule_digest": capsule_digest, "count": 3, "chain": chain.hex(),
            "digest": digest}


def complete(name, want):
    seal = capsule()
    events = seal_events(want["capsule_existed"]) + [SYNC, {"op": "opened", "frontier": want["frontier_at_open"]},
                                                    {"op": "process_start", "number": 4}]
    saved_poison = want.get("saved") == "poisoned"
    for sequence in range(3):
        events += dependency_events(sequence, sequence in want["fetched"], poisoned=saved_poison)
        if sequence in want["fetched"]:
            events[-6]["poisoned"] = want.get("fetch_poisoned", False)
    if want.get("rejected"):
        return {"name": name, "exit_code": 0, "signal": "", "events": events,
                "result": {"errors": [], "capsule_existed": want["capsule_existed"],
                           "frontier_at_open": want["frontier_at_open"], "head_before": 3, "head_number": 3,
                           "head_hash": HASHES[2], "capsule": seal,
                           "import_error": "invalid merkle root (remote: aa local: bb)"}}
    if want["closes"]:
        events += [put("closed"), SYNC]
    events.append({"op": "finished"})
    result = {"errors": [], "capsule_existed": want["capsule_existed"], "frontier_at_open": want["frontier_at_open"],
              "head_before": 3, "head_number": 4, "head_hash": HASHES[3],
              "reader_slots": ["0x" + response for _, response in log()], "capsule": seal,
              "consumed": consumed(seal["digest"]), "cursor": 3,
              "extra_after_finish": "dependency log is closed: geth-probe-obs-4 at 3"}
    return {"name": name, "exit_code": 0, "signal": "", "events": events, "result": result}


def crash(name, want):
    events = seal_events(False)
    if want["point"] == "before_handoff":
        events += [SYNC, {"op": "opened", "frontier": 0}, {"op": "process_start", "number": 4}]
        for sequence in want["handed"]:
            events += dependency_events(sequence, True)
        events += dependency_events(want["sequence"], True, handed=False)
    events.append({"op": "crash", "point": want["point"]})
    return {"name": name, "exit_code": -1, "signal": "killed", "events": events}


def control(name):
    events = [{"op": "process_start", "number": 4}] + [{"op": "direct_poison", "number": n}
                                                        for n in ORACLE["requested_numbers"]]
    result = {"errors": [], "head_before": 3, "head_number": 3, "head_hash": HASHES[2],
              "import_error": "invalid merkle root (remote: aa local: bb)"}
    return {"name": name, "exit_code": 0, "signal": "", "events": events, "result": result}


def observation():
    scenarios = []
    for name, wants in ORACLE["scenarios"].items():
        attempts = []
        for want in wants:
            builder = {"complete": lambda: complete(want["name"], want), "crash": lambda: crash(want["name"], want),
                       "control": lambda: control(want["name"])}[want["kind"]]
            attempts.append(builder())
        scenarios.append({"name": name, "attempts": attempts})
    return {"family": "geth", "observation": ORACLE["observation"], "target": 4, "lookbacks": [1, 3, 1],
            "generated_hashes": list(HASHES), "header_rlp_sha256": HEADER_SHA, "scenarios": scenarios}


def attempt(value, scenario, name):
    rows = {row["name"]: row for row in value["scenarios"]}[scenario]["attempts"]
    return {row["name"]: row for row in rows}[name]


class OracleTests(unittest.TestCase):
    def failures(self, change):
        value = observation()
        change(value)
        return geth.checked_compare(value, ORACLE, COMMIT)

    def assertRejected(self, change, text):
        failures = self.failures(change)
        self.assertTrue(any(text in failure for failure in failures), failures)

    def test_matching_observation_passes(self):
        self.assertEqual(geth.checked_compare(observation(), ORACLE, COMMIT), [])

    def test_wrong_commit_is_rejected(self):
        self.assertIn("capsule item native-commit differs", " ".join(geth.checked_compare(observation(), ORACLE, "x")))

    def test_chain_identity(self):
        self.assertRejected(lambda v: v.update(family="bsc"), "does not describe the oracle chain")
        self.assertRejected(lambda v: v.update(observation="other"), "does not describe the oracle chain")
        self.assertRejected(lambda v: v.update(target=5), "does not describe the oracle chain")
        self.assertRejected(lambda v: v["generated_hashes"].pop(), "does not describe the oracle chain")

    def test_scenario_set(self):
        self.assertRejected(lambda v: v["scenarios"].pop(), "scenario set differs")
        self.assertRejected(lambda v: v["scenarios"].append(copy.deepcopy(v["scenarios"][0])), "scenario set differs")
        self.assertRejected(lambda v: v["scenarios"][0]["attempts"].pop(), "attempts differ from the oracle")

    def test_complete_attempt_must_finish_cleanly(self):
        clean = ("clean_then_closed_replay", "clean-1")
        self.assertRejected(lambda v: attempt(v, *clean).update(exit_code=1), "did not finish")
        self.assertRejected(lambda v: attempt(v, *clean).update(signal="killed"), "did not finish")
        self.assertRejected(lambda v: attempt(v, *clean).pop("result"), "did not finish")
        self.assertRejected(lambda v: attempt(v, *clean)["result"].update(errors=["x"]), "reported errors")
        self.assertRejected(lambda v: attempt(v, *clean)["result"].update(import_error="x"), "reported errors")
        self.assertRejected(lambda v: attempt(v, *clean)["result"].update(head_number=3), "imported head differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"].update(head_hash=HASHES[2]), "imported head differs")

    def test_native_must_receive_saved_bytes(self):
        def change(value):
            attempt(value, "clean_then_closed_replay", "clean-2-closed-replay")["result"]["reader_slots"][1] = HASHES[3]
        self.assertRejected(change, "did not receive the expected BLOCKHASH bytes")

    def test_durable_state_at_open(self):
        retry = ("crash_before_handoff", "handoff-2-retry")
        self.assertRejected(lambda v: attempt(v, *retry)["result"].update(frontier_at_open=1), "durable state at open")
        self.assertRejected(lambda v: attempt(v, *retry)["result"].update(capsule_existed=False), "durable state at open")

    def test_capsule_checks(self):
        clean = ("clean_then_closed_replay", "clean-1")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["capsule"]["items"].pop(), "capsule items differ")
        for index, name in ((0, "header-rlp"), (2, "parent-hash"), (6, "native-commit"), (7, "normalization-profile")):
            def change(value, index=index):
                attempt(value, *clean)["result"]["capsule"]["items"][index]["sha256"] = "00" * 32
            self.assertRejected(change, f"capsule item {name} differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["capsule"].update(digest="00" * 32),
                            "capsule digest differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["capsule"].update(observation="x"),
                            "capsule digest differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["capsule"]["items"][3].update(length=1),
                            "capsule digest differs")

    def test_capsule_item_layout(self):
        clean = ("clean_then_closed_replay", "clean-1")
        for length in (0, 65537, "1"):
            def change(value, length=length):
                attempt(value, *clean)["result"]["capsule"]["items"][1]["length"] = length
            self.assertRejected(change, "empty or longer than one chunk")

    def test_saved_poisoned_answers_reach_native_code(self):
        replay = ("poisoned_log_replay", "poisoned-2-replay")
        first = ("poisoned_log_replay", "poisoned-1")

        def imported(value):
            result = attempt(value, *replay)["result"]
            result.update(import_error=None, head_number=4, head_hash=HASHES[3])
        self.assertRejected(imported, "did not reject the saved poisoned answers")
        self.assertRejected(lambda v: attempt(v, *replay)["result"].update(head_number=4),
                            "did not reject the saved poisoned answers")
        self.assertRejected(lambda v: attempt(v, *replay)["result"].update(import_error="unknown ancestor"),
                            "did not reject the saved poisoned answers")
        self.assertRejected(lambda v: attempt(v, *replay)["result"].update(reader_slots=[]),
                            "did not reject the saved poisoned answers")
        self.assertRejected(lambda v: attempt(v, *replay)["result"].update(consumed={}),
                            "did not reject the saved poisoned answers")
        self.assertRejected(lambda v: attempt(v, *replay)["result"].update(errors=["x"]), "reported errors")

        def native_bytes(value):
            event = next(e for e in attempt(value, *replay)["events"] if e["op"] == "handoff")
            event["response"] = HASHES[2][2:]
        self.assertRejected(native_bytes, "a hand-off differs from the saved bytes")

        def honest_fetch(value):
            next(e for e in attempt(value, *first)["events"] if e["op"] == "fetch")["poisoned"] = False
        self.assertRejected(honest_fetch, "a fetch used the wrong source")
        self.assertRejected(lambda v: attempt(v, *first)["events"].append({"op": "finished"}),
                            "consumed manifest was not closed")
        self.assertRejected(lambda v: attempt(v, *replay)["result"].update(frontier_at_open=0), "durable state at open")

    def test_consumed_manifest(self):
        clean = ("clean_then_closed_replay", "clean-1")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["consumed"].update(digest="00" * 32),
                            "consumed manifest differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["consumed"].update(count=2), "consumed manifest differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"]["consumed"].update(capsule_digest="00" * 32),
                            "consumed manifest differs")
        self.assertRejected(lambda v: attempt(v, *clean)["result"].update(cursor=2), "closed log accepted")
        self.assertRejected(lambda v: attempt(v, *clean)["result"].update(extra_after_finish="<nil>"), "closed log accepted")

    def test_poisoned_fetch_is_rejected(self):
        def change(value):
            events = attempt(value, "crash_before_handoff", "handoff-2-retry")["events"]
            next(event for event in events if event["op"] == "fetch")["poisoned"] = True
        self.assertRejected(change, "a fetch used the wrong source")

    def test_seal_order(self):
        clean = ("clean_then_closed_replay", "clean-1")

        def drop_last_sync(value):
            events = attempt(value, *clean)["events"]
            del events[events.index({"op": "sealed"}) - 1]
        self.assertRejected(drop_last_sync, "manifest was not written once and synced")

        def drop_chunk_sync(value):
            events = attempt(value, *clean)["events"]
            index = next(i for i, e in enumerate(events) if e.get("key", "").endswith("/capsule"))
            del events[index - 1]
        self.assertRejected(drop_chunk_sync, "chunks were not synced before the manifest")

        def no_chunks(value):
            events = attempt(value, *clean)["events"]
            events[:] = [e for e in events if "/capsule/" not in e.get("key", "")]
        self.assertRejected(no_chunks, "did not write one chunk for every capsule item")

        def missing_chunk(value):
            events = attempt(value, *clean)["events"]
            events[:] = [e for e in events if not e.get("key", "").endswith("/capsule/0000000000000003/0000000000000000")]
        self.assertRejected(missing_chunk, "did not write one chunk for every capsule item")

        def missing_chunk_in_crash(value):
            events = attempt(value, "crash_after_seal", "seal-1-crash")["events"]
            del events[0]
        self.assertRejected(missing_chunk_in_crash, "did not write one chunk for every capsule item")

        def two_manifests(value):
            attempt(value, *clean)["events"].insert(0, put("capsule"))
        self.assertRejected(two_manifests, "manifest was not written once and synced")

        def double_seal(value):
            events = attempt(value, *clean)["events"]
            events.insert(0, {"op": "sealed"})
        self.assertRejected(double_seal, "did not seal exactly once")

        def reseal_writes(value):
            events = attempt(value, "crash_after_seal", "seal-2-retry")["events"]
            events.insert(0, put("capsule"))
        self.assertRejected(reseal_writes, "reseal rewrote the capsule")

        def reseal_no_sync(value):
            events = attempt(value, "crash_after_seal", "seal-2-retry")["events"]
            events.pop(0)
        self.assertRejected(reseal_no_sync, "reseal rewrote the capsule or did not sync")

    def test_durable_before_execution(self):
        clean = ("clean_then_closed_replay", "clean-1")

        def move_start(value):
            events = attempt(value, *clean)["events"]
            start = events.pop(next(i for i, e in enumerate(events) if e["op"] == "process_start"))
            events.insert(0, start)
        self.assertRejected(move_start, "not durable before execution started")

        def open_without_sync(value):
            events = attempt(value, *clean)["events"]
            del events[events.index({"op": "sealed"}) + 1]
        self.assertRejected(open_without_sync, "not durable before execution started")

        def seal_after_open(value):
            events = attempt(value, *clean)["events"]
            sealed = events.pop(events.index({"op": "sealed"}))
            events.insert(next(i for i, e in enumerate(events) if e["op"] == "opened") + 1, sealed)
        self.assertRejected(seal_after_open, "not durable before execution started")

        def no_start(value):
            events = attempt(value, *clean)["events"]
            events[:] = [e for e in events if e["op"] != "process_start"]
        self.assertRejected(no_start, "not durable before execution started")

        def no_open(value):
            events = attempt(value, *clean)["events"]
            events[:] = [e for e in events if e["op"] != "opened"]
        self.assertRejected(no_open, "not durable before execution started")

    def test_dependency_order(self):
        clean = ("clean_then_closed_replay", "clean-1")

        def remove(op, key=None, nth=0):
            def change(value):
                events = attempt(value, *clean)["events"]
                start = next(i for i, e in enumerate(events) if e["op"] == "request")
                matches = [i for i, e in enumerate(events) if i > start and e["op"] == op and
                           (key is None or e.get("key", "").endswith(key))]
                del events[matches[nth]]
            return change
        self.assertRejected(remove("sync", nth=1), "not persisted and synced before its hand-off")
        self.assertRejected(remove("sync", nth=0), "not persisted and synced before its hand-off")
        self.assertRejected(remove("put", "/frontier"), "not persisted and synced before its hand-off")
        self.assertRejected(remove("put", "/log/0000000000000000"), "not persisted and synced before its hand-off")
        self.assertRejected(remove("fetch"), "not persisted and synced before its hand-off")
        self.assertRejected(remove("handoff"), "was not handed off exactly once")
        self.assertRejected(remove("request", nth=1), "requests are not the expected sequences")

        def swap(value):
            events = attempt(value, *clean)["events"]
            index = next(i for i, e in enumerate(events) if e["op"] == "handoff")
            events[index - 1], events[index] = events[index], events[index - 1]
        self.assertRejected(swap, "not persisted and synced before its hand-off")

        def double_handoff(value):
            events = attempt(value, *clean)["events"]
            index = next(i for i, e in enumerate(events) if e["op"] == "handoff")
            events.insert(index, dict(events[index]))
        self.assertRejected(double_handoff, "was not handed off exactly once")

        def wrong_handoff_sequence(value):
            next(e for e in attempt(value, *clean)["events"] if e["op"] == "handoff")["sequence"] = 1
        self.assertRejected(wrong_handoff_sequence, "was not handed off exactly once")

        def write_after_handoff(value):
            events = attempt(value, *clean)["events"]
            index = next(i for i, e in enumerate(events) if e["op"] == "handoff")
            events.insert(index + 1, put("frontier"))
        self.assertRejected(write_after_handoff, "events follow the hand-off of sequence 0")

        def replay_writes(value):
            events = attempt(value, "clean_then_closed_replay", "clean-2-closed-replay")["events"]
            index = next(i for i, e in enumerate(events) if e["op"] == "handoff")
            events.insert(index, {"op": "fetch", "sequence": 0, "poisoned": False})
        self.assertRejected(replay_writes, "replayed sequence 0 fetched or wrote data")

        def wrong_request(value):
            next(e for e in attempt(value, *clean)["events"] if e["op"] == "request")["request"] = "other"
        self.assertRejected(wrong_request, "a request differs from the expected identity")

        def wrong_response(value):
            next(e for e in attempt(value, *clean)["events"] if e["op"] == "handoff")["response"] = "00" * 32
        self.assertRejected(wrong_response, "a hand-off differs from the saved bytes")

        def early_request(value):
            events = attempt(value, *clean)["events"]
            request = events.pop(next(i for i, e in enumerate(events) if e["op"] == "request"))
            events.insert(0, request)
        self.assertRejected(early_request, "requests are not the expected sequences")

    def test_close_tail(self):
        self.assertRejected(lambda v: attempt(v, "clean_then_closed_replay", "clean-1")["events"].pop(),
                            "consumed manifest was not closed")
        self.assertRejected(lambda v: attempt(v, "clean_then_closed_replay", "clean-2-closed-replay")["events"].insert(
            -1, put("closed")), "consumed manifest was not closed")

    def test_crash_attempts(self):
        seal = ("crash_after_seal", "seal-1-crash")
        handoff = ("crash_before_handoff", "handoff-1-crash")
        self.assertRejected(lambda v: attempt(v, *seal).update(signal=""), "was not killed")
        self.assertRejected(lambda v: attempt(v, *seal).update(result={}), "was not killed")
        self.assertRejected(lambda v: attempt(v, *seal)["events"].pop(), "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal)["events"][-1].update(point="before_handoff"),
                            "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal).update(events=[]), "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal)["events"][-1].update(op="sealed"),
                            "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal)["events"].insert(-1, SYNC), "did not follow a completed seal")
        self.assertRejected(lambda v: attempt(v, *seal)["events"].pop(-3), "manifest was not written once and synced")

        def no_start(value):
            events = attempt(value, *handoff)["events"]
            events[:] = [e for e in events if e["op"] != "process_start"]
        self.assertRejected(no_start, "execution did not start before the crash")

        def crash_unsynced(value):
            events = attempt(value, *handoff)["events"]
            del events[-2]
        self.assertRejected(crash_unsynced, "sequence 1 was not persisted")

        def handed_crashed(value):
            events = attempt(value, *handoff)["events"]
            events.insert(-1, {"op": "handoff", "sequence": 1, "response": log()[1][1]})
        self.assertRejected(handed_crashed, "sequence 1 was not persisted")

    def test_control(self):
        control_attempt = ("control_direct_poison", "control-1")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["result"].update(import_error=None),
                            "poisoned control was not rejected")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["result"].update(head_number=4),
                            "poisoned control was not rejected")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["events"].pop(), "poisoned control was not rejected")

    def test_unknown_attempt_kind(self):
        oracle = copy.deepcopy(ORACLE)
        oracle["scenarios"]["control_direct_poison"][0]["kind"] = "other"
        self.assertIn("unknown attempt kind", " ".join(geth.checked_compare(observation(), oracle, COMMIT)))

    def test_malformed_observation(self):
        self.assertRejected(lambda v: v["scenarios"][0].update(attempts=None), "attempts differ")
        self.assertTrue(geth.checked_compare({"family": "geth", "observation": ORACLE["observation"], "target": 4,
                                              "generated_hashes": None}, ORACLE, COMMIT))
        self.assertRejected(lambda v: attempt(v, "clean_then_closed_replay", "clean-1")["result"].update(capsule=None),
                            "capsule items differ")


class InstallTests(unittest.TestCase):
    def tree(self, text="\tdefer evm.Release()\n"):
        directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, directory)
        (directory / "core").mkdir()
        (directory / geth.ANCHOR_FILE).write_text("func Process() {\n" + text + "}\n")
        return directory

    def test_install_adds_hook_and_files(self):
        source = self.tree()
        row = geth.install(source, geth.FIXTURE, geth.SHARED)
        patched = (source / geth.ANCHOR_FILE).read_text()
        self.assertIn(geth.ANCHOR + geth.HOOK_CALL, patched)
        self.assertEqual(row["sha256_after"], sha(patched.encode()))
        for name, relative in geth.TREE_FILES.items():
            origin = (geth.SHARED if name in geth.SHARED_FILES else geth.FIXTURE) / name
            self.assertEqual((source / relative).read_bytes(), origin.read_bytes())

    def test_install_refuses_bad_anchor_or_existing_paths(self):
        for text in ("", "\tdefer evm.Release()\n\tdefer evm.Release()\n", "\tdefer evm.Release()\n// extractorGetHash\n"):
            with self.assertRaises(Exception):
                geth.install(self.tree(text), geth.FIXTURE, geth.SHARED)
        source = self.tree()
        (source / "cmd/extractor-inputs-probe").mkdir(parents=True)
        (source / "cmd/extractor-inputs-probe/probe.go").write_text("x")
        with self.assertRaises(Exception):
            geth.install(source, geth.FIXTURE, geth.SHARED)

    def test_native_mutants_apply_once_and_restore(self):
        source = self.tree()
        geth.install(source, geth.FIXTURE, geth.SHARED)
        table = json.loads((geth.FIXTURE / "native_mutants.json").read_text())
        self.assertGreaterEqual(len(table["mutants"]), 3)
        for mutant in table["mutants"]:
            path = source / geth.TREE_FILES[mutant["file"]]
            before = path.read_bytes()
            restore = geth.apply_mutant(source, mutant)
            self.assertNotEqual(path.read_bytes(), before)
            restore()
            self.assertEqual(path.read_bytes(), before)
        with self.assertRaises(Exception):
            geth.apply_mutant(source, {"name": "x", "file": "probe.go", "original": "no such text", "replacement": ""})

    def test_fixture_files_exist(self):
        for name in geth.FIXTURE_FILES:
            self.assertTrue((geth.FIXTURE / name).is_file(), name)


if __name__ == "__main__":
    unittest.main()
