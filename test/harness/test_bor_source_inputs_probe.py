"""Portable checks of the E01-02.inputs Bor probe judge: no Docker or native client."""

import contextlib
import copy
import io
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import bor_source_inputs_probe as probe
import capture_probe
from artifacts import Rejected, reference
import source_inputs

FIXTURE = Path(__file__).with_name("fixtures") / "source_inputs_bor"
COMMIT = "3a018e38467594508ca31ebeaf804837fb92834a"
BASE_TIME = 1794317135


TIMES = [BASE_TIME, BASE_TIME + 1, BASE_TIME + 2, BASE_TIME + 8, BASE_TIME + 9, BASE_TIME + 10, BASE_TIME + 11,
         BASE_TIME + 17, BASE_TIME + 18]


def rlp(value):
    """A minimal RLP encoder for test blocks, written separately from the decoder under test."""
    if isinstance(value, int):
        value = value.to_bytes((value.bit_length() + 7) // 8, "big")
    if isinstance(value, list):
        payload, short, long = b"".join(rlp(item) for item in value), 0xc0, 0xf7
    elif len(value) == 1 and value[0] < 0x80:
        return value
    else:
        payload, short, long = value, 0x80, 0xb7
    if len(payload) < 56:
        return bytes([short + len(payload)]) + payload
    size = len(payload).to_bytes((len(len(payload).to_bytes(8, "big").lstrip(b"\0"))), "big")
    return bytes([long + len(size)]) + size + payload


def sealed(times=TIMES):
    """blocks.rlp bytes and the matching generation record for blocks 1 to len(times)."""
    blocks, rows = b"", []
    for number, moment in enumerate(times, 1):
        root = bytes([number]) * 32
        header = [b"\x11" * 32, b"\x22" * 32, b"\x33" * 20, root, b"\x44" * 32, b"\x55" * 32, b"\0" * 256, 1,
                  number, 20000000, 0, moment, b"\x99" * 600, b"\0" * 32, b"\0" * 8]
        blocks += rlp([header, [], []])
        rows.append({"number": number, "hash": f"0x{number:064x}", "state_root": "0x" + root.hex(),
                     "timestamp": moment})
    return blocks, {"blocks": rows}


def context():
    oracle = probe.load(FIXTURE / "expected.json")
    bodies = {path.name: path.read_bytes() for path in (FIXTURE / "stub").iterdir()}
    blocks, generated = sealed()
    return probe.build_context(oracle, generated, bodies, b"genesis-bytes", blocks, COMMIT)


def call_rows(ctx, origins, requests=None):
    requests = requests or ctx["requests"]
    rows = []
    for index, origin in enumerate(origins):
        request = requests[index]["request"]
        rows.append({"sequence": index, "request": request,
                     "request_sha256": source_inputs.sha(request.encode()).hex(),
                     "response_sha256": ctx["expected"]["entries"][index]["response_sha256"],
                     "response_length": 1, "origin": origin})
    return rows


def dump(observation, count, closed=False, marker=None):
    prefix = probe.LOG_PREFIX + observation
    rows = [{"key": prefix + "/capsule", "value_sha256": "c" * 64, "value_length": 1}]
    for sequence in range(count):
        rows.append({"key": f"{prefix}/log/{sequence:016x}", "value_sha256": f"{sequence:064x}", "value_length": 1})
        rows.append({"key": f"{prefix}/log/{sequence:016x}/0000000000000000", "value_sha256": f"{sequence + 50:064x}",
                     "value_length": 1})
    if count:
        rows.append({"key": prefix + "/frontier", "value_sha256": f"{count + 90:064x}", "value_length": 1})
    if closed:
        rows.append({"key": prefix + "/closed", "value_sha256": "d" * 64, "value_length": 1})
    if marker:
        rows.append({"key": probe.MARKER_PREFIX + observation, "value_sha256": "e" * 64, "value_length": 1})
    return rows


def result(ctx, plan, step, rows, complete=True, frontier=0, consumed=True, marker=True):
    last = ctx["generated"]["blocks"][-1]
    value = {"observation": plan["observation"], "frontier_at_open": frontier, "counted_calls": rows,
             "inserted": len(ctx["generated"]["blocks"]) if complete else 3,
             "head_hash": last["hash"] if complete else "0x0", "head_root": last["state_root"] if complete else "0x0",
             "prefix_conflict": False, "crashed": False, "crash_after": step["crash_after"]}
    if not complete:
        value["import_error"] = "invalid merkle root"
    if complete and consumed and step["log"]:
        value["consumed"] = {"observation": plan["observation"], "capsule_digest": ctx["expected"]["capsule"],
                             "count": len(ctx["requests"]), "chain": "f" * 64, "digest": ctx["expected"]["consumed"]}
        if marker:
            value["stand_in_marker"] = {"kind": probe.MARKER_KIND, "observation": plan["observation"],
                                        "consumed_manifest_digest": ctx["expected"]["consumed"]}
    return value


def attempt(status, before, after, value=None, crash=None):
    return {"argv": [], "status": dict(status), "store_before": before, "store_after": after, "result": value,
            "crash_result": crash}


OK = {"exit_code": 0, "signal": ""}
KILLED = {"exit_code": -1, "signal": "killed"}


def passing(ctx, name):
    """A scenario observation that matches the oracle."""
    plan = ctx["plans"][name]
    obs, steps, total = plan["observation"], plan["attempts"], len(ctx["requests"])
    ran = {"name": name, "observation": obs, "attempts": steps}
    kind = plan["kind"]
    if kind in ("replay-after-loss", "tail-append"):
        point = steps[0]["crash_after"]
        crash = {"counted_calls": call_rows(ctx, ["fetch"] * (point + 1)), "crashed": True}
        retry = call_rows(ctx, ["replay"] * (point + 1) + ["fetch"] * (total - point - 1))
        attempts = [attempt(KILLED, [], dump(obs, point + 1), None, crash),
                    attempt(OK, dump(obs, point + 1), dump(obs, total, True, marker=True),
                            result(ctx, plan, steps[1], retry, frontier=point + 1))]
    elif kind == "prefix-conflict":
        point, offset = steps[0]["crash_after"], plan["conflict_offset"]
        delayed = probe.expected_requests(ctx["oracle"], ctx["timestamps"], steps[1]["state_sync_delay"])
        rows = call_rows(ctx, ["replay"] * offset)
        rows.append({"sequence": offset, "request": delayed[offset]["request"], "request_sha256": "0" * 64,
                     "response_length": 0, "origin": "replay",
                     "error": "dependency log prefix conflict: offset holds another request"})
        conflict = result(ctx, plan, steps[1], rows, complete=False, frontier=point + 1)
        conflict["prefix_conflict"] = True
        saved = dump(obs, point + 1)
        attempts = [attempt(KILLED, [], saved, None, {"counted_calls": call_rows(ctx, ["fetch"] * (point + 1))}),
                    attempt(OK, saved, copy.deepcopy(saved), conflict),
                    attempt(OK, saved, dump(obs, total, True, marker=True),
                            result(ctx, plan, steps[2], call_rows(
                                ctx, ["replay"] * (point + 1) + ["fetch"] * (total - point - 1)), frontier=point + 1))]
    elif kind == "control-changed-response":
        value = result(ctx, plan, steps[0], call_rows(ctx, ["fetch"] * 3), complete=False)
        attempts = [attempt(OK, [], [], value)]
    else:
        delayed = probe.expected_requests(ctx["oracle"], ctx["timestamps"], steps[0]["state_sync_delay"])
        attempts = [attempt(OK, [], [], result(ctx, plan, steps[0], call_rows(ctx, ["fetch"] * total, delayed)))]
    return {"plan": ran, "attempts": attempts}


class OracleTests(unittest.TestCase):
    def test_expected_requests_follow_the_block_times(self):
        ctx = context()
        requests = [row["request"] for row in ctx["requests"]]
        self.assertEqual(requests[:2], ["GET /bor/spans/0", "GET /bor/spans/1"])
        self.assertEqual(requests[2], "GET /clerk/time?from_id=1&to_time=2026-11-10T13:25:43Z&pagination.limit=50")
        self.assertEqual(requests[4], "GET /clerk/time?from_id=3&to_time=2026-11-10T13:25:52Z&pagination.limit=50")
        delayed = probe.expected_requests(ctx["oracle"], ctx["timestamps"], 1)
        self.assertEqual(delayed[2]["request"],
                         "GET /clerk/time?from_id=1&to_time=2026-11-10T13:25:42Z&pagination.limit=50")
        self.assertEqual(delayed[:2], ctx["requests"][:2])

    def test_expected_digests_use_the_readme_formulas(self):
        ctx = context()
        items = (("genesis", b"genesis-bytes"), ("blocks", sealed()[0]), ("bor-source-commit", COMMIT.encode()))
        capsule = source_inputs.capsule_digest(items)
        chain = source_inputs.chain_start(capsule)
        for sequence, row in enumerate(ctx["requests"]):
            body = (FIXTURE / "stub" / row["body"]).read_bytes()
            chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, row["request"].encode(), body))
        self.assertEqual(ctx["expected"]["capsule"], capsule.hex())
        self.assertEqual(ctx["expected"]["consumed"], source_inputs.consumed_digest(capsule, 5, chain).hex())
        self.assertNotEqual(ctx["body_sha256"]["clerk-a-from-1.json"], ctx["body_sha256"]["clerk-b-from-1.json"])

    def test_every_scenario_passes_when_it_matches_the_oracle(self):
        ctx = context()
        self.assertEqual(sorted(ctx["plans"]), ["ac-cap-04-01", "ac-cap-04-02", "ac-cap-04-03",
                                                 "control-changed-response", "control-delay"])
        for name in ctx["plans"]:
            self.assertEqual(probe.checked_compare(name, passing(ctx, name), ctx), [], name)


