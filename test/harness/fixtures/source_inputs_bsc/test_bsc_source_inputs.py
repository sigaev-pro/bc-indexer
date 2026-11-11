"""Portable controls for the BSC source inputs driver; no Docker or Go toolchain needed.

A synthetic run that follows the probe protocol must pass `evaluate`, and each
single deviation must be rejected with the message of the check it breaks.
"""

import copy
import functools
import hashlib
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest
import zlib

HARNESS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(HARNESS))
import bsc_source_inputs as driver
import source_inputs
from artifacts import Rejected



def enc_bytes(data):
    """Test-only RLP string encoder, independent of the driver."""
    if len(data) == 1 and data[0] < 0x80:
        return data
    if len(data) <= 55:
        return bytes([0x80 + len(data)]) + data
    size = (len(data).bit_length() + 7) // 8
    return bytes([0xb7 + size]) + len(data).to_bytes(size, "big") + data


def enc_list(items):
    payload = b"".join(items)
    if len(payload) <= 55:
        return bytes([0xc0 + len(payload)]) + payload
    size = (len(payload).bit_length() + 7) // 8
    return bytes([0xf7 + size]) + len(payload).to_bytes(size, "big") + payload


def enc_int(value):
    return enc_bytes(value.to_bytes((value.bit_length() + 7) // 8, "big"))


def header(parent, number, root, time):
    fields = [enc_bytes(parent), enc_bytes(b"\x1d" * 32), enc_bytes(b"\x00" * 20), enc_bytes(root),
              enc_bytes(b"\x56" * 32), enc_bytes(b"\x56" * 32), enc_bytes(b"\x00" * 256), enc_int(2), enc_int(number),
              enc_int(30_000_000), enc_int(0), enc_int(time), enc_bytes(b"x"), enc_bytes(b"\x00" * 32),
              enc_bytes(b"\x00" * 8)]
    return enc_list(fields)


ROOTS = ["0x" + "44" * 32, "0x" + "44" * 32]
GENESIS_HEADER = header(b"\x00" * 32, 0, b"\x33" * 32, 0x10)
HEADER_1 = header(driver.keccak256(GENESIS_HEADER), 1, bytes.fromhex("44" * 32), 0x13)
HEADER_2 = header(driver.keccak256(HEADER_1), 2, bytes.fromhex("44" * 32), 0x16)
GENESIS_HASH = "0x" + driver.keccak256(GENESIS_HEADER).hex()
HASHES = ["0x" + driver.keccak256(HEADER_1).hex(), "0x" + driver.keccak256(HEADER_2).hex()]
RAW = [enc_list([HEADER_1, b"\xc0", b"\xc0"]), enc_list([HEADER_2, b"\xc0", b"\xc0"])]
GENESIS = (b'{"config":{"chainId":714,"homesteadBlock":0,"brunoBlock":1,"eulerBlock":2,"keplerTime":0,'
           b'"shanghaiTime":20},"timestamp":"0x10"}')
COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
SEED = {"headers": 1, "body_sha256": "ab" * 32, "state_nodes": 5, "codes": 0}
KEY = b"parlia-" + bytes.fromhex(GENESIS_HASH[2:])
REQUEST = (driver.GET_TAG + KEY).hex()
SNAPSHOT = b'{"number":0,"validators":{"0x03":{}}}'
TAMPERED = b'{"number":0,"validators":{"0xfc":{}}}'
FIXTURE = {"blocks": [{"number": 1, "hash": HASHES[0], "state_root": ROOTS[0]},
                      {"number": 2, "hash": HASHES[1], "state_root": ROOTS[1]}]}


def sha(data):
    return hashlib.sha256(data).hexdigest()


@functools.lru_cache(maxsize=None)
def items(index):
    return driver.expected_items(index, RAW, GENESIS_HEADER, GENESIS, COMMIT)


def capsule(index):
    items_ = items(index)
    return {"observation": driver.OBSERVATIONS[index],
            "items": [{"name": name, "length": len(data), "sha256": sha(data)} for name, data in items_],
            "digest": source_inputs.capsule_digest(items_).hex()}


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
    """Expected manifest, built from the source_inputs primitives, not from the driver."""
    digest = capsule(index)["digest"]
    chain = source_inputs.chain_start(bytes.fromhex(digest))
    for sequence, response in enumerate(responses):
        chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, bytes.fromhex(REQUEST), response))
    expected = source_inputs.consumed_digest(bytes.fromhex(digest), len(responses), chain).hex()
    return {"observation": driver.OBSERVATIONS[index], "capsule_digest": digest, "count": len(responses),
            "chain": "unused-by-the-driver", "digest": expected}


