"""Portable controls for the BSC source inputs driver; no Docker or Go toolchain needed.

A synthetic run that follows the probe protocol must pass `evaluate`, and each
single deviation must be rejected with the message of the check it breaks.
"""

import copy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

HARNESS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(HARNESS))
import bsc_source_inputs as driver
import source_inputs
from artifacts import Rejected

GENESIS_HASH = "0x" + "11" * 32
HASHES = ["0x" + "22" * 32, "0x" + "33" * 32]
ROOTS = ["0x" + "44" * 32, "0x" + "44" * 32]
RAW = [b"\xc3\x01\x02\x03", b"\xc1\x09"]
GENESIS = b'{"config":{}}'
COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
KEY = b"parlia-" + bytes.fromhex(GENESIS_HASH[2:])
REQUEST = (driver.GET_TAG + KEY).hex()
SNAPSHOT = b'{"number":0,"validators":{"0x03":{}}}'
TAMPERED = b'{"number":0,"validators":{"0xfc":{}}}'
FIXTURE = {"blocks": [{"number": 1, "hash": HASHES[0], "state_root": ROOTS[0]},
                      {"number": 2, "hash": HASHES[1], "state_root": ROOTS[1]}]}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def capsule(index):
    parents = [GENESIS_HASH, HASHES[0]]
    items = driver.expected_items(RAW[index], parents[index], GENESIS, COMMIT)
    return {"observation": driver.OBSERVATIONS[index],
            "items": [{"name": name, "length": len(data), "sha256": sha(data)} for name, data in items],
            "digest": source_inputs.capsule_digest(items).hex()}


def handoff(observation, sequence, response):
    return {"event": "handoff", "observation": observation, "sequence": sequence, "request_hex": REQUEST,
            "response_hex": response.hex(), "response_sha256": sha(response)}


def put(observation, record):
    return {"event": "put", "observation": observation, "record": record}


SYNC = {"event": "sync"}


def seal(observation, chunks=5):
    return [put(observation, "capsule-chunk")] * chunks + [SYNC, put(observation, "capsule"), SYNC, SYNC]


def new_response(observation, sequence, response):
    return [{"event": "fetch", "observation": observation, "tag": "bsc-parlia-db-get-v1"},
            put(observation, "entry-chunk"), put(observation, "entry"), SYNC, put(observation, "frontier"), SYNC,
            handoff(observation, sequence, response)]


def consumed(index, responses):
    rows = [{"request_hex": REQUEST, "response_hex": response.hex()} for response in responses]
    digest = capsule(index)["digest"]
    return {"observation": driver.OBSERVATIONS[index], "capsule_digest": digest, "count": len(rows),
            "chain": "unused-by-the-driver", "digest": driver.consumed_digest(digest, rows)}


def execute(observation):
    return {"event": "execute", "observation": observation, "frontier": 0}


def finished(index, manifest):
    return {"event": "finished", "observation": driver.OBSERVATIONS[index], "consumed": manifest}


def result(index, fetches=0, frontier=0, error=""):
    return {"observation": driver.OBSERVATIONS[index], "capsule": capsule(index), "frontier_at_start": frontier,
            "fetches": fetches, "head_number": index + 1, "head_hash": HASHES[index], "head_root": ROOTS[index],
            "error": error}