class CompareControls(unittest.TestCase):
    """Each control breaks one property and expects the judge to refuse it."""

    def setUp(self):
        self.ctx = context()

    def refused(self, name, change, text):
        observed = passing(self.ctx, name)
        change(observed)
        failures = probe.checked_compare(name, observed, self.ctx)
        self.assertTrue(any(text in failure for failure in failures), failures)

    def test_plan_and_exit_status(self):
        self.refused("ac-cap-04-01", lambda o: o["plan"].update(observation="x"), "did not run the oracle plan")
        self.refused("ac-cap-04-01", lambda o: o["attempts"].pop(), "did not run the oracle plan")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][0]["status"].update(signal=""), "not lost at its crash")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][0].update(crash_result=None), "not lost at its crash")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][1]["status"].update(exit_code=1), "exited with")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][1]["status"].update(signal="killed"), "exited with")
        self.refused("control-delay", lambda o: o["attempts"][0].update(result=None), "left no result")

    def test_replay_after_loss(self):
        name = "ac-cap-04-01"
        calls = lambda o: o["attempts"][1]["result"]["counted_calls"]
        self.refused(name, lambda o: o["attempts"][0]["crash_result"]["counted_calls"].pop(), "lost attempt: 2 counted")
        self.refused(name, lambda o: o["attempts"][0].update(store_after=[
            row for row in o["attempts"][0]["store_after"] if not row["key"].endswith("/log/0000000000000002")]),
            "exactly the saved prefix")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(frontier_at_open=2), "durable frontier")
        self.refused(name, lambda o: calls(o)[2].update(origin="fetch"), "origin fetch, expected replay")
        self.refused(name, lambda o: calls(o)[2].update(response_sha256=self.ctx["body_sha256"]["clerk-b-from-1.json"]),
                     "differ from the saved response")
        self.refused(name, lambda o: calls(o)[3].update(request="GET /clerk/time?from_id=4"), "expected sequence")
        self.refused(name, lambda o: calls(o)[3].update(request_sha256="1" * 64), "request digest differs")
        self.refused(name, lambda o: calls(o)[4].update(error="boom"), "failed: boom")
        self.refused(name, lambda o: calls(o)[1].update(sequence=7), "expected sequence")
        self.refused(name, lambda o: calls(o).pop(), "4 counted calls, expected 5")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(head_hash="0x1"), "differs from the sealed")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(head_root="0x1"), "differs from the sealed")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(import_error="x"), "did not complete")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(inserted=8), "did not complete")
        self.refused(name, lambda o: o["attempts"][1]["result"]["consumed"].update(digest="0" * 64),
                     "consumed-manifest digest differs")

    def test_changed_response_never_reaches_bor(self):
        changed = self.ctx["body_sha256"]["clerk-b-from-1.json"]
        # Make the changed body the expected one at a fetched offset, so only the changed-body rule can refuse it.
        self.ctx["expected"]["entries"][4]["response_sha256"] = changed
        observed = passing(self.ctx, "ac-cap-04-01")
        failures = probe.checked_compare("ac-cap-04-01", observed, self.ctx)
        self.assertTrue(any("changed external response reached Bor" in f for f in failures), failures)

    def test_prefix_conflict(self):
        name = "ac-cap-04-02"
        conflict = lambda o: o["attempts"][1]["result"]
        self.refused(name, lambda o: conflict(o)["counted_calls"][2].update(error="other error"), "prefix conflict")
        self.refused(name, lambda o: conflict(o)["counted_calls"][2].update(request=self.ctx["requests"][2]["request"]),
                     "prefix conflict")
        self.refused(name, lambda o: conflict(o)["counted_calls"].append(conflict(o)["counted_calls"][1]),
                     "prefix conflict")
        self.refused(name, lambda o: conflict(o)["counted_calls"][0].update(origin="fetch"), "conflicting retry")
        self.refused(name, lambda o: conflict(o).update(prefix_conflict=False), "did not fail with the prefix")
        self.refused(name, lambda o: conflict(o).pop("import_error"), "did not fail with the prefix")
        self.refused(name, lambda o: conflict(o).update(consumed={"digest": "x"}), "closed the log")
        self.refused(name, lambda o: conflict(o).update(stand_in_marker={"kind": "x"}), "closed the log")
        self.refused(name, lambda o: o["attempts"][1]["store_after"].pop(), "changed the store")
        self.refused(name, lambda o: o["attempts"][1]["store_after"][1].update(value_sha256="9" * 64),
                     "changed the store")
        self.refused(name, lambda o: o["attempts"][1].update(store_before=[]), "changed the store")
        # Before and after agree with each other but not with the store the lost attempt left.
        self.refused(name, lambda o: o["attempts"][1].update(store_before=[], store_after=[]), "changed the store")
        self.refused(name, lambda o: o["attempts"][2]["result"].update(frontier_at_open=0), "keep the saved frontier")
        self.refused(name, lambda o: o["attempts"][2]["result"]["counted_calls"][0].update(origin="fetch"),
                     "reopened retry")
        self.refused(name, lambda o: o["attempts"][2]["result"].update(head_hash="0x1"), "reopened retry")

    def test_conflict_offset_must_be_a_real_conflict(self):
        self.ctx["plans"]["ac-cap-04-02"]["conflict_offset"] = 1
        observed = passing(self.ctx, "ac-cap-04-02")
        observed["plan"]["attempts"] = self.ctx["plans"]["ac-cap-04-02"]["attempts"]
        failures = probe.checked_compare("ac-cap-04-02", observed, self.ctx)
        self.assertTrue(any("conflict offset" in f for f in failures), failures)
        self.ctx["plans"]["ac-cap-04-02"]["conflict_offset"] = 3
        failures = probe.checked_compare("ac-cap-04-02", passing(self.ctx, "ac-cap-04-02"), self.ctx)
        self.assertTrue(any("conflict offset" in f for f in failures), failures)

    def test_tail_append(self):
        name = "ac-cap-04-03"
        retry = lambda o: o["attempts"][1]["result"]
        self.refused(name, lambda o: o["attempts"][0]["store_after"].append(
            {"key": probe.LOG_PREFIX + "ac-cap-04-03/log/0000000000000004", "value_sha256": "0", "value_length": 1}),
            "exactly the saved prefix")
        self.refused(name, lambda o: retry(o).update(frontier_at_open=3), "durable frontier")
        self.refused(name, lambda o: retry(o)["counted_calls"][1].update(origin="fetch"), "origin fetch")
        self.refused(name, lambda o: retry(o)["counted_calls"][2].update(origin="replay"), "origin replay")
        self.refused(name, lambda o: o["attempts"][1]["store_after"][1].update(value_sha256="9" * 64),
                     "saved prefix key")
        self.refused(name, lambda o: o["attempts"][1]["store_after"].pop(1), "saved prefix key")
        self.refused(name, lambda o: o["attempts"][1].update(store_after=dump("ac-cap-04-03", 4, True, True)),
                     "tail was not appended")
        self.refused(name, lambda o: retry(o).update(head_root="0x1"), "differs from the sealed")
        self.refused(name, lambda o: retry(o)["consumed"].update(digest="0" * 64), "final consumed manifest")
        self.refused(name, lambda o: retry(o)["consumed"].update(count=4), "final consumed manifest")
        self.refused(name, lambda o: retry(o)["consumed"].update(capsule_digest="0" * 64), "final consumed manifest")
        self.refused(name, lambda o: retry(o).pop("consumed"), "final consumed manifest")
        self.refused(name, lambda o: retry(o)["stand_in_marker"].update(consumed_manifest_digest="0" * 64),
                     "does not bind")
        self.refused(name, lambda o: retry(o)["stand_in_marker"].update(kind="exec-ready"), "does not bind")
        self.refused(name, lambda o: retry(o)["stand_in_marker"].update(observation="x"), "does not bind")
        self.refused(name, lambda o: o["attempts"][1]["store_after"].pop(), "marker is not in the store")

    def test_frontier_key_may_change_in_the_tail(self):
        observed = passing(self.ctx, "ac-cap-04-03")
        self.assertEqual(probe.checked_compare("ac-cap-04-03", observed, self.ctx), [])
        frontier = [row for row in observed["attempts"][0]["store_after"] if row["key"].endswith("/frontier")]
        after = [row for row in observed["attempts"][1]["store_after"] if row["key"].endswith("/frontier")]
        self.assertNotEqual(frontier[0]["value_sha256"], after[0]["value_sha256"])

    def test_controls(self):
        self.refused("control-changed-response", lambda o: o["attempts"][0]["result"].pop("import_error"),
                     "did not make native import fail")
        self.refused("control-changed-response", lambda o: o["attempts"][0]["result"].update(inserted=9),
                     "did not make native import fail")
        self.refused("control-delay", lambda o: o["attempts"][0]["result"]["counted_calls"][2].update(
            request=self.ctx["requests"][2]["request"]), "delayed request sequence")
        self.refused("control-delay", lambda o: o["attempts"][0]["result"].update(head_hash="0x1"),
                     "differs from the sealed")

    def test_unknown_kind_and_malformed_observation(self):
        self.ctx["plans"]["control-delay"]["kind"] = "other"
        self.assertTrue(any("unknown scenario kind" in f
                            for f in probe.checked_compare("control-delay", passing(context(), "control-delay"), self.ctx)))
        failures = probe.checked_compare("ac-cap-04-01", {"plan": None, "attempts": [None, None]}, context())
        self.assertTrue(failures)
        observed = passing(self.ctx, "ac-cap-04-01")
        observed["attempts"][1]["result"]["counted_calls"] = None
        self.assertTrue(any("malformed observation" in f for f in probe.checked_compare("ac-cap-04-01", observed,
                                                                                         context())))

    def test_to_time_format(self):
        self.assertEqual(probe.to_time(1794317143, 0), "2026-11-10T13:25:43Z")
        self.assertEqual(probe.to_time(1794317143, 1), "2026-11-10T13:25:42Z")
        self.assertEqual(probe.to_time(0, 0), "1970-01-01T00:00:00Z")

    def test_log_key_helpers(self):
        rows = dump("obs", 2, True, marker=True) + dump("other", 3)
        keys = probe.log_keys(rows, "obs")
        self.assertEqual(probe.entry_sequences(keys, "obs"), [0, 1])
        self.assertTrue(all(key.startswith(probe.LOG_PREFIX + "obs/") for key in keys))
        self.assertNotIn(probe.LOG_PREFIX + "ob/log/0000000000000000", probe.log_keys(dump("ob", 1), "obs"))