def execute(observation):
    return {"event": "execute", "observation": observation, "frontier": 0}


def finished(index, manifest):
    return {"event": "finished", "observation": driver.OBSERVATIONS[index], "consumed": manifest}


def result(index, fetches=0, frontier=0, error=""):
    return {"observation": driver.OBSERVATIONS[index], "capsule": capsule(index), "frontier_at_start": frontier,
            "fetches": fetches, "head_number": index + 1, "head_hash": HASHES[index], "head_root": ROOTS[index],
            "error": error, "static_items_hex": {name: data.hex() for name, data in items(index)}}


def seed(**changes):
    return {"event": "seed", "observation": driver.OBSERVATIONS[1], "seed": dict(SEED, **changes), "error": "<nil>"}


def preflight(error="<nil>"):
    return {"event": "preflight", "observation": driver.OBSERVATIONS[1], "error": error}


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
    state_error = "parent state is missing from the home: root 44: missing trie node"
    traces = {"trace-capture.jsonl": capture, "trace-resume.jsonl": resume,
              "trace-control.jsonl": [{"event": "fetch", "observation": "", "tag": "bsc-parlia-db-get-v1"}],
              "trace-replay.jsonl": replay,
              "trace-single.jsonl": [seed(), preflight(), SYNC, SYNC, execute(two), handoff(two, 0, b"\x01" + SNAPSHOT),
                                     finished(1, second)],
              "trace-single-no-state.jsonl": [seed(state_nodes=0), preflight(state_error)],
              "trace-single-no-headers.jsonl": [seed(headers=0), preflight(),
                                                {"event": "fetch", "observation": "", "tag": "bsc-parlia-db-get-v1"}]}
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
        "single": {"stage": "observe", "error": "", "seed": dict(SEED), "observation": dict(result(1), consumed=second),
                   "head_number": 2},
        "single_no_state": {"stage": "preflight", "error": state_error, "seed": dict(SEED, state_nodes=0),
                            "observation": None, "head_number": 0},
        "single_no_headers": {"stage": "import", "error": "unknown ancestor", "seed": dict(SEED, headers=0),
                              "observation": None, "head_number": 0},
    }
    return report, traces