def synthetic():
    one, two = driver.OBSERVATIONS
    first = consumed(0, [b"\x00"])
    second = consumed(1, [b"\x01" + SNAPSHOT])
    capture = (seal(one) + [execute(one)] + new_response(one, 0, b"\x00") + [put(one, "closed"), SYNC, finished(0, first)]
               + seal(two) + [execute(two)] + new_response(two, 0, b"\x01" + SNAPSHOT)
               + [{"event": "crash", "observation": two, "sequence": 0}])
    resume = ([SYNC, SYNC, execute(two), handoff(two, 0, b"\x01" + SNAPSHOT), put(two, "closed"), SYNC,
               finished(1, second)])
    replay = []
    for index, (observation, manifest, response) in enumerate(((one, first, b"\x00"), (two, second, b"\x01" + SNAPSHOT))):
        replay += [SYNC, SYNC, execute(observation), handoff(observation, 0, response), finished(index, manifest)]
    traces = {"trace-capture.jsonl": capture, "trace-resume.jsonl": resume,
              "trace-control.jsonl": [{"event": "fetch", "observation": "", "tag": "bsc-parlia-db-get-v1"}],
              "trace-replay.jsonl": replay}
    resumed = dict(result(1, frontier=1), consumed=second)
    report = {
        "genesis_hash": GENESIS_HASH, "block_hashes": list(HASHES), "public_profile_credit": False,
        "child": {"error": "signal: killed", "killed_by_sigkill": True},
        "after_crash": {one + "_capsule": capsule(0), two + "_capsule": capsule(1), one + "_frontier": 1,
                        two + "_frontier": 1, "snapshot_key_hex": KEY.hex(),
                        "saved_response_sha256": sha(b"\x01" + SNAPSHOT), "saved_equals_live": True},
        "replay_tamper": {"live_before_tamper_sha256": sha(SNAPSHOT), "live_after_tamper_sha256": sha(TAMPERED),
                          "live_after_tamper_response_sha256": sha(b"\x01" + TAMPERED)},
        "resume": resumed,
        "control": {"stage": "insert", "error": "unauthorized validator: 0x03", "head_number": 1, "fetches": 3,
                    "live_before_tamper_sha256": sha(SNAPSHOT), "live_after_tamper_sha256": sha(TAMPERED)},
        "replay": [dict(result(0), consumed=first), dict(result(1), consumed=second)],
    }
    return report, traces


def evaluate(report, traces):
    return driver.checked_evaluate(report, traces, FIXTURE, RAW, GENESIS, COMMIT, GENESIS_HASH)


