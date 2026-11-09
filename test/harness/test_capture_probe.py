"""Portable checks for the owned capture probe orchestrator. No native runtime claim."""

import copy
import hashlib
import json
import os
from pathlib import Path
import signal
import tempfile
import unittest
from unittest import mock

import capture_probe
from artifacts import Rejected, reference

ORACLE = json.loads((capture_probe.FIXTURE / "expected.json").read_text())


def pattern(size, seed):
    return bytes((i * 131 + seed * 17 + (i >> 8)) & 0xFF for i in range(size))


def observation(family="geth"):
    """A synthetic observation that matches the oracle exactly."""
    head = "0x" + "11" * 32
    scenarios = []
    for name, want in ORACLE["scenarios"].items():
        row = {"name": name, "head_number": 3, "head_hash": head, "notes": want["notes"],
               "log_sha256": list(ORACLE["log_sha256"]), "chunk_bytes": want["chunk_bytes"],
               "buffer_bytes": want.get("buffer_bytes", 2000), "peak_held": 1000,
               "waits": want.get("waits", 2), "callback_wait_rejected": "call_chunks" in want, "errors": [],
               "storage_slot0": {key.upper().replace("0X", "0x"): value for key, value in ORACLE["storage_slot0"].items()}}
        if "call_chunks" in want:
            row["fields"] = [dict(call, chunks=chunks) for call, chunks in zip(ORACLE["call_inputs"], want["call_chunks"])]
        else:
            row["fields"] = [{"tag": 3, "length": 700, "sha256": "ab" * 32, "chunks": 3} for _ in range(3)]
        if want["pause"] is not None:
            row["pause"] = {"observed": True, "kind": want["pause"]["kind"], "head_number": 1,
                            "frames_at_pause": 9, "frames_after_hold": 9, "import_returned_during_hold": False,
                            "callback_open_during_pause": False, "hold_milliseconds": 301}
        scenarios.append(row)
    return {"family": family, "generated_head_hash": head, "baseline_head_hash": head, "scenarios": scenarios}


class OracleTest(unittest.TestCase):
    def test_oracle_matches_written_formulas(self):
        pairs = [(4096, 1, 2), (262144, 3, 4)]
        self.assertEqual([call["sha256"] for call in ORACLE["call_inputs"]],
                         [hashlib.sha256(pattern(size, seed)).hexdigest() for size, seed, _ in pairs])
        self.assertEqual(ORACLE["log_sha256"], [hashlib.sha256(pattern(size, out)).hexdigest() for size, _, out in pairs])
        self.assertEqual(list(ORACLE["storage_slot0"].values()),
                         ["0x" + pattern(size, out)[:32].hex() for size, _, out in pairs])
        self.assertEqual(ORACLE["scenarios"]["vm_safe_backpressure"]["call_chunks"], [1, 64])
        self.assertEqual(ORACLE["scenarios"]["overlap_delayed_worker"]["call_chunks"], [4, 256])


