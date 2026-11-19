"""Exercise owned filesystem/record boundaries with only Docker work replaced."""

import contextlib
import fcntl
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import signal
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("type_paths_poly_orchestrated", HERE / "poly_native.py")
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)
import runner


class PolygonOrchestrationTests(unittest.TestCase):
    def setUp(self):
        self.context = contextlib.ExitStack()
        self.addCleanup(self.context.close)
        self.root = Path(self.context.enter_context(tempfile.TemporaryDirectory())).resolve()
        self.prepared = self.root / "prepared"
        self.lock = self.root / "runner.lock"
        self.failure = None
        self.cleanup = True
        self.signal_mask = signal.pthread_sigmask(signal.SIG_BLOCK, ())
        test = self

        class FakeNative:
            def __init__(self, output, deadline):
                self.output = output
                self.nonce = "portable-owned-runner"
                self.commands = []
                self.cleanup_complete = True

            def command(self, image, argv, mounts, **kwargs):
                self.commands.append({"argv": argv})
                self.cleanup_complete = test.cleanup
                if test.failure:
                    raise test.failure
                if "build" in argv:
                    (self.output / "bor/bin/probe").write_bytes(b"portable fake binary")
                if argv[0] == "/bin/probe":
                    (self.output / "probe/observation.json").write_text('{"diagnostic":"synthetic"}')

        self.context.enter_context(patch.object(runner, "LOCK", self.lock))
        self.context.enter_context(patch.object(driver.pins, "foreign_harness_containers", return_value=[]))
        self.context.enter_context(patch.object(driver.pins, "source_commit", return_value="a" * 40))
        self.context.enter_context(patch.object(driver.pins, "Native", FakeNative))
        self.context.enter_context(patch.object(driver.bor_admission_tree, "install", return_value={"portable": True}))
        build = self.root / "build"
        build.mkdir()
        archive = build / "source.tar"
        with tarfile.open(archive, "w") as stream:
            for name, data in {
                "tests/bor/testdata/genesis.json": json.dumps({"config": {"chainId": 15001, "bor": {}}, "alloc": {}}).encode(),
                "tests/bor/helper.go": b"package bor\n",
            }.items():
                item = tarfile.TarInfo(name)
                item.size = len(data)
                stream.addfile(item, io.BytesIO(data))
        archive_ref = driver.reference(archive)
        archive_ref["path"] = "source.tar"
        self.context.enter_context(patch.object(driver.profile, "SOURCE_ARCHIVE_SHA256", archive_ref["sha256"]))
        compiler = "docker.io/library/golang@sha256:" + "b" * 64
        manifest = {"evidence_kind": "actual-build", "task_id": "E01-01.builds",
                    "module_closures": {"bor": {"source_commit": driver.profile.SOURCE_COMMIT, "source_archive": archive_ref}},
                    "builds": {"bor": {"source_commit": driver.profile.SOURCE_COMMIT, "compiler_image": compiler}},
                    "image_inputs": {"golang-build-image": {"reference": compiler},
                                     "debian-runtime-image": {"reference": "docker.io/library/debian@sha256:" + "c" * 64}}}
        self.manifest = build / "manifest.json"
        self.manifest.write_text(json.dumps(manifest))
        self.sha = hashlib.sha256(self.manifest.read_bytes()).hexdigest()

    def prepare(self):
        return driver.prepare(self.manifest, self.sha, self.prepared)

    def released(self):
        with self.lock.open("a+") as handle:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(handle, fcntl.LOCK_UN)
        self.assertEqual(signal.pthread_sigmask(signal.SIG_BLOCK, ()), self.signal_mask)

    def saved(self, record, output, name, result):
        self.assertEqual(record["result"], result, record.get("error"))
        self.assertEqual(json.loads((output / name).read_text()), record)
        self.assertIsNone(record["ac_verdict"])
        self.released()

    def test_prepare_copies_real_sources_and_materializes_complete_owned_inputs(self):
        result = self.prepare()
        self.saved(result, self.prepared, "preparation.json", "PASS")
        self.assertTrue(result["cleanup_complete"])
        tool = driver.load(self.prepared / "tool.json")
        for field in ("inputs", "bodies"):
            for ref in tool[field]:
                self.assertEqual(driver.reference(Path(ref["path"])), ref)
        events = driver.load(self.prepared / "bor/bodies/clerk-corpus-from-1.json")
        self.assertEqual([row["id"] for row in events["event_records"]], ["1", "2"])

    def test_snapshot_copy_failure_is_retained_without_native_commands(self):
        with patch.object(driver, "copy_artifact", side_effect=OSError("injected copy failure")):
            result = self.prepare()
        self.saved(result, self.prepared, "preparation.json", "FAIL")
        self.assertIn("injected copy failure", result["error"])
        self.assertEqual(result["commands"], [])

    def test_snapshot_directory_collision_is_retained(self):
        original = driver.keep_code
        def collide(output, deadline):
            (output / "fixture").mkdir()
            return original(output, deadline)
        with patch.object(driver, "keep_code", side_effect=collide):
            result = self.prepare()
        self.saved(result, self.prepared, "preparation.json", "FAIL")
        self.assertIn("FileExistsError", result["error"])

    def test_existing_output_is_never_overwritten(self):
        self.prepared.mkdir()
        sentinel = self.prepared / "sentinel"
        sentinel.write_text("preserve")
        with self.assertRaises(FileExistsError):
            self.prepare()
        self.assertEqual(list(self.prepared.iterdir()), [sentinel])
        self.released()

    def test_command_failure_and_interrupt_keep_records_and_release_lock(self):
        for failure in (OSError("injected native failure"), InterruptedError("injected signal")):
            with self.subTest(failure=failure):
                self.prepared = self.root / ("attempt-" + type(failure).__name__)
                self.failure = failure
                result = self.prepare()
                self.saved(result, self.prepared, "preparation.json", "FAIL")
                self.assertIn(str(failure), result["error"])
                self.assertEqual(len(result["commands"]), 1)

    def test_incomplete_runner_cleanup_cannot_pass(self):
        self.cleanup = False
        result = self.prepare()
        self.saved(result, self.prepared, "preparation.json", "FAIL")
        self.assertFalse(result["cleanup_complete"])

    def test_run_preserves_diagnostic_scope_and_native_failure(self):
        self.assertEqual(self.prepare()["result"], "PASS")
        tool = self.prepared / "tool.json"
        digest = hashlib.sha256(tool.read_bytes()).hexdigest()
        output = self.root / "run"
        result = driver.run(tool, digest, output)
        self.saved(result, output, "run.json", "PASS")
        self.assertIn("NOT_EVALUATED", result["acceptance"])
        self.failure = OSError("run failed")
        output = self.root / "failed-run"
        result = driver.run(tool, digest, output)
        self.saved(result, output, "run.json", "FAIL")
        self.assertIn("run failed", result["error"])


if __name__ == "__main__":
    unittest.main()
