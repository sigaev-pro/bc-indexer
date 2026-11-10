"""Portable checks for the Geth probe driver: preparation and the measured run with Docker faked
at the Native boundary. No native runtime claim."""

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

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1]))
sys.path.insert(0, str(HERE))
from artifacts import Rejected, reference  # noqa: E402
import capture_probe  # noqa: E402
import source_inputs_geth as geth  # noqa: E402
import test_source_inputs_geth as oracle_tests  # noqa: E402

COMPILER = "docker.io/library/golang@sha256:" + "b" * 64
RUNTIME = "docker.io/library/debian@sha256:" + "a" * 64
UNIT_OK = "ok  \tgithub.com/ethereum/go-ethereum/extractor/source_inputs\t1.0s\n"


class FakeNative:
    """Stands in for Docker; writes what each command would write."""
    interrupt = False
    cleanup_complete = True
    unit_stdout = UNIT_OK
    failing_build = None
    probe_observation = None
    mutant_observation = None

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
        if argv[0].startswith("/probe/"):
            name = Path(argv[0]).name
            value = FakeNative.probe_observation if name == "probe" else FakeNative.mutant_observation
            (targets["/out"] / "probe.json").write_text(json.dumps(value))
        elif argv[1] == "build":
            output = argv[argv.index("-o") + 1]
            if FakeNative.failing_build and output.endswith(FakeNative.failing_build):
                raise geth.capture_probe.Rejected("Native command failed")
            (targets["/work/bin"] / Path(output).name).write_bytes(output.encode())
        elif argv[1] == "test":
            stdout.write_text("=== RUN TestX\n" + FakeNative.unit_stdout)
        stdout.touch()
        return stdout