class SealedHeaderTests(unittest.TestCase):
    def test_decodes_number_time_and_root(self):
        blocks, generated = sealed()
        self.assertEqual(probe.sealed_headers(blocks),
                         [{key: row[key] for key in ("number", "timestamp", "state_root")} for row in generated["blocks"]])

    def test_short_and_long_items(self):
        self.assertEqual(probe.rlp_item(b"\x05", 0), (b"\x05", 1))
        self.assertEqual(probe.rlp_item(rlp(b"abc"), 0), (b"abc", 4))
        self.assertEqual(probe.rlp_item(rlp(b"x" * 300), 0)[0], b"x" * 300)
        self.assertEqual(probe.rlp_item(rlp([b"a", [b"b"]]), 0)[0], [b"a", [b"b"]])
        self.assertEqual(probe.rlp_item(rlp([b"y" * 60]), 0)[0], [b"y" * 60])

    def test_malformed_input_is_refused(self):
        for data in (b"", b"\x83ab", b"\xb9\x01", b"\xc3\x01", rlp([b"a"])[:-1], b"\xc2\x82a", rlp([[1, 2], [], []]),
                     rlp(b"not a list")):
            with self.subTest(data=data), self.assertRaises(Rejected):
                probe.sealed_headers(data)
        # Direct item controls: an empty input, a string shorter than its length prefix,
        # and a list whose last item runs past the list's own end.
        for data in (b"", b"\x83ab", b"\xc2\x83abc"):
            with self.subTest(item=data), self.assertRaises(Rejected):
                probe.rlp_item(data, 0)

    def test_generation_record_must_match_the_blocks(self):
        oracle = probe.load(FIXTURE / "expected.json")
        bodies = {path.name: path.read_bytes() for path in (FIXTURE / "stub").iterdir()}
        blocks, generated = sealed()
        for field, value in (("timestamp", 1), ("state_root", "0x" + "0" * 64), ("number", 99)):
            changed = copy.deepcopy(generated)
            changed["blocks"][3][field] = value
            with self.subTest(field=field), self.assertRaises(Rejected):
                probe.build_context(oracle, changed, bodies, b"g", blocks, COMMIT)
        with self.assertRaises(Rejected):
            probe.build_context(oracle, {"blocks": generated["blocks"][:-1]}, bodies, b"g", blocks, COMMIT)