class Evaluate(unittest.TestCase):
    def assertRejected(self, change, message):
        report, traces = synthetic()
        change(report, traces)
        with self.assertRaisesRegex(Rejected, message):
            evaluate(report, traces)

    def test_synthetic_run_passes(self):
        report, traces = synthetic()
        summary = evaluate(report, traces)
        self.assertEqual(summary["block1_responses"], 1)
        self.assertEqual(summary["block2_responses"], 1)
        self.assertEqual(summary["crash_request"], KEY.hex())
        self.assertEqual(summary["consumed"], [consumed(0, [b"\x00"])["digest"],
                                               consumed(1, [b"\x01" + SNAPSHOT])["digest"]])

    def test_resume_may_fetch_beyond_the_saved_frontier(self):
        report, traces = synthetic()
        two = driver.OBSERVATIONS[1]
        extra = b"\x00"
        resume = traces["trace-resume.jsonl"]
        traces["trace-resume.jsonl"] = resume[:4] + new_response(two, 1, extra) + resume[4:]
        manifest = consumed(1, [b"\x01" + SNAPSHOT, extra])
        traces["trace-resume.jsonl"][-1] = finished(1, manifest)
        report["resume"].update(consumed=manifest, fetches=1)
        report["replay"][1]["consumed"] = manifest
        replay = traces["trace-replay.jsonl"]
        index = next(i for i, event in enumerate(replay) if event.get("event") == "handoff" and event["observation"] == two)
        replay.insert(index + 1, handoff(two, 1, extra))
        replay[-1] = finished(1, manifest)
        self.assertEqual(evaluate(report, traces)["block2_responses"], 2)

    def test_public_credit_is_rejected(self):
        self.assertRejected(lambda r, t: r.update(public_profile_credit=True), "public-profile credit")

    def test_other_chain_is_rejected(self):
        self.assertRejected(lambda r, t: r.update(genesis_hash="0x" + "99" * 32), "another chain")
        self.assertRejected(lambda r, t: r["block_hashes"].reverse(), "another chain")

    def test_child_must_be_killed(self):
        self.assertRejected(lambda r, t: r["child"].update(killed_by_sigkill=False), "not killed by SIGKILL")

    def test_capsule_must_be_synced_before_execute(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = capture.index(execute(driver.OBSERVATIONS[0]))
            capture.insert(index, put(driver.OBSERVATIONS[0], "capsule"))
        self.assertRejected(change, "capsule not synced before execution")

    def test_capsule_write_without_any_sync_is_rejected(self):
        def change(report, traces):
            traces["trace-capture.jsonl"] = [event for event in traces["trace-capture.jsonl"][:9] if event != SYNC] + \
                traces["trace-capture.jsonl"][9:]
        self.assertRejected(change, "capsule not synced before execution")

    def test_execute_needs_a_sync_even_without_capsule_writes(self):
        def change(report, traces):
            replay = traces["trace-replay.jsonl"]
            del replay[0:2]
        self.assertRejected(change, "capsule not synced before execution")

    def test_read_before_execute_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            capture.insert(0, {"event": "fetch", "observation": driver.OBSERVATIONS[0]})
        self.assertRejected(change, "a read before execution")

    def test_one_execute_per_observation(self):
        def change(report, traces):
            traces["trace-replay.jsonl"].insert(2, execute(driver.OBSERVATIONS[0]))
        self.assertRejected(change, "expected one execute event")

    def test_handoff_before_frontier_sync_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = next(i for i, event in enumerate(capture) if event.get("event") == "handoff")
            del capture[index - 1]
        self.assertRejected(change, "was not durable first")

    def test_handoff_without_chunks_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            capture.remove(put(driver.OBSERVATIONS[0], "entry-chunk"))
        self.assertRejected(change, "was not durable first")

    def test_handoff_without_fetch_first_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = next(i for i, event in enumerate(capture) if event.get("event") == "fetch")
            capture[index], capture[index + 1] = capture[index + 1], capture[index]
        self.assertRejected(change, "was not durable first")

    def test_handoff_must_start_with_its_fetch(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = next(i for i, event in enumerate(capture) if event.get("event") == "fetch")
            capture[index] = put(driver.OBSERVATIONS[0], "entry-chunk")
        self.assertRejected(change, "was not durable first")

    def test_handoff_with_another_event_among_the_chunks_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            capture[capture.index(put(driver.OBSERVATIONS[0], "entry-chunk"))] = SYNC
        self.assertRejected(change, "was not durable first")

    def test_frontier_before_entry_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            one = driver.OBSERVATIONS[0]
            index = capture.index(put(one, "entry"))
            capture[index], capture[index + 2] = put(one, "frontier"), put(one, "entry")
        self.assertRejected(change, "was not durable first")

    def test_write_of_another_observation_is_rejected(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = capture.index(put(driver.OBSERVATIONS[0], "entry"))
            capture[index] = put(driver.OBSERVATIONS[1], "entry")
        self.assertRejected(change, "a write of another observation")

    def test_handoff_sequence_and_digest(self):
        def sequence(report, traces):
            next(event for event in traces["trace-capture.jsonl"] if event.get("event") == "handoff")["sequence"] = 1
        self.assertRejected(sequence, "hand-off sequence")

        def digest(report, traces):
            next(event for event in traces["trace-capture.jsonl"] if event.get("event") == "handoff")[
                "response_sha256"] = "00" * 32
        self.assertRejected(digest, "hand-off digest")

    def test_block_one_must_fetch(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = next(i for i, event in enumerate(capture) if event.get("event") == "handoff")
            del capture[index - 6:index]
        self.assertRejected(change, "Block 1 capture must fetch")

    def test_block_one_fetch_without_handoff(self):
        def change(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = next(i for i, event in enumerate(capture) if event.get("event") == "handoff")
            capture.insert(index + 1, {"event": "fetch", "observation": driver.OBSERVATIONS[0]})
        self.assertRejected(change, "fetch without a hand-off")

    def test_close_steps(self):
        def missing(report, traces):
            traces["trace-capture.jsonl"].remove(put(driver.OBSERVATIONS[0], "closed"))
        self.assertRejected(missing, "close steps")

        def extra(report, traces):
            replay = traces["trace-replay.jsonl"]
            replay.insert(4, SYNC)
        self.assertRejected(extra, "close steps")

        def twice(report, traces):
            traces["trace-capture.jsonl"].insert(-1, finished(0, consumed(0, [b"\x00"])))
        self.assertRejected(twice, "expected one finished event")

    def test_crash_must_follow_the_first_block_two_handoff(self):
        def no_crash(report, traces):
            traces["trace-capture.jsonl"].pop()
        self.assertRejected(no_crash, "The crash is not the hand-off of block 2")

        def two_handoffs(report, traces):
            capture = traces["trace-capture.jsonl"]
            capture[-1:] = [handoff(driver.OBSERVATIONS[1], 1, b"\x00")] + capture[-1:]
        self.assertRejected(two_handoffs, "must stop at its first new hand-off")

        def replayed(report, traces):
            capture = traces["trace-capture.jsonl"]
            index = len(capture) - 8
            del capture[index:index + 6]
        self.assertRejected(replayed, "must stop at its first new hand-off")

        def wrong_sequence(report, traces):
            traces["trace-capture.jsonl"][-1] = {"event": "crash", "observation": driver.OBSERVATIONS[1], "sequence": 1}
        self.assertRejected(wrong_sequence, "The crash is not the hand-off of block 2")

        def other_event(report, traces):
            capture = traces["trace-capture.jsonl"]
            capture.insert(-1, SYNC)
        self.assertRejected(other_event, "The crash is not the hand-off of block 2")

    def test_crashed_read_must_be_a_stored_snapshot(self):
        def other_key(report, traces):
            for name in ("trace-capture.jsonl",):
                traces[name][-2]["request_hex"] = (driver.GET_TAG + b"other").hex()
        self.assertRejected(other_key, "not a Parlia snapshot read")

        def missing(report, traces):
            event = traces["trace-capture.jsonl"][-2]
            event.update(response_hex="00", response_sha256=sha(b"\x00"))
        self.assertRejected(missing, "did not return a stored snapshot")

        def missing_with_bytes(report, traces):
            event = traces["trace-capture.jsonl"][-2]
            event.update(response_hex="00abcd", response_sha256=sha(b"\x00\xab\xcd"))
        self.assertRejected(missing_with_bytes, "did not return a stored snapshot")

        def status_only(report, traces):
            event = traces["trace-capture.jsonl"][-2]
            event.update(response_hex="01", response_sha256=sha(b"\x01"))
        self.assertRejected(status_only, "did not return a stored snapshot")

    def test_capsule_checks(self):
        one = driver.OBSERVATIONS[0]
        self.assertRejected(lambda r, t: r["after_crash"][one + "_capsule"].update(observation="x"), "capsule identity")
        self.assertRejected(lambda r, t: r["after_crash"][one + "_capsule"]["items"].pop(), "capsule items differ")
        self.assertRejected(lambda r, t: r["after_crash"][one + "_capsule"].update(digest="00" * 32),
                            "capsule digest differs")
        self.assertRejected(lambda r, t: r["resume"]["capsule"]["items"].reverse(), "capsule items differ")
        self.assertRejected(lambda r, t: r["replay"][0]["capsule"].update(digest="00" * 32), "capsule digest differs")

    def test_block_one_manifest_is_recomputed(self):
        def change(report, traces):
            event = next(e for e in traces["trace-capture.jsonl"] if e.get("event") == "finished")
            event["consumed"] = dict(event["consumed"], digest="00" * 32)
        self.assertRejected(change, "Block 1 consumed manifest")

        def count(report, traces):
            event = next(e for e in traces["trace-capture.jsonl"] if e.get("event") == "finished")
            event["consumed"] = dict(event["consumed"], count=2)
        self.assertRejected(count, "Block 1 consumed manifest")

        def capsule_digest(report, traces):
            event = next(e for e in traces["trace-capture.jsonl"] if e.get("event") == "finished")
            event["consumed"] = dict(event["consumed"], capsule_digest="00" * 32)
        self.assertRejected(capsule_digest, "Block 1 consumed manifest")

    def test_after_crash_checks(self):
        one, two = driver.OBSERVATIONS
        self.assertRejected(lambda r, t: r["after_crash"].update({one + "_frontier": 0}), "Durable frontiers")
        self.assertRejected(lambda r, t: r["after_crash"].update({two + "_frontier": 0}), "Durable frontiers")
        self.assertRejected(lambda r, t: r["after_crash"].update(saved_response_sha256="00" * 32),
                            "saved response differs from the bytes handed")
        self.assertRejected(lambda r, t: r["after_crash"].update(saved_equals_live=False), "not the stored snapshot")

    def test_replay_tamper_checks(self):
        self.assertRejected(lambda r, t: r["replay_tamper"].update(live_before_tamper_sha256=sha(TAMPERED)),
                            "snapshot was not tampered")
        self.assertRejected(lambda r, t: r["replay_tamper"].update(live_before_tamper_sha256=sha(b"other")),
                            "snapshot was not tampered")
        self.assertRejected(lambda r, t: r["replay_tamper"].update(live_after_tamper_sha256=sha(SNAPSHOT)),
                            "snapshot was not tampered")

        def handed_tampered(report, traces):
            report["replay_tamper"]["live_after_tamper_response_sha256"] = sha(b"\x01" + SNAPSHOT)
        self.assertRejected(handed_tampered, "Replay of bsc-block-2 handed the tampered snapshot")

    def test_resume_checks(self):
        self.assertRejected(lambda r, t: r["resume"].update(error="boom"), "Resume failed")
        self.assertRejected(lambda r, t: r["resume"].update(frontier_at_start=0), "Resume failed")
        self.assertRejected(lambda r, t: r["resume"].update(head_number=1), "Resume did not import block 2")
        self.assertRejected(lambda r, t: r["resume"].update(head_hash=HASHES[0]), "Resume did not import block 2")
        self.assertRejected(lambda r, t: r["resume"].update(head_root="0x" + "55" * 32), "Resume did not import block 2")
        self.assertRejected(lambda r, t: r["resume"].update(fetches=1), "fetched below the saved frontier")

    def test_resume_must_replay_the_saved_bytes(self):
        def fetched(report, traces):
            resume = traces["trace-resume.jsonl"]
            two = driver.OBSERVATIONS[1]
            resume[3:4] = new_response(two, 0, b"\x01" + SNAPSHOT)
            report["resume"]["fetches"] = 1
        self.assertRejected(fetched, "Resume did not replay the saved response")

        def other_bytes(report, traces):
            traces["trace-resume.jsonl"][3] = handoff(driver.OBSERVATIONS[1], 0, b"\x01" + TAMPERED)
        self.assertRejected(other_bytes, "Resume did not replay the saved response")

        def other_request(report, traces):
            traces["trace-resume.jsonl"][3]["request_hex"] = (driver.GET_TAG + b"parlia-x").hex()
        self.assertRejected(other_request, "Resume did not replay the saved response")

        def second_replay(report, traces):
            two = driver.OBSERVATIONS[1]
            responses = [b"\x01" + SNAPSHOT, b"\x00"]
            manifest = consumed(1, responses)
            traces["trace-resume.jsonl"] = [
                {"event": "fetch", "observation": "", "tag": "bsc-parlia-db-get-v1"}, SYNC, SYNC, execute(two),
                handoff(two, 0, responses[0]), handoff(two, 1, responses[1]), put(two, "closed"), SYNC,
                finished(1, manifest)]
            report["resume"].update(consumed=manifest, fetches=1)
        self.assertRejected(second_replay, "fetched below the saved frontier")

        def unlogged_fetch(report, traces):
            traces["trace-resume.jsonl"].insert(0, {"event": "fetch", "observation": ""})
        self.assertRejected(unlogged_fetch, "fetched below the saved frontier")

        def replay_after_new(report, traces):
            resume = traces["trace-resume.jsonl"]
            resume.insert(4, handoff(driver.OBSERVATIONS[1], 1, b"\x00"))
        self.assertRejected(replay_after_new, "fetched below the saved frontier")

    def test_block_two_manifest_is_recomputed(self):
        def digest(report, traces):
            manifest = dict(report["resume"]["consumed"], digest="00" * 32)
            report["resume"]["consumed"] = manifest
            traces["trace-resume.jsonl"][-1] = finished(1, manifest)
        self.assertRejected(digest, "Block 2 consumed manifest")

        def count(report, traces):
            manifest = dict(report["resume"]["consumed"], count=2)
            report["resume"]["consumed"] = manifest
            traces["trace-resume.jsonl"][-1] = finished(1, manifest)
        self.assertRejected(count, "Block 2 consumed manifest")

        def differs(report, traces):
            report["resume"]["consumed"] = dict(report["resume"]["consumed"], chain="other")
        self.assertRejected(differs, "Block 2 consumed manifest")

    def test_control_checks(self):
        self.assertRejected(lambda r, t: r["control"].update(error="<nil>"), "Control did not fail")
        self.assertRejected(lambda r, t: r["control"].update(stage="other"), "Control did not fail")
        self.assertRejected(lambda r, t: r["control"].update(live_before_tamper_sha256="00" * 32), "another snapshot")
        self.assertRejected(lambda r, t: r["control"].update(live_after_tamper_sha256="00" * 32), "another snapshot")
        self.assertRejected(lambda r, t: r["control"].update(head_number=2), "Control imported block 2")

    def test_control_may_fail_at_open(self):
        report, traces = synthetic()
        report["control"] = {key: value for key, value in report["control"].items() if key != "head_number"}
        report["control"]["stage"] = "open"
        evaluate(report, traces)

    def test_replay_checks(self):
        self.assertRejected(lambda r, t: r["replay"].pop(), "Replay must cover both blocks")
        self.assertRejected(lambda r, t: t["trace-replay.jsonl"].append({"event": "fetch"}), "Replay fetched or wrote")
        self.assertRejected(lambda r, t: t["trace-replay.jsonl"].insert(0, put("x", "other")), "Replay fetched or wrote")
        for field, value in (("error", "boom"), ("fetches", 1), ("head_number", 2), ("head_hash", HASHES[1]),
                             ("head_root", "0x" + "55" * 32)):
            self.assertRejected(lambda r, t: r["replay"][0].update({field: value}), "Replay of bsc-block-1 failed")

    def test_replay_manifests(self):
        def changed(report, traces):
            manifest = dict(report["replay"][1]["consumed"], digest="00" * 32)
            report["replay"][1]["consumed"] = manifest
            traces["trace-replay.jsonl"][-1] = finished(1, manifest)
        self.assertRejected(changed, "Replay of bsc-block-2 changed its manifest")

        def trace_differs(report, traces):
            traces["trace-replay.jsonl"][-1] = finished(1, dict(report["replay"][1]["consumed"], count=9))
        self.assertRejected(trace_differs, "Replay of bsc-block-2 changed its manifest")

        def report_only(report, traces):
            report["replay"][1]["consumed"] = dict(report["replay"][1]["consumed"], count=7)
        self.assertRejected(report_only, "Replay of bsc-block-2 changed its manifest")

        def first_changed(report, traces):
            manifest = dict(report["replay"][0]["consumed"], chain="other")
            report["replay"][0]["consumed"] = manifest
            event = next(e for e in traces["trace-replay.jsonl"] if e.get("event") == "finished")
            event["consumed"] = manifest
        self.assertRejected(first_changed, "Replay of bsc-block-1 changed its manifest")

    def test_malformed_report_is_rejected(self):
        self.assertRejected(lambda r, t: r.pop("resume"), "Malformed probe report")
        self.assertRejected(lambda r, t: t.pop("trace-replay.jsonl"), "Malformed probe report")


class Helpers(unittest.TestCase):
    def test_split_rlp_list_items(self):
        long_list = b"\xf8\x38" + b"\x01" * 0x38
        self.assertEqual(driver.split_rlp_list_items(b"\xc2\x01\x02" + long_list + b"\xc0"),
                         [b"\xc2\x01\x02", long_list, b"\xc0"])
        self.assertEqual(driver.split_rlp_list_items(b"\xf7" + b"\x02" * 0x37), [b"\xf7" + b"\x02" * 0x37])
        with self.assertRaisesRegex(Rejected, "not an RLP list"):
            driver.split_rlp_list_items(b"\x80")
        with self.assertRaisesRegex(Rejected, "Truncated"):
            driver.split_rlp_list_items(b"\xc3\x01")
        with self.assertRaisesRegex(Rejected, "Truncated"):
            driver.split_rlp_list_items(b"\xf8\x38\x01")

    def test_expected_items(self):
        items = driver.expected_items(b"\xc0", "0x" + "ab" * 32, b"{}", "abc")
        self.assertEqual([name for name, _ in items],
                         ["block-rlp", "parent-hash", "genesis-json", "native-commit", "state-profile"])
        self.assertEqual(items[1][1], bytes.fromhex("ab" * 32))
        self.assertEqual(items[4][1], b"state.scheme=hash;gcmode=full;snapshots=off")

    def test_go_constants_match_the_driver(self):
        go = (Path(__file__).with_name("bsc_inputs.go")).read_text()
        self.assertIn(f'StateProfile = "{driver.STATE_PROFILE.decode()}"', go)
        self.assertIn(f'ParliaGetTag = "{driver.GET_TAG[:-1].decode()}"', go)

    def test_test_names(self):
        with tempfile.TemporaryDirectory() as name:
            path = Path(name) / "x_test.go"
            path.write_text("func TestB(t *testing.T) {}\nfunc TestA(t *testing.T) {}\n"
                            f"func {driver.CHILD_TEST}(t *testing.T) {{}}\nfunc helper(t *testing.T) {{}}\n")
            self.assertEqual(driver.test_names([path]), ["TestA", "TestB"])
            path.write_text("func TestA(t *testing.T) {}\nfunc TestA(t *testing.T) {}\n")
            with self.assertRaisesRegex(Rejected, "Duplicate"):
                driver.test_names([path])

    def test_repository_test_names_cover_both_files(self):
        names = driver.test_names([driver.CORE / "inputs_test.go", driver.FIXTURE / "bsc_inputs_test.go"])
        self.assertIn("TestDigestVectors", names)
        self.assertIn("TestNativeBSCInputs", names)
        self.assertNotIn(driver.CHILD_TEST, names)

    def test_install_refuses_an_existing_package(self):
        with tempfile.TemporaryDirectory() as name:
            source = Path(name) / "source"
            (source / driver.INSTALL).mkdir(parents=True)
            with self.assertRaisesRegex(Rejected, "already exists"):
                driver.install(source, Path(name) / "retained", None)

    def test_install_copies_core_and_probe(self):
        with tempfile.TemporaryDirectory() as name:
            # The real path, because the artifact helpers refuse symlinks (macOS /var).
            source = Path(name).resolve() / "source"
            source.mkdir()
            rows = driver.install(source, source.parent / "retained", None)
            self.assertEqual([row["relative"] for row in rows],
                             [f"{driver.INSTALL}/{file}" for file in driver.CORE_FILES + driver.PROBE_FILES])
            for row in rows:
                self.assertEqual((source / row["relative"]).read_bytes(), Path(row["source"]["path"]).read_bytes())

    def test_failing_tests(self):
        self.assertEqual(driver.failing_tests("--- FAIL: TestB (1s)\n    --- FAIL: TestA (0s)\nok\n"),
                         ["TestA", "TestB"])

    def test_mutant_table(self):
        table = driver.load_table(driver.FIXTURE / "source_inputs_bsc.mutants.json")
        self.assertTrue(table["mutants"])
        with tempfile.TemporaryDirectory() as name:
            path = Path(name) / "t.json"
            bad = copy.deepcopy(table)
            bad["mutants"].append(dict(bad["mutants"][0]))
            path.write_text(json.dumps(bad))
            with self.assertRaisesRegex(Rejected, "unique"):
                driver.load_table(path)
            bad = copy.deepcopy(table)
            bad["mutants"][0]["original"] = "no such fragment"
            path.write_text(json.dumps(bad))
            with self.assertRaisesRegex(Rejected, "exactly once"):
                driver.load_table(path)
            bad = copy.deepcopy(table)
            bad["mutants"][0]["file"] = "inputs.go"
            path.write_text(json.dumps(bad))
            with self.assertRaisesRegex(Rejected, "unsupported shape"):
                driver.load_table(path)
            bad = copy.deepcopy(table)
            bad["mutants"][0]["replacement"] = bad["mutants"][0]["original"]
            path.write_text(json.dumps(bad))
            with self.assertRaisesRegex(Rejected, "changes nothing"):
                driver.load_table(path)
            bad = copy.deepcopy(table)
            bad["language"] = "go"
            path.write_text(json.dumps(bad))
            with self.assertRaisesRegex(Rejected, "Unsupported mutant table"):
                driver.load_table(path)


if __name__ == "__main__":
    unittest.main()