def evaluate(report, traces, raw=None):
    return driver.checked_evaluate(report, traces, FIXTURE, raw or RAW, GENESIS, COMMIT, GENESIS_HASH)


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
        self.assertEqual(summary["static_items"], list(driver.ITEM_NAMES))
        self.assertEqual(summary["capsule_digests"], [capsule(0)["digest"], capsule(1)["digest"]])
        self.assertEqual(summary["single_seed"], SEED)
        self.assertEqual(summary["single_consumed"], consumed(1, [b"\x01" + SNAPSHOT])["digest"])
        self.assertIn("parent state is missing", summary["no_state_error"])
        self.assertEqual(summary["no_headers_error"], "unknown ancestor")

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
        report["single"]["observation"]["consumed"] = manifest
        single = traces["trace-single.jsonl"]
        single.insert(-1, handoff(two, 1, extra))
        single[-1] = finished(1, manifest)
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
                traces[name][-2]["request_hex"] = (driver.HAS_TAG + KEY).hex()
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
            traces["trace-resume.jsonl"][3]["request_hex"] = (driver.HAS_TAG + KEY).hex()
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

    def test_static_item_bytes(self):
        def changed(item, *path):
            def change(report, traces):
                target = report
                for key in path:
                    target = target[key]
                target["static_items_hex"][item] = "00"
            return change
        self.assertRejected(changed("fork-id", "resume"), "bsc-block-2: static item bytes differ")
        self.assertRejected(changed("normalization-profile", "replay", 1), "bsc-block-2: static item bytes differ")
        self.assertRejected(changed("ancestor-headers-rlp", "single", "observation"), "bsc-block-2: static item bytes differ")
        self.assertRejected(lambda r, t: r["replay"][0]["static_items_hex"].pop("state-profile"),
                            "bsc-block-1: static item bytes differ")
        self.assertRejected(lambda r, t: r["replay"][0]["static_items_hex"].update(extra="00"),
                            "bsc-block-1: static item bytes differ")

    def test_genesis_header_must_hash_to_the_genesis(self):
        self.assertRejected(lambda r, t: r["replay"][0]["static_items_hex"].update({"parent-header-rlp": HEADER_1.hex()}),
                            "genesis header does not hash")
        other = header(b"\x00" * 32, 0, b"\x34" * 32, 0x10)
        self.assertRejected(lambda r, t: r["replay"][0]["static_items_hex"].update({"parent-header-rlp": other.hex()}),
                            "genesis header does not hash")

        def numbered(report, traces):
            # A header of another number that the report also claims as the genesis.
            report["genesis_hash"] = HASHES[0]
            report["replay"][0]["static_items_hex"]["parent-header-rlp"] = HEADER_1.hex()
        with self.assertRaisesRegex(Rejected, "genesis header does not hash"):
            report, traces = synthetic()
            numbered(report, traces)
            driver.checked_evaluate(report, traces, FIXTURE, RAW, GENESIS, COMMIT, HASHES[0])

    def test_fixture_block_identity(self):
        report, traces = synthetic()
        with self.assertRaisesRegex(Rejected, "Fixture block 1 identity"):
            evaluate(report, traces, raw=[RAW[1], RAW[0]])
        orphan = enc_list([header(b"\x07" * 32, 1, bytes.fromhex("44" * 32), 0x13), b"\xc0", b"\xc0"])
        fixture = copy.deepcopy(FIXTURE)
        fixture["blocks"][0]["hash"] = "0x" + driver.keccak256(driver.rlp_list(orphan)[0]).hex()
        report["block_hashes"][0] = fixture["blocks"][0]["hash"]
        with self.assertRaisesRegex(Rejected, "Fixture block 1 identity"):
            driver.checked_evaluate(report, traces, fixture, [orphan, RAW[1]], GENESIS, COMMIT, GENESIS_HASH)
        fixture = copy.deepcopy(FIXTURE)
        fixture["blocks"][1]["hash"] = "0x" + "77" * 32
        report["block_hashes"] = [HASHES[0], fixture["blocks"][1]["hash"]]
        with self.assertRaisesRegex(Rejected, "Fixture block 2 identity"):
            driver.checked_evaluate(report, traces, fixture, RAW, GENESIS, COMMIT, GENESIS_HASH)
        wrong_number = enc_list([header(bytes.fromhex(GENESIS_HASH[2:]), 5, bytes.fromhex("44" * 32), 0x13), b"\xc0",
                                 b"\xc0"])
        fixture["blocks"][0]["hash"] = "0x" + driver.keccak256(driver.rlp_list(wrong_number)[0]).hex()
        report["block_hashes"][0] = fixture["blocks"][0]["hash"]
        with self.assertRaisesRegex(Rejected, "Fixture block 1 identity"):
            driver.checked_evaluate(report, traces, fixture, [wrong_number, RAW[1]], GENESIS, COMMIT, GENESIS_HASH)

    def test_block_two_parent_root(self):
        fixture = copy.deepcopy(FIXTURE)
        fixture["blocks"][0]["state_root"] = "0x" + "55" * 32
        report, traces = synthetic()
        with self.assertRaisesRegex(Rejected, "Block 2 parent root"):
            driver.checked_evaluate(report, traces, fixture, RAW, GENESIS, COMMIT, GENESIS_HASH)

    def test_reads_must_be_declared(self):
        other = (driver.GET_TAG + b"parlia-" + b"\x01" * 32).hex()

        def capture_one(report, traces):
            next(e for e in traces["trace-capture.jsonl"] if e.get("event") == "handoff")["request_hex"] = other
        self.assertRejected(capture_one, "bsc-block-1: a read of an undeclared key")

        def capture_two(report, traces):
            traces["trace-capture.jsonl"][-2]["request_hex"] = other
        self.assertRejected(capture_two, "bsc-block-2: a read of an undeclared key")

        def resume(report, traces):
            traces["trace-resume.jsonl"].insert(-3, dict(handoff(driver.OBSERVATIONS[1], 1, b"\x00"), request_hex=other))
        self.assertRejected(resume, "bsc-block-2: a read of an undeclared key")

        def replay(report, traces):
            next(e for e in traces["trace-replay.jsonl"] if e.get("event") == "handoff")["request_hex"] = other
        self.assertRejected(replay, "bsc-block-1: a read of an undeclared key")

        def single(report, traces):
            traces["trace-single.jsonl"][5]["request_hex"] = other
        self.assertRejected(single, "bsc-block-2: a read of an undeclared key")

        def unknown_tag(report, traces):
            traces["trace-single.jsonl"][5]["request_hex"] = (b"bsc-parlia-db-xyz-v1\x00" + KEY).hex()
        self.assertRejected(unknown_tag, "bsc-block-2: a read of an undeclared key")

    def test_single_replay_passes_with_a_has_read(self):
        report, traces = synthetic()
        traces["trace-single.jsonl"][5]["request_hex"] = (driver.HAS_TAG + KEY).hex()
        self.assertEqual(evaluate(report, traces)["single_seed"], SEED)

    def test_single_replay_checks(self):
        self.assertRejected(lambda r, t: r["single"].update(stage="preflight"), "Single replay failed")
        self.assertRejected(lambda r, t: r["single"].update(error="boom"), "Single replay failed")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].pop(0), "must start with its seed")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"][0].update(observation="bsc-block-1"),
                            "must start with its seed")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"][0].update(error="boom"), "Single replay seed")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"][0]["seed"].update(codes=1), "Single replay seed")
        for field, value in (("headers", 2), ("headers", 0), ("state_nodes", 0), ("body_sha256", "")):
            def change(report, traces, field=field, value=value):
                report["single"]["seed"][field] = value
                traces["trace-single.jsonl"][0]["seed"][field] = value
            self.assertRejected(change, "Single replay seed")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].pop(1), "Single replay preflight")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"][1].update(error="boom"), "Single replay preflight")
        for field, value in (("error", "boom"), ("fetches", 1), ("head_number", 1), ("head_hash", HASHES[0]),
                             ("head_root", "0x" + "55" * 32)):
            self.assertRejected(lambda r, t: r["single"]["observation"].update({field: value}),
                                "Single replay did not import block 2")
        self.assertRejected(lambda r, t: r["single"]["observation"]["capsule"].update(digest="00" * 32),
                            "capsule digest differs")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].append({"event": "fetch"}), "Single replay fetched or wrote")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].insert(2, put("x", "other")),
                            "Single replay fetched or wrote")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].append({"event": "x", "observation": "bsc-block-1"}),
                            "Single replay ran block 1")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].__delitem__(slice(2, 4)), "capsule not synced")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].__delitem__(5), "Single replay fetched")

        def manifest(report, traces):
            changed = dict(report["single"]["observation"]["consumed"], digest="00" * 32)
            report["single"]["observation"]["consumed"] = changed
            traces["trace-single.jsonl"][-1] = finished(1, changed)
        self.assertRejected(manifest, "Single replay changed the block 2 manifest")
        self.assertRejected(lambda r, t: t["trace-single.jsonl"].__setitem__(
            -1, finished(1, dict(r["single"]["observation"]["consumed"], count=5))), "Single replay changed")

    def test_state_withheld_control(self):
        message = "State-withheld control did not stop at the preflight"
        self.assertRejected(lambda r, t: r["single_no_state"].update(stage="observe"), message)
        self.assertRejected(lambda r, t: r["single_no_state"].update(error="other"), message)
        self.assertRejected(lambda r, t: t["trace-single-no-state.jsonl"][0].update(error="boom"), message)
        self.assertRejected(lambda r, t: r["single_no_state"]["seed"].update(state_nodes=5), message)
        self.assertRejected(lambda r, t: r["single_no_state"]["seed"].update(headers=0), message)
        self.assertRejected(lambda r, t: t["trace-single-no-state.jsonl"].pop(1), message)
        self.assertRejected(lambda r, t: t["trace-single-no-state.jsonl"][1].update(error="<nil>"), message)
        self.assertRejected(lambda r, t: t["trace-single-no-state.jsonl"].pop(0), "must start with its seed")
        for name in ("execute", "handoff", "fetch"):
            self.assertRejected(lambda r, t: t["trace-single-no-state.jsonl"].append({"event": name}),
                                "State-withheld control executed")

    def test_headers_withheld_control(self):
        message = "Header-withheld control did not fail on the unknown parent"
        self.assertRejected(lambda r, t: r["single_no_headers"].update(stage="open"), message)
        self.assertRejected(lambda r, t: r["single_no_headers"].update(error="<nil>"), message)
        self.assertRejected(lambda r, t: r["single_no_headers"].update(head_number=2), message)
        self.assertRejected(lambda r, t: r["single_no_headers"].pop("head_number"), message)
        self.assertRejected(lambda r, t: t["trace-single-no-headers.jsonl"][0].update(error="boom"), message)
        self.assertRejected(lambda r, t: r["single_no_headers"]["seed"].update(headers=1), message)
        self.assertRejected(lambda r, t: r["single_no_headers"]["seed"].update(state_nodes=4), message)
        self.assertRejected(lambda r, t: t["trace-single-no-headers.jsonl"].pop(1), message)
        self.assertRejected(lambda r, t: t["trace-single-no-headers.jsonl"][1].update(error="boom"), message)
        for name in ("execute", "handoff"):
            self.assertRejected(lambda r, t: t["trace-single-no-headers.jsonl"].append({"event": name}),
                                "Header-withheld control executed")

    def test_malformed_report_is_rejected(self):
        self.assertRejected(lambda r, t: r.pop("resume"), "Malformed probe report")
        self.assertRejected(lambda r, t: t.pop("trace-replay.jsonl"), "Malformed probe report")