class FakeNative:
    """Stands in for Docker at the Native boundary; writes what each command would write."""
    interrupt = False
    cleanup_complete = True
    unit_stdout = (f"ok  \t{probe.PACKAGE}/sourceinputs\t1.0s\nok  \t{probe.PACKAGE}/heimdallstub\t1.0s\n")
    context = None
    broken = None

    def __init__(self, output, deadline):
        self.output, self.nonce, self.commands = output, "n", []
        self.cleanup_complete = FakeNative.cleanup_complete
        (output / "logs").mkdir()

    def command(self, image, argv, mounts, **kwargs):
        if FakeNative.interrupt:
            os.kill(os.getpid(), signal.SIGTERM)  # the real handler turns it into Interrupted
        self.commands.append(argv)
        targets = {target: source for source, target, _ in mounts}
        stdout = self.output / "logs" / f"{len(self.commands):03d}.stdout"
        if argv[:2] == ["/probe/probe", "generate"]:
            chain = targets["/chains"] / "a"
            chain.mkdir()
            blocks, generated = sealed()
            (chain / "blocks.rlp").write_bytes(blocks)
            (chain / "generated.json").write_text(json.dumps(generated))
        elif argv[:2] == ["/probe/probe", "scenario"]:
            name = argv[argv.index("-out") + 1].removeprefix("/out/")
            directory = targets["/out"] / name
            directory.mkdir()
            observed = passing(FakeNative.context, name)
            if FakeNative.broken == name:
                observed["attempts"][-1]["status"]["exit_code"] = 1
            rows = []
            for index, row in enumerate(observed["attempts"]):
                for key, file in (("result", f"attempt-{index}.json"), ("crash_result", f"attempt-{index}.json.crash.json")):
                    if row[key] is not None:
                        (directory / file).write_text(json.dumps(row[key]))
                rows.append({key: row[key] for key in ("argv", "status", "store_before", "store_after")})
            (directory / "scenario.json").write_text(json.dumps({"plan": observed["plan"], "attempts": rows}))
        elif argv[1] == "build":
            (targets["/work/bin"] / "probe").write_bytes(b"probe")
        elif argv[1] == "test":
            stdout.write_text("=== RUN TestX\n" + FakeNative.unit_stdout)
        stdout.touch()
        return stdout