class DriverFixture(unittest.TestCase):
    """Temporary roots, a synthetic accepted build and fakes; it defines no tests of its own."""

    def setUp(self):
        self.root = Path(tempfile.mkdtemp()).resolve()
        self.addCleanup(shutil.rmtree, self.root)
        for target, name, value in ((capture_probe, "LOCK", self.root / "lock"),
                                    (capture_probe.subprocess, "run", None), (geth, "Native", FakeNative)):
            patch = mock.patch.object(target, name, side_effect=self.fake_subprocess) if value is None else \
                mock.patch.object(target, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        self.containers = ""
        FakeNative.interrupt, FakeNative.cleanup_complete = False, True
        FakeNative.unit_stdout, FakeNative.failing_build = UNIT_OK, None
        FakeNative.probe_observation = oracle_tests.observation()
        FakeNative.mutant_observation = {"family": "geth"}

    def fake_subprocess(self, argv, **kwargs):
        if argv[0] == "docker" and self.containers is None:
            raise FileNotFoundError("docker")
        if argv[0] == "docker":
            return mock.Mock(returncode=0, stdout=self.containers)
        return mock.Mock(stdout="0" * 40 + "\n" if "rev-parse" in argv else "")

    def build(self, name="build", compiler=COMPILER, family_compiler=None, kind="actual-build", runtime=RUNTIME,
              closure_commit=oracle_tests.COMMIT):
        root = self.root / name
        root.mkdir()
        staging = root / "src"
        (staging / "core").mkdir(parents=True)
        (staging / "go.mod").write_text("module github.com/ethereum/go-ethereum\n\ngo 1.25.0\n")
        (staging / geth.ANCHOR_FILE).write_text("func Process() {\n" + geth.ANCHOR + "}\n")
        with tarfile.open(root / "geth.tar", "w") as stream:
            for member in ("go.mod", geth.ANCHOR_FILE):
                stream.add(staging / member, arcname=member)
        archive = reference(root / "geth.tar")
        archive["path"] = "geth.tar"
        module = {}
        for field in ("info", "mod", "zip"):
            (root / f"geth.{field}").write_text(field)
            module[field] = reference(root / f"geth.{field}")
            module[field]["path"] = f"geth.{field}"
        (root / "cache/geth/mod").mkdir(parents=True)
        manifest = {"evidence_kind": kind, "task_id": "E01-01.builds",
                    "builds": {"geth": {"source_commit": oracle_tests.COMMIT, "compiler_image": family_compiler or compiler}},
                    "module_closures": {"geth": {"source_commit": closure_commit, "source_archive": archive,
                                                 "artifacts": [module]}},
                    "image_inputs": {"golang-build-image": {"reference": compiler},
                                     "debian-runtime-image": {"reference": runtime}}}
        path = root / "manifest.json"
        path.write_text(json.dumps(manifest))
        return path

    def prepare(self, name="prep", manifest=None):
        manifest = manifest or self.build()
        return geth.prepare(manifest, reference(manifest)["sha256"], self.root / name)

    def prepared(self, prep="prep", **build):
        output, record = self.prepare(prep, manifest=self.build(**build))
        self.assertEqual(record["result"], "PASS", record.get("error"))
        return output / "preparation.json"

    def run_probe(self, preparation, name="run"):
        return geth.run(preparation, reference(preparation)["sha256"], self.root / name)

    def changed_preparation(self, preparation, change, name="changed.json"):
        record = json.loads(preparation.read_text())
        change(record)
        path = preparation.parent / name
        path.write_text(json.dumps(record))
        return path


class PrepareTest(DriverFixture):
    def test_passing_preparation(self):
        before = {number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}
        output, record = self.prepare()
        self.assertEqual(record["result"], "PASS", record.get("error"))
        self.assertEqual(json.loads((output / "preparation.json").read_text())["result"], "PASS")
        self.assertIn(geth.ANCHOR + geth.HOOK_CALL, (output / "geth/source" / geth.ANCHOR_FILE).read_text())
        names = [mutant["name"] for mutant in json.loads((geth.FIXTURE / "native_mutants.json").read_text())["mutants"]]
        self.assertEqual(sorted(record["mutants"]), sorted(names))
        # Every mutant was restored: the installed probe equals the fixture after all mutant builds.
        self.assertEqual((output / "geth/source/cmd/extractor-inputs-probe/probe.go").read_bytes(),
                         (geth.FIXTURE / "probe.go").read_bytes())
        self.assertEqual(record["module_artifacts_verified"], 1)
        self.assertEqual({number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}, before)

    def test_failures_still_write_the_record(self):
        cases = {"unpinned": dict(compiler="golang:latest"),
                 "family": dict(family_compiler="docker.io/library/golang@sha256:" + "d" * 64),
                 "closure": dict(closure_commit="e" * 40)}
        for name, arguments in cases.items():
            with self.subTest(name):
                output, record = self.prepare(name, self.build(f"build-{name}", **arguments))
                self.assertEqual(record["result"], "FAIL")
                self.assertTrue((output / "preparation.json").exists())
        FakeNative.unit_stdout = "FAIL\tgithub.com/ethereum/go-ethereum/extractor/source_inputs\n"
        _, record = self.prepare("unit", self.build("build-unit"))
        self.assertIn("Shared unit tests did not pass", record["error"])
        FakeNative.unit_stdout = UNIT_OK
        FakeNative.failing_build = "mutant-native_lookup_bypass"
        output, record = self.prepare("mutant", self.build("build-mutant"))
        self.assertEqual(record["result"], "FAIL")
        # A failed mutant build still restores the installed probe source.
        self.assertEqual((output / "geth/source/cmd/extractor-inputs-probe/probe.go").read_bytes(),
                         (geth.FIXTURE / "probe.go").read_bytes())
        FakeNative.failing_build = None
        FakeNative.cleanup_complete = False
        _, record = self.prepare("cleanup", self.build("build-cleanup"))
        self.assertEqual(record["result"], "FAIL")
        FakeNative.cleanup_complete = True
        FakeNative.interrupt = True
        output, record = self.prepare("signal", self.build("build-signal"))
        self.assertIn("Interrupted", json.loads((output / "preparation.json").read_text())["error"])

    def test_refusals_create_no_output(self):
        manifest = self.build()
        with self.assertRaises(Rejected):
            geth.prepare(manifest, "0" * 64, self.root / "digest")
        with self.assertRaises(Rejected):
            self.prepare("kind", self.build("build-kind", kind="plan"))
        for output in (manifest.parent / "inside", geth.ROOT / "test/harness/prep-out"):
            with self.subTest(output=output), self.assertRaises(Rejected):
                geth.prepare(manifest, reference(manifest)["sha256"], output)
            self.assertFalse(output.exists())
        self.addCleanup(os.chdir, os.getcwd())
        os.chdir(manifest.parent)
        with self.assertRaises(Rejected):
            geth.prepare(Path(manifest.name), reference(manifest)["sha256"], self.root / "relative")
        for name in ("digest", "kind", "relative"):
            self.assertFalse((self.root / name).exists(), name)

    def test_main_prepare_exit_code(self):
        manifest = self.build()
        self.assertEqual(geth.main(["prepare", "--build-manifest", str(manifest), "--sha256",
                                    reference(manifest)["sha256"], "--output", str(self.root / "main")]), 0)


class RunTest(DriverFixture):
    def setUp(self):
        super().setUp()
        self.preparation = self.prepared()

    def test_passing_run(self):
        before = {number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}
        output, record = self.run_probe(self.preparation)
        self.assertEqual(record["result"], "PASS", record.get("error"))
        self.assertEqual(record["probe"]["result"], "PASS")
        self.assertEqual({row["result"] for row in record["mutants"].values()}, {"KILLED"})
        self.assertEqual(json.loads((output / "run-record.json").read_text())["result"], "PASS")
        self.assertEqual({number: signal.getsignal(number) for number in capture_probe.STOP_SIGNALS}, before)
        self.assertIs(record["public_profile_credit"], False)
        self.assertIsNone(record["ac_verdict"])

    def test_surviving_mutant_fails_the_run(self):
        FakeNative.mutant_observation = oracle_tests.observation()
        _, record = self.run_probe(self.preparation)
        self.assertEqual(record["probe"]["result"], "PASS")
        self.assertEqual({row["result"] for row in record["mutants"].values()}, {"SURVIVED"})
        self.assertEqual(record["result"], "FAIL")

    def test_oracle_difference_fails(self):
        FakeNative.probe_observation["family"] = "bsc"
        _, record = self.run_probe(self.preparation)
        self.assertEqual(record["probe"]["result"], "FAIL")
        self.assertEqual(record["result"], "FAIL")
        FakeNative.probe_observation = ["not", "an", "object"]
        _, record = self.run_probe(self.preparation, "malformed")
        self.assertEqual(record["probe"]["result"], "FAIL")

    def test_signal_still_writes_the_record(self):
        FakeNative.interrupt = True
        output, record = self.run_probe(self.preparation)
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("signal", json.loads((output / "run-record.json").read_text())["error"])

    def test_incomplete_cleanup_fails(self):
        FakeNative.cleanup_complete = False
        _, record = self.run_probe(self.preparation)
        self.assertEqual(record["result"], "FAIL")

    def test_refusals_create_no_output(self):
        with self.assertRaises(Rejected):
            geth.run(self.preparation, "0" * 64, self.root / "bad-digest")
        cases = {"failed": lambda r: r.update(result="FAIL"), "dirty": lambda r: r.update(cleanup_complete=False),
                 "kind": lambda r: r.update(kind="owned-capture-probe-preparation")}
        for name, change in cases.items():
            with self.subTest(name), self.assertRaises(Rejected):
                self.run_probe(self.changed_preparation(self.preparation, change, f"{name}.json"), name)
        self.containers = "abc\n"
        with self.assertRaises(Rejected):
            self.run_probe(self.preparation, "foreign")
        self.containers = ""
        with open(self.root / "lock", "w") as holder:
            capture_probe.fcntl.flock(holder, capture_probe.fcntl.LOCK_EX)
            with self.assertRaises(Rejected):
                self.run_probe(self.preparation, "locked")
        for output in (self.preparation.parent / "inside", geth.ROOT / "test/harness/run-out"):
            with self.subTest(output=output), self.assertRaises(Rejected):
                geth.run(self.preparation, reference(self.preparation)["sha256"], output)
            self.assertFalse(output.exists())
        for name in ("bad-digest", "failed", "dirty", "kind", "foreign", "locked"):
            self.assertFalse((self.root / name).exists(), name)

    def test_relative_preparation_is_refused(self):
        self.addCleanup(os.chdir, os.getcwd())
        os.chdir(self.preparation.parent)
        with self.assertRaises(Rejected):
            geth.run(Path(self.preparation.name), reference(self.preparation)["sha256"], self.root / "relative")
        self.assertFalse((self.root / "relative").exists())

    def test_existing_output_is_never_written(self):
        (self.root / "taken").mkdir()
        (self.root / "taken" / "keep").write_text("x")
        with self.assertRaises(OSError):
            self.run_probe(self.preparation, "taken")
        self.assertEqual(os.listdir(self.root / "taken"), ["keep"])

    def test_run_failures_write_the_record(self):
        cases = {
            "no-oracle": (lambda r: r.update(fixture=[]), "exactly one oracle"),
            "no-mutants": (lambda r: r.update(mutants={}), "built no mutants"),
        }
        for name, (change, text) in cases.items():
            with self.subTest(name):
                output, record = self.run_probe(self.changed_preparation(self.preparation, change, f"{name}.json"), name)
                self.assertEqual(record["result"], "FAIL")
                self.assertIn(text, record["error"])
                self.assertTrue((output / "run-record.json").exists())
        binary = Path(json.loads(self.preparation.read_text())["binary"]["path"])
        original = binary.read_bytes()
        binary.write_bytes(b"changed")
        _, record = self.run_probe(self.preparation, "changed-binary")
        self.assertEqual(record["result"], "FAIL")
        binary.write_bytes(original)

    def test_unpinned_runtime_fails(self):
        preparation = self.prepared("prep-unpinned", name="build-unpinned", runtime="debian:latest")
        _, record = self.run_probe(preparation, "unpinned")
        self.assertIn("Unpinned runtime image", record["error"])

    def test_main_run_exit_codes(self):
        sha = reference(self.preparation)["sha256"]
        self.assertEqual(geth.main(["run", str(self.preparation), "--sha256", sha, "--output",
                                    str(self.root / "main-pass")]), 0)
        FakeNative.mutant_observation = oracle_tests.observation()
        self.assertEqual(geth.main(["run", str(self.preparation), "--sha256", sha, "--output",
                                    str(self.root / "main-fail")]), 1)
        self.assertEqual(geth.main(["run", str(self.preparation), "--sha256", "0" * 64, "--output",
                                    str(self.root / "main-refused")]), 2)
        self.assertFalse((self.root / "main-refused").exists())


if __name__ == "__main__":
    unittest.main()