class CompareTest(unittest.TestCase):
    def test_matching_observation_passes(self):
        self.assertEqual(capture_probe.compare(observation(), ORACLE, "geth"), [])

    def test_each_single_difference_fails(self):
        def scenario(value, name):
            return next(row for row in value["scenarios"] if row["name"] == name)

        mutations = {
            "family": lambda o: o.update(family="bsc"),
            "baseline": lambda o: o.update(baseline_head_hash="0x" + "22" * 32),
            "missing scenario": lambda o: o["scenarios"].pop(),
            "duplicate scenario": lambda o: o["scenarios"].append(o["scenarios"][0]),
            "probe error": lambda o: scenario(o, "overlap_delayed_worker")["errors"].append("x"),
            "head": lambda o: scenario(o, "import_backpressure").update(head_number=2),
            "head hash": lambda o: scenario(o, "vm_safe_backpressure").update(head_hash="0x" + "33" * 32),
            "chunk": lambda o: scenario(o, "vm_safe_backpressure").update(chunk_bytes=1024),
            "buffer": lambda o: scenario(o, "overlap_delayed_worker").update(buffer_bytes=1),
            "peak": lambda o: scenario(o, "import_backpressure").update(peak_held=2001),
            "zero peak": lambda o: scenario(o, "import_backpressure").update(peak_held=0),
            "notes": lambda o: scenario(o, "vm_safe_backpressure").update(notes=1),
            "overlap waited": lambda o: scenario(o, "overlap_delayed_worker").update(waits=1),
            "never waited": lambda o: scenario(o, "vm_safe_backpressure").update(waits=0),
            "logs": lambda o: scenario(o, "overlap_delayed_worker")["log_sha256"].reverse(),
            "storage": lambda o: scenario(o, "overlap_delayed_worker").update(storage_slot0={}),
            "input digest": lambda o: scenario(o, "overlap_delayed_worker")["fields"][1].update(sha256="00" * 32),
            "input chunks": lambda o: scenario(o, "vm_safe_backpressure")["fields"][0].update(chunks=2),
            "extra input": lambda o: scenario(o, "vm_safe_backpressure")["fields"].append({}),
            "callback wait": lambda o: scenario(o, "vm_safe_backpressure").update(callback_wait_rejected=False),
            "block fields": lambda o: scenario(o, "import_backpressure")["fields"].pop(),
            "block tag": lambda o: scenario(o, "import_backpressure")["fields"][0].update(tag=1),
            "block chunks": lambda o: scenario(o, "import_backpressure")["fields"][0].update(chunks=4),
            "unexpected pause": lambda o: scenario(o, "overlap_delayed_worker").update(pause={}),
            "no pause": lambda o: scenario(o, "vm_safe_backpressure").update(pause=None),
            "pause kind": lambda o: scenario(o, "vm_safe_backpressure")["pause"].update(kind="import"),
            "pause head": lambda o: scenario(o, "import_backpressure")["pause"].update(head_number=2),
            "unobserved": lambda o: scenario(o, "import_backpressure")["pause"].update(observed=False),
            "moved": lambda o: scenario(o, "vm_safe_backpressure")["pause"].update(frames_after_hold=10),
            "returned": lambda o: scenario(o, "vm_safe_backpressure")["pause"].update(import_returned_during_hold=True),
            "callback open": lambda o: scenario(o, "import_backpressure")["pause"].update(callback_open_during_pause=True),
            "short hold": lambda o: scenario(o, "import_backpressure")["pause"].update(hold_milliseconds=299),
        }
        for label, mutate in mutations.items():
            with self.subTest(label):
                value = observation()
                mutate(value)
                self.assertTrue(capture_probe.compare(value, ORACLE, "geth"))


class PatchTest(unittest.TestCase):
    def source(self, text):
        root = Path(tempfile.mkdtemp()).resolve()
        (root / "core/vm").mkdir(parents=True)
        (root / "core/vm/interpreter.go").write_text(text)
        return root

    def test_inserts_once_before_the_hook_block(self):
        root = self.source("for {\n" + capture_probe.ANCHOR + "\t\tif debug {\n")
        row = capture_probe.patch_interpreter(root)
        text = (root / "core/vm/interpreter.go").read_text()
        self.assertEqual(text, "for {\n" + capture_probe.SAFE_POINT_CALL + capture_probe.ANCHOR + "\t\tif debug {\n")
        self.assertEqual(row["sha256_after"], hashlib.sha256(text.encode()).hexdigest())
        self.assertIn("SetExtractorSafePoint", (root / "core/vm/extractor_capture_probe.go").read_text())

    def test_rejects_missing_repeated_or_existing_anchor(self):
        for text in ("for {\n", capture_probe.ANCHOR * 2, "extractorSafePoint\n" + capture_probe.ANCHOR):
            with self.subTest(text=text), self.assertRaises(Rejected):
                capture_probe.patch_interpreter(self.source(text))

    def test_go_directive(self):
        self.assertEqual(capture_probe.go_directive("module x\n\ngo 1.26.10\n"), "1.26.10")
        with self.assertRaises(Rejected):
            capture_probe.go_directive("module x\n")


class FakeNative:
    """Stands in for Docker; writes the observation each probe would write."""
    observations = {}
    interrupt = False

    def __init__(self, output, deadline):
        self.output, self.nonce, self.commands, self.cleanup_complete = output, "n", [], True
        (output / "logs").mkdir()

    def command(self, image, argv, mounts, **kwargs):
        if FakeNative.interrupt:
            raise capture_probe.Interrupted("signal 15")
        family = argv[1]
        (mounts[1][0] / "probe.json").write_text(json.dumps(FakeNative.observations[family]))
        self.commands.append(argv)


class RunTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp()).resolve()
        lock = mock.patch.object(capture_probe, "LOCK", self.root / "lock")
        lock.start()
        self.addCleanup(lock.stop)
        docker = mock.patch.object(capture_probe.subprocess, "run", side_effect=self.fake_subprocess)
        docker.start()
        self.addCleanup(docker.stop)
        self.containers = ""
        native = mock.patch.object(capture_probe, "Native", FakeNative)
        native.start()
        self.addCleanup(native.stop)
        FakeNative.interrupt = False
        FakeNative.observations = {family: observation(family) for family in capture_probe.FAMILIES}
        self.preparation = self.prepared()

    def fake_subprocess(self, argv, **kwargs):
        if argv[0] == "docker":
            return mock.Mock(returncode=0, stdout=self.containers)
        return mock.Mock(stdout="0" * 40 + "\n" if "rev-parse" in argv else "")

    def prepared(self, result="PASS"):
        prep = self.root / "prep"
        prep.mkdir(exist_ok=True)
        (prep / "expected.json").write_text(json.dumps(ORACLE))
        (prep / "manifest.json").write_text(json.dumps({"image_inputs": {"debian-runtime-image": {
            "reference": "docker.io/library/debian@sha256:" + "a" * 64}}}))
        families = {}
        for family in capture_probe.FAMILIES:
            (prep / family).mkdir(exist_ok=True)
            (prep / family / "probe").write_bytes(b"binary")
            (prep / family / "genesis.json").write_text("{}")
            families[family] = {"binary": reference(prep / family / "probe"),
                                "genesis": reference(prep / family / "genesis.json")}
        record = {"kind": "owned-capture-probe-preparation", "result": result, "cleanup_complete": True,
                  "build_manifest": {"path": str(prep / "manifest.json")}, "families": families,
                  "fixture": [{"copy": reference(prep / "expected.json")}]}
        path = prep / f"preparation-{result}.json"
        path.write_text(json.dumps(record))
        return path

    def run_probe(self, name="run", preparation=None):
        preparation = preparation or self.preparation
        return capture_probe.run(preparation, reference(preparation)["sha256"], self.root / name)

    def test_passing_run(self):
        before = {number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}
        output, record = self.run_probe()
        self.assertEqual(record["result"], "PASS", record)
        self.assertEqual(json.loads((output / "run-record.json").read_text())["result"], "PASS")
        self.assertEqual({number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}, before)

    def test_oracle_difference_fails(self):
        FakeNative.observations["bsc"]["scenarios"][0]["errors"] = ["boom"]
        _, record = self.run_probe()
        self.assertEqual(record["result"], "FAIL")
        self.assertEqual(record["families"]["bsc"]["result"], "FAIL")

    def test_signal_still_writes_the_record(self):
        FakeNative.interrupt = True
        output, record = self.run_probe()
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("signal", json.loads((output / "run-record.json").read_text())["error"])

    def test_refusals_create_no_output(self):
        with self.assertRaises(Rejected):
            capture_probe.run(self.preparation, "0" * 64, self.root / "bad-digest")
        with self.assertRaises(Rejected):
            self.run_probe("failed-prep", self.prepared("FAIL"))
        self.containers = "abc\n"
        with self.assertRaises(Rejected):
            self.run_probe("foreign")
        self.containers = ""
        with open(self.root / "lock", "w") as holder:
            capture_probe.fcntl.flock(holder, capture_probe.fcntl.LOCK_EX)
            with self.assertRaises(Rejected):
                self.run_probe("locked")
        for name in ("bad-digest", "failed-prep", "foreign", "locked"):
            self.assertFalse((self.root / name).exists(), name)

    def test_existing_output_is_never_written(self):
        (self.root / "taken").mkdir()
        (self.root / "taken" / "keep").write_text("x")
        with self.assertRaises(OSError):
            self.run_probe("taken")
        self.assertEqual(os.listdir(self.root / "taken"), ["keep"])

    def test_output_inside_preparation_is_refused(self):
        with self.assertRaises(Rejected):
            capture_probe.run(self.preparation, reference(self.preparation)["sha256"], self.preparation.parent / "run")

    def test_changed_binary_fails_the_run(self):
        (self.root / "prep/bor/probe").write_bytes(b"changed")
        _, record = self.run_probe()
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("changed", record["error"])


if __name__ == "__main__":
    unittest.main()