GOLANG = "docker.io/library/golang@sha256:" + "b" * 64
DEBIAN = "docker.io/library/debian@sha256:" + "a" * 64


class DriverFixture(unittest.TestCase):
    """Temporary roots and fakes for prepare() and run(); it defines no tests of its own."""

    def setUp(self):
        self.root = Path(tempfile.mkdtemp()).resolve()
        self.addCleanup(shutil.rmtree, self.root, True)
        for target, value in ((capture_probe, "LOCK"), (probe, "Native")):
            patcher = mock.patch.object(target, value, self.root / "lock" if value == "LOCK" else FakeNative)
            patcher.start()
            self.addCleanup(patcher.stop)
        docker = mock.patch.object(capture_probe.subprocess, "run", side_effect=self.fake_subprocess)
        docker.start()
        self.addCleanup(docker.stop)
        self.containers = ""
        FakeNative.interrupt, FakeNative.cleanup_complete, FakeNative.broken = False, True, None
        FakeNative.context = context()

    def fake_subprocess(self, argv, **kwargs):
        if argv[0] == "docker":
            return mock.Mock(returncode=0, stdout=self.containers)
        return mock.Mock(stdout="0" * 40 + "\n" if "rev-parse" in argv else "")

    def build(self, compiler=GOLANG, runtime=DEBIAN, family_compiler=None, kind="actual-build", closure_commit="c" * 40):
        root = self.root / "build"
        root.mkdir(exist_ok=True)
        archive = root / "bor.tar"
        if not archive.exists():
            with tarfile.open(archive, "w") as stream:
                for name, data in (("go.mod", b"module github.com/ethereum/go-ethereum\n\ngo 1.25.0\n"),
                                   ("tests/bor/testdata/genesis.json", b"genesis-bytes"),
                                   ("tests/bor/helper.go", b"package bor\n")):
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    stream.addfile(info, io.BytesIO(data))
        ref = reference(archive)
        ref["path"] = archive.name
        module = {}
        for field in ("info", "mod", "zip"):
            (root / f"bor.{field}").write_text(field)
            module[field] = reference(root / f"bor.{field}")
            module[field]["path"] = f"bor.{field}"
        manifest = {"evidence_kind": kind, "task_id": "E01-01.builds",
                    "builds": {"bor": {"source_commit": COMMIT, "compiler_image": family_compiler or compiler}},
                    "module_closures": {"bor": {"source_commit": closure_commit if closure_commit != "c" * 40 else COMMIT,
                                                "source_archive": ref, "artifacts": [module]}},
                    "image_inputs": {"golang-build-image": {"reference": compiler},
                                     "debian-runtime-image": {"reference": runtime}}}
        path = root / f"manifest-{len(list(root.glob('manifest-*')))}.json"
        path.write_text(json.dumps(manifest))
        return path

    def prepare(self, name="prepared", manifest=None):
        manifest = manifest or self.build()
        return probe.prepare(manifest, reference(manifest)["sha256"], self.root / name)

    def prepared(self, change=None):
        output, record = self.prepare("prep-" + str(len(list(self.root.glob("prep-*")))))
        self.assertEqual(record["result"], "PASS", record.get("error"))
        if change:
            record = json.loads((output / "preparation.json").read_text())
            change(record)
            (output / "preparation.json").write_text(json.dumps(record))
        return output / "preparation.json"

    def run_probe(self, preparation, name="run"):
        return probe.run(preparation, reference(preparation)["sha256"], self.root / name)