class FakeRunner:
    def __init__(self, path, cleanup=True):
        (path / "logs").mkdir(parents=True)
        self.path, self.commands, self.cleanup_complete = path, [], cleanup


class Mutants(unittest.TestCase):
    ROW = {"name": "m", "file": "bsc_inputs.go", "original": "a", "replacement": "b"}

    def run_one(self, build, probe, cleanup=True, row=None):
        output = Path(self.enterContext(tempfile.TemporaryDirectory())).resolve()
        (output / "owned").mkdir()
        owned = FakeRunner(output / "owned")
        rows = []

        def make_runner(path):
            runner = FakeRunner(path, cleanup)
            runner.commands.append({"request": str(path)})
            return runner
        try:
            driver.run_mutant(0, row or self.ROW, output, owned, rows, make_runner, build, probe)
        except InterruptedError:
            pass
        return rows, owned

    def test_build_failure(self):
        def build(runner):
            raise Rejected("Native command failed; inspect 001.result.json")
        rows, owned = self.run_one(build, None)
        self.assertEqual([row["outcome"] for row in rows], ["BUILD_FAILED"])
        self.assertEqual(len(owned.commands), 1)

    def test_survivor(self):
        rows, _ = self.run_one(lambda runner: None, lambda runner, directory: None)
        self.assertEqual(rows[0]["outcome"], "SURVIVED")
        self.assertEqual(len(rows[0]["commands"]), 2)

    def test_kill_named_by_a_go_test(self):
        def probe(runner, directory):
            (directory / "logs" / "000.stdout").write_text("--- FAIL: TestX (1s)\n")
            raise Rejected("Native command failed; inspect 000.result.json")
        rows, _ = self.run_one(lambda runner: None, probe)
        self.assertEqual((rows[0]["outcome"], rows[0]["killed_by"]), ("KILLED", ["TestX"]))

    def test_kill_named_by_a_driver_check(self):
        def probe(runner, directory):
            raise Rejected("Resume did not import block 2")
        rows, _ = self.run_one(lambda runner: None, probe)
        self.assertEqual((rows[0]["outcome"], rows[0]["killed_by"]), ("KILLED", ["Resume did not import block 2"]))

    def test_failure_without_a_named_test(self):
        def probe(runner, directory):
            (directory / "logs" / "000.stdout").write_text("panic: test timed out\n")
            raise Rejected("Native command failed; inspect 000.result.json")
        rows, _ = self.run_one(lambda runner: None, probe)
        self.assertEqual(rows[0]["outcome"], "NO_EVIDENCE")

    def test_stop_signal_is_recorded_and_cleanup_propagates(self):
        def probe(runner, directory):
            raise InterruptedError("Interrupted by SIGTERM")
        with self.assertRaises(InterruptedError):
            output = Path(self.enterContext(tempfile.TemporaryDirectory())).resolve()
            (output / "owned").mkdir()
            owned, rows = FakeRunner(output / "owned"), []
            driver.run_mutant(0, self.ROW, output, owned, rows, lambda path: FakeRunner(path, False),
                              lambda runner: None, probe)
        self.assertEqual(rows[0]["outcome"], "INCOMPLETE")
        self.assertIs(rows[0]["cleanup_complete"], False)
        self.assertIs(owned.cleanup_complete, False)

    def test_error_in_build_is_recorded(self):
        def build(runner):
            raise OSError("disk full")
        output = Path(self.enterContext(tempfile.TemporaryDirectory())).resolve()
        (output / "owned").mkdir()
        owned, rows = FakeRunner(output / "owned"), []
        with self.assertRaises(OSError):
            driver.run_mutant(0, self.ROW, output, owned, rows, lambda path: FakeRunner(path), build, None)
        self.assertEqual(rows[0]["outcome"], "INCOMPLETE")
        self.assertIs(owned.cleanup_complete, True)

    def test_incomplete_cleanup_of_a_finished_mutant_reaches_the_owned_runner(self):
        rows, owned = self.run_one(lambda runner: None, lambda runner, directory: None, cleanup=False)
        self.assertIs(rows[0]["cleanup_complete"], False)
        self.assertIs(owned.cleanup_complete, False)

    def test_classify(self):
        self.assertEqual(driver.classify(["TestA"], "Native command failed"), ("KILLED", ["TestA"]))
        self.assertEqual(driver.classify([], "Replay fetched or wrote"), ("KILLED", ["Replay fetched or wrote"]))
        self.assertEqual(driver.classify([], "Native command failed; inspect x"),
                         ("NO_EVIDENCE", ["Native command failed; inspect x"]))
        self.assertEqual(driver.classify([], ""), ("NO_EVIDENCE", [""]))

    def test_tally(self):
        def row(name, outcome, equivalent=None):
            return {"name": name, "outcome": outcome, "equivalent": equivalent}
        rows = [row("k", "KILLED"), row("e", "SURVIVED", "same behavior"), row("s", "SURVIVED"),
                row("b", "BUILD_FAILED"), row("n", "NO_EVIDENCE"), row("i", "INCOMPLETE"), row("x", "OTHER")]
        bad, counts = driver.tally(rows)
        self.assertEqual(bad, ["s", "b", "n", "i", "x"])
        self.assertEqual(counts, {"KILLED": 1, "SURVIVED": 2, "BUILD_FAILED": 1, "NO_EVIDENCE": 1, "INCOMPLETE": 1})
        self.assertEqual(driver.tally([row("k", "KILLED"), row("e", "SURVIVED", "reason")])[0], [])


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
        one, two = (dict(driver.expected_items(index, RAW, GENESIS_HEADER, GENESIS, "abc")) for index in range(2))
        self.assertEqual(list(dict(driver.expected_items(0, RAW, GENESIS_HEADER, GENESIS, "abc"))), list(driver.ITEM_NAMES))
        self.assertEqual(one["block-rlp"], RAW[0])
        self.assertEqual(one["parent-header-rlp"], GENESIS_HEADER)
        self.assertEqual(one["parent-hash"].hex(), GENESIS_HASH[2:])
        self.assertEqual(one["parent-root"], b"\x33" * 32)
        self.assertEqual(one["ancestor-headers-rlp"], b"\xc0")
        self.assertEqual(two["parent-header-rlp"], HEADER_1)
        self.assertEqual(two["parent-root"], bytes.fromhex("44" * 32))
        self.assertEqual(two["ancestor-headers-rlp"], enc_list([GENESIS_HEADER]))
        key = (b"parlia-" + bytes.fromhex(GENESIS_HASH[2:])).hex()
        self.assertEqual(two["parlia-snapshot-keys"], ('{"checkpoint":0,"keys":["%s"]}' % key).encode())
        self.assertEqual(one["parlia-snapshot-keys"], two["parlia-snapshot-keys"])
        self.assertEqual(one["native-commit"], b"abc")
        self.assertEqual(one["normalization-profile"], b"test-only-probe-v1")
        self.assertEqual(one["state-profile"], b"state.scheme=hash;gcmode=full;snapshots=off")
        # Homestead at block 0 is the genesis rule set and is skipped. Bruno at block 1 has passed for
        # block 1, and Euler at block 2 is next. Block 2 also passes Euler. Kepler at time 0 is not after
        # the genesis time and is skipped; Shanghai at time 20 has passed at block 2's time 22.
        start = zlib.crc32(bytes.fromhex(GENESIS_HASH[2:]))
        bruno = zlib.crc32((1).to_bytes(8, "big"), start)
        self.assertEqual(one["fork-id"], ('{"hash":"%08x","next":"2"}' % bruno).encode())
        euler = zlib.crc32((2).to_bytes(8, "big"), bruno)
        shanghai = zlib.crc32((20).to_bytes(8, "big"), euler)
        self.assertEqual(two["fork-id"], ('{"hash":"%08x","next":"0"}' % shanghai).encode())

    def test_keccak_vectors(self):
        for data, digest in ((b"", "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"),
                             (b"abc", "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"),
                             (b"\xc0", "1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
                             (b"\x80", "56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")):
            self.assertEqual(driver.keccak256(data).hex(), digest)

    def test_fork_id_mainnet_vectors(self):
        # EIP-2124 vectors for the Ethereum mainnet rules up to London.
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
            self.assertEqual(json.loads(driver.fork_id(genesis, mainnet, head, 0)), {"hash": digest, "next": following})

    def test_ancestor_floor_matches_the_go_table(self):
        for number, floor in {2: 0, 3: 0, 256: 0, 300: 0, 1024: 0, 1025: 769, 1100: 844, 1500: 1024, 2048: 1024,
                              2049: 1793}.items():
            self.assertEqual(driver.ancestor_floor(number), floor)
        go = re.sub(r"\s+", " ", (Path(__file__).with_name("bsc_inputs_test.go")).read_text())
        self.assertIn("2: 0, 3: 0, 256: 0, 300: 0, 1024: 0, 1025: 769, 1100: 844, 1500: 1024, 2048: 1024, 2049: 1793}", go)

    def test_rlp_helpers(self):
        long_item = b"\xb8\x38" + b"\x07" * 0x38
        self.assertEqual(driver.rlp_list(enc_list([b"\x01", b"\x82ab", long_item])), [b"\x01", b"\x82ab", long_item])
        self.assertEqual(driver.rlp_bytes(long_item), b"\x07" * 0x38)
        self.assertEqual(driver.rlp_bytes(b"\x05"), b"\x05")
        self.assertEqual(driver.rlp_encode_list([long_item]), enc_list([long_item]))
        self.assertEqual(driver.rlp_encode_list([]), b"\xc0")
        self.assertEqual(driver.header_number(HEADER_2), 2)
        for bad, function in ((b"\x80", driver.rlp_list), (b"\xc0\x00", driver.rlp_list), (b"\xc0", driver.rlp_bytes),
                              (b"\x82a", driver.rlp_bytes), (b"\xb8", driver.rlp_bytes), (b"", driver.rlp_bytes),
                              (b"\xc2\x82", driver.rlp_list), (b"\xc2\x82a", driver.rlp_list),
                              (b"\xc3\xb9\x01\x00", driver.rlp_list)):
            with self.assertRaises(Rejected):
                function(bad)

    def test_go_constants_match_the_driver(self):
        go = (Path(__file__).with_name("bsc_inputs.go")).read_text()
        self.assertIn(f'StateProfile = "{driver.STATE_PROFILE.decode()}"', go)
        self.assertIn(f'ParliaGetTag = "{driver.GET_TAG[:-1].decode()}"', go)
        self.assertIn(f'ParliaHasTag = "{driver.HAS_TAG[:-1].decode()}"', go)
        self.assertIn(f'NormalizationProfile = "{driver.NORMALIZATION_PROFILE.decode()}"', go)
        self.assertIn(f"CheckpointInterval = {driver.CHECKPOINT_INTERVAL}\n", go)
        self.assertIn(f"BlockHashWindow = {driver.BLOCKHASH_WINDOW}\n", go)
        self.assertIn(f'SnapshotPrefix = "{driver.SNAPSHOT_PREFIX.decode()}"', go)
        self.assertIn(f'ErrParentState = errors.New("{driver.PARENT_STATE_ERROR}")', go)
        names = go[go.index("var StaticItemNames"):go.index("}", go.index("var StaticItemNames"))]
        self.assertEqual(re.findall(r'"([a-z-]+)"', names), list(driver.ITEM_NAMES))

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