class PrepareTests(DriverFixture):
    def test_passing_preparation(self):
        output, record = self.prepare()
        self.assertEqual(record["result"], "PASS", record.get("error"))
        source = output / "bor/source" / probe.PROBE_DIR
        for path in ("main.go", "heimdallstub/stub.go", "heimdallstub/stub_test.go", "sourceinputs/inputs.go",
                     "sourceinputs/inputs_test.go"):
            self.assertTrue((source / path).is_file(), path)
        self.assertEqual((source / "sourceinputs/inputs.go").read_bytes(),
                         (probe.ROOT / "adapters/bor/source_inputs/inputs.go").read_bytes())
        self.assertEqual((output / "bor/bin/genesis.json").read_bytes(), b"genesis-bytes")
        self.assertEqual(record["chain"]["blocks"]["sha256"], reference(output / "chains/a/blocks.rlp")["sha256"])
        self.assertEqual(json.loads((output / "preparation.json").read_text())["result"], "PASS")

    def test_failures_still_write_the_record(self):
        cases = {"compiler": dict(compiler="golang:latest"), "runtime": dict(runtime="debian:latest"),
                 "family": dict(family_compiler="docker.io/library/golang@sha256:" + "d" * 64),
                 "closure": dict(closure_commit="e" * 40)}
        messages = {"compiler": "Unpinned compiler", "runtime": "Unpinned runtime image", "family": "compiler mismatch",
                    "closure": "Source closure mismatch"}
        for name, arguments in cases.items():
            with self.subTest(name):
                output, record = self.prepare(name, self.build(**arguments))
                self.assertEqual(record["result"], "FAIL")
                self.assertIn(messages[name], record["error"])
                self.assertTrue((output / "preparation.json").exists())
        self.addCleanup(setattr, FakeNative, "unit_stdout", FakeNative.unit_stdout)
        for package in ("sourceinputs", "heimdallstub"):
            with self.subTest(package):
                FakeNative.unit_stdout = f"ok  \t{probe.PACKAGE}/{package}\t1.0s\n"
                _, record = self.prepare("unit-" + package)
                self.assertIn("tests did not pass", record["error"])
        FakeNative.unit_stdout = (f"ok  \t{probe.PACKAGE}/sourceinputs\t1.0s\nok  \t{probe.PACKAGE}/heimdallstub\t1.0s\n")
        FakeNative.cleanup_complete = False
        _, record = self.prepare("cleanup")
        self.assertEqual(record["result"], "FAIL")
        FakeNative.cleanup_complete = True
        FakeNative.interrupt = True
        output, record = self.prepare("signal")
        self.assertIn("Interrupted", json.loads((output / "preparation.json").read_text())["error"])

    def test_refusals_create_no_output(self):
        manifest = self.build()
        with self.assertRaises(Rejected):
            probe.prepare(manifest, "0" * 64, self.root / "digest")
        relative = Path(os.path.relpath(manifest))
        self.assertFalse(relative.is_absolute())
        with self.assertRaisesRegex(Rejected, "BuildManifest path must be absolute"):
            probe.prepare(relative, reference(manifest)["sha256"], self.root / "relative")
        not_built = self.build(kind="plan")
        with self.assertRaises(Rejected):
            probe.prepare(not_built, reference(not_built)["sha256"], self.root / "plan")
        for output in (manifest.parent / "inside", probe.ROOT / "test/harness/prep-out"):
            with self.subTest(output=output), self.assertRaises(Rejected):
                probe.prepare(manifest, reference(manifest)["sha256"], output)
            self.assertFalse(output.exists())
        for name in ("digest", "plan", "relative"):
            self.assertFalse((self.root / name).exists())

    def test_changed_module_artifact_fails_the_preparation(self):
        manifest = self.build()
        (self.root / "build/bor.zip").write_text("changed")
        output, record = self.prepare("changed-module", manifest)
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("changed", record["error"])
        self.assertNotIn("module_artifacts_verified", json.loads((output / "preparation.json").read_text()).get("bor", {}))


class RunTests(DriverFixture):
    def test_passing_run(self):
        before = {number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}
        output, record = self.run_probe(self.prepared())
        self.assertEqual(record["result"], "PASS", record.get("error"))
        self.assertEqual(sorted(record["scenarios"]), sorted(FakeNative.context["plans"]))
        self.assertEqual(record["expected"]["consumed"], FakeNative.context["expected"]["consumed"])
        self.assertIn("admission_finished", record["phases"])
        self.assertEqual(json.loads((output / "run-record.json").read_text())["result"], "PASS")
        self.assertEqual({number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}, before)

    def test_one_failing_scenario_fails_the_run(self):
        FakeNative.broken = "ac-cap-04-03"
        _, record = self.run_probe(self.prepared())
        self.assertEqual(record["scenarios"]["ac-cap-04-03"]["result"], "FAIL")
        self.assertEqual(record["result"], "FAIL")

    def test_an_oracle_without_scenarios_cannot_pass(self):
        preparation = self.prepared()
        oracle_row = [row for row in json.loads(preparation.read_text())["fixture"]
                      if row["copy"]["path"].endswith("/expected.json")][0]["copy"]
        oracle = json.loads(Path(oracle_row["path"]).read_text())
        oracle["scenarios"] = []
        Path(oracle_row["path"]).write_text(json.dumps(oracle))

        def repin(record):
            for row in record["fixture"]:
                if row["copy"]["path"] == oracle_row["path"]:
                    row["copy"] = reference(Path(oracle_row["path"]))
        prepared = preparation.parent / "repinned.json"
        record = json.loads(preparation.read_text())
        repin(record)
        prepared.write_text(json.dumps(record))
        _, result = self.run_probe(prepared)
        self.assertEqual(result["scenarios"], {})
        self.assertEqual(result["result"], "FAIL")

    def test_signal_and_cleanup_fail_the_run(self):
        preparation = self.prepared()
        FakeNative.interrupt = True
        output, record = self.run_probe(preparation, "signal")
        self.assertIn("signal", json.loads((output / "run-record.json").read_text())["error"])
        FakeNative.interrupt = False
        FakeNative.cleanup_complete = False
        _, record = self.run_probe(preparation, "cleanup")
        self.assertEqual(record["result"], "FAIL")

    def test_preparation_gate(self):
        for name, change in (("kind", lambda r: r.update(kind="other")), ("result", lambda r: r.update(result="FAIL")),
                             ("cleanup", lambda r: r.update(cleanup_complete=False))):
            with self.subTest(name), self.assertRaises(Rejected):
                self.run_probe(self.prepared(change), name)
            self.assertFalse((self.root / name).exists())

    def test_run_inputs_are_checked(self):
        _, record = self.run_probe(self.prepared(lambda r: r.update(runtime_image="debian:latest")), "unpinned")
        self.assertIn("Unpinned runtime image", record["error"])
        _, record = self.run_probe(self.prepared(lambda r: r.update(fixture=[])), "no-oracle")
        self.assertIn("exactly one oracle", record["error"])
        _, record = self.run_probe(self.prepared(lambda r: r.update(fixture=r["fixture"] + r["fixture"][:1])), "two")
        self.assertIn("exactly one oracle", record["error"])
        preparation = self.prepared()
        Path(json.loads(preparation.read_text())["bor"]["binary"]["path"]).write_bytes(b"changed")
        _, record = self.run_probe(preparation, "changed")
        self.assertIn("changed", record["error"])
        preparation = self.prepared()
        Path(json.loads(preparation.read_text())["bor"]["key_source"]["path"]).write_bytes(b"changed key source")
        _, record = self.run_probe(preparation, "changed-key-source")
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("changed", record["error"])

    def test_refusals_create_no_output(self):
        preparation = self.prepared()
        with self.assertRaises(Rejected):
            probe.run(preparation, "0" * 64, self.root / "digest")
        with self.assertRaisesRegex(Rejected, "Preparation path must be absolute"):
            probe.run(Path(os.path.relpath(preparation)), reference(preparation)["sha256"], self.root / "relative")
        self.containers = "abc\n"
        with self.assertRaises(Rejected):
            self.run_probe(preparation, "foreign")
        self.containers = ""
        with open(self.root / "lock", "w") as holder:
            capture_probe.fcntl.flock(holder, capture_probe.fcntl.LOCK_EX)
            with self.assertRaises(Rejected):
                self.run_probe(preparation, "locked")
        for output in (preparation.parent / "inside", probe.ROOT / "test/harness/run-out"):
            with self.subTest(output=output), self.assertRaises(Rejected):
                probe.run(preparation, reference(preparation)["sha256"], output)
            self.assertFalse(output.exists())
        for name in ("digest", "relative", "foreign", "locked"):
            self.assertFalse((self.root / name).exists())

    def test_existing_output_is_never_written(self):
        preparation = self.prepared()
        (self.root / "taken").mkdir()
        (self.root / "taken" / "keep").write_text("x")
        with self.assertRaises(OSError):
            self.run_probe(preparation, "taken")
        self.assertEqual(os.listdir(self.root / "taken"), ["keep"])

    def test_main_exit_codes(self):
        preparation = self.prepared()
        quiet = contextlib.redirect_stdout(io.StringIO())
        quiet.__enter__()
        self.addCleanup(quiet.__exit__, None, None, None)
        self.assertEqual(probe.main(["run", str(preparation), "--sha256", reference(preparation)["sha256"],
                                     "--output", str(self.root / "main-pass")]), 0)
        self.assertEqual(probe.main(["run", str(preparation), "--sha256", "0" * 64, "--output",
                                     str(self.root / "main-refused")]), 2)
        FakeNative.broken = "control-delay"
        self.assertEqual(probe.main(["run", str(preparation), "--sha256", reference(preparation)["sha256"],
                                     "--output", str(self.root / "main-fail")]), 1)


if __name__ == "__main__":
    unittest.main()
