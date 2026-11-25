"""Portable checks for the capture-enabled BSC client image procedure. No native runtime claim."""

import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import bsc_capture_build as build
from artifacts import Rejected, reference
import runner

RUN = subprocess.run
COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
GOLANG = "docker.io/library/golang@sha256:" + "1" * 64
DEBIAN = "docker.io/library/debian@sha256:" + "2" * 64
IMAGE_ID, BASE_ID = "sha256:" + "3" * 64, "sha256:" + "4" * 64
EPOCH = 1793671467  # 2026-11-03T02:04:27Z
TAG = "bc-indexer/bsc-capture:1.8.2-capture"
SELECTED = {"repository": "bnb-chain/bsc", "version": "v1.8.2", "commit": COMMIT, "go_version": "go1.27.3",
            "compiler_image": GOLANG, "runtime_image": DEBIAN}
VERSION = (f"Geth\nVersion: 1.8.2\nGit Commit: {COMMIT}\nArchitecture: amd64\nGo Version: go1.27.3\n"
           "Operating System: linux\nGOPATH=\nGOROOT=\n")
CONFIG = ('[Eth]\nNetworkId = 97\nSyncMode = "full"\nNoPruning = false\nStateScheme = "hash"\n'
          'PruneAncientData = false\n\n[Eth.Miner]\nSyncMode = "snap"\nStateScheme = "path"\n\n[Eth.Miner.Mev]\n'
          'Enabled = false\n')
START_LOG = "\n".join("INFO [11-25|03:10:32.206] " + line for line in build.START_MARKERS) + "\n"
REFUSALS = {"snap-sync": (1, "Fatal: Failed to register the Ethereum service: sync mode snap (bsc.snap-sync)"),
            "foreign-store": (1, "Fatal: native store identity mismatch: store is bound to x")}


def facts():
    patches = [{"patch": {"path": "/p/" + name, "sha256": f"{index:064x}", "size_bytes": 10}, "files": []}
               for index, name in enumerate(build.PATCHES)]
    installed = [{"relative": "extractor/bsc/lifecycle/lifecycle.go", "source": {"sha256": "a" * 64, "size_bytes": 3}}]
    return build.image_facts(SELECTED, patches, installed, "b" * 40, "go version go1.27.3 linux/amd64",
                             {"sha256": "c" * 64}, {"sha256": "d" * 64}, {"sha256": "e" * 64}, EPOCH)


def done(argv, code=0, out=b"", err=b""):
    return subprocess.CompletedProcess(argv, code, out, err)


class TagTest(unittest.TestCase):
    def test_tag_of_an_upstream_version(self):
        self.assertEqual(build.tag_for("v1.8.2"), TAG)

    def test_other_versions_are_refused(self):
        for version in ("1.8.2", "v1.8", "v1.8.2-x", "v01.8.2", "main", "", None, 2):
            with self.subTest(version=version), self.assertRaises(Rejected):
                build.tag_for(version)


class LockTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp()).resolve()
        self.lock = {"components": {"bsc": {"source_repository": SELECTED["repository"], "version": "v1.8.2", "commit": COMMIT},
                                    "go": {"version": "go1.27.3"},
                                    "golang-build-image": {"pull_reference": GOLANG},
                                    "debian-runtime-image": {"pull_reference": DEBIAN}}}
        self.manifest = {"builds": {"bsc": {"source_commit": COMMIT, "compiler_image": GOLANG}},
                         "image_inputs": {"golang-build-image": {"reference": GOLANG},
                                          "debian-runtime-image": {"reference": DEBIAN}}}

    def pins(self):
        path = self.root / "candidate-lock.json"
        path.write_text(json.dumps(self.lock))
        return build.lock_pins(path, self.manifest)

    def test_bound_pins(self):
        lock_ref, selected = self.pins()
        self.assertEqual(selected, SELECTED)
        self.assertEqual(lock_ref["sha256"], hashlib.sha256((self.root / "candidate-lock.json").read_bytes()).hexdigest())

    def test_each_difference_is_refused(self):
        changes = [lambda: self.lock["components"]["bsc"].update(commit="0" * 40),
                   lambda: self.lock["components"]["debian-runtime-image"].update(pull_reference="debian@sha256:" + "9" * 64),
                   lambda: self.lock["components"]["golang-build-image"].update(pull_reference=GOLANG[:-1] + "0"),
                   lambda: self.manifest["builds"]["bsc"].update(compiler_image="golang:latest"),
                   lambda: self.manifest["image_inputs"]["debian-runtime-image"].update(reference="debian:13")]
        for index, change in enumerate(changes):
            original = json.dumps(self.lock), json.dumps(self.manifest)
            change()
            with self.subTest(change=index), self.assertRaises(Rejected):
                self.pins()
            self.lock, self.manifest = json.loads(original[0]), json.loads(original[1])


class LabelsTest(unittest.TestCase):
    def test_labels_hold_the_facts_and_no_local_path(self):
        rows = facts()
        labels = build.image_labels(rows)
        expected = {"source-commit": COMMIT, "source-version": "v1.8.2", "go-version": "go1.27.3",
                    "compiler-image": GOLANG, "base-image": DEBIAN, "binary-sha256": "c" * 64,
                    "lock-sha256": "e" * 64, "harness-commit": "b" * 40,
                    "patches": ";".join(f"{name}={index:064x}" for index, name in enumerate(build.PATCHES))}
        self.assertEqual({key: labels["io.extractor." + key] for key in expected}, expected)
        self.assertEqual(labels["org.opencontainers.image.version"], "1.8.2-capture")
        self.assertEqual(labels["org.opencontainers.image.revision"], COMMIT)
        self.assertEqual(labels["org.opencontainers.image.source"], "https://github.com/bnb-chain/bsc")
        self.assertEqual(labels["org.opencontainers.image.base.name"], "docker.io/library/debian")
        self.assertEqual(labels["org.opencontainers.image.base.digest"], "sha256:" + "2" * 64)
        self.assertEqual([row["name"] for row in rows["patches"]], list(build.PATCHES))
        self.assertNotIn("/p/", json.dumps(labels))
        self.assertEqual(len(labels), 16)

    def test_changed_labels_are_reported(self):
        rows = facts()
        self.assertIsNone(build.check_labels(build.image_labels(rows), rows))
        labels = {**build.image_labels(rows), "io.extractor.source-commit": "0" * 40}
        del labels["org.opencontainers.image.revision"]
        with self.assertRaisesRegex(Rejected, "source-commit.*revision"):
            build.check_labels(labels, rows)


class VersionAndConfigTest(unittest.TestCase):
    def test_version_output(self):
        fields = build.version_fields(VERSION)
        self.assertEqual(fields["Version"], "1.8.2")
        self.assertNotIn("Geth", fields)
        self.assertNotIn("GOPATH", fields)
        self.assertIsNone(build.check_version(fields, SELECTED))
        for wrong in ({"Version": "1.8.1"}, {"Git Commit": "0" * 40}, {"Go Version": "go1.27.2"},
                      {"Architecture": "arm64"}, {"Operating System": "darwin"}):
            with self.subTest(wrong=wrong), self.assertRaises(Rejected):
                build.check_version({**fields, **wrong}, SELECTED)
        with self.assertRaises(Rejected):
            build.version_fields("Version: 1\nVersion: 2\n")

    def test_effective_config_reads_only_the_eth_section(self):
        self.assertEqual(build.config_values(CONFIG, "Eth", build.CONFIG_VALUES), build.CONFIG_VALUES)
        self.assertIsNone(build.check_effective_config(CONFIG))
        for text in (CONFIG.replace('SyncMode = "full"', 'SyncMode = "snap"'),
                     CONFIG.replace('StateScheme = "hash"\n', ""),
                     CONFIG.replace("NoPruning = false", "NoPruning = true"),
                     CONFIG.replace("PruneAncientData = false", "PruneAncientData = true"), "",
                     CONFIG.replace("StateScheme", "GCMode"),
                     CONFIG.replace("[Eth]", "[Node]")):
            with self.subTest(text=text[-80:]), self.assertRaises(Rejected):
                build.check_effective_config(text)
        with self.assertRaises(Rejected):
            build.config_values('[Eth]\nStateScheme = "hash"\nStateScheme = "path"\n', "Eth", build.CONFIG_VALUES)


class StartTest(unittest.TestCase):
    def test_clean_start(self):
        self.assertIsNone(build.check_start(START_LOG, 0))
        for marker in build.START_MARKERS:
            with self.subTest(marker=marker), self.assertRaises(Rejected):
                build.check_start(START_LOG.replace(marker, "x"), 0)
        with self.assertRaisesRegex(Rejected, "exited with 1"):
            build.check_start(START_LOG, 1)

    def test_refusal(self):
        self.assertIsNone(build.check_refusal(REFUSALS["snap-sync"][1], 1, "bsc.snap-sync"))
        with self.assertRaisesRegex(Rejected, "passed"):
            build.check_refusal(REFUSALS["snap-sync"][1], 0, "bsc.snap-sync")
        with self.assertRaisesRegex(Rejected, "does not name"):
            build.check_refusal("Fatal: other error", 1, "bsc.snap-sync")

    def test_start_scripts(self):
        script = build.start_script(["--chapel", "--datadir", "/home"], "start", 20)
        self.assertIn("geth --chapel --datadir /home > /work/start.out 2>&1 &", script)
        self.assertIn('until grep -q "' + build.READY_MARKER + '" /work/start.out', script)
        self.assertIn("\nsleep 20\n", script)
        self.assertIn('kill -INT "$pid" 2>/dev/null\nwait "$pid"\necho $? > /work/start.exit', script)
        self.assertEqual(build.start_script(["--chapel"], "control"),
                         "geth --chapel > /work/control.out 2>&1; echo $? > /work/control.exit")

    def test_profile_and_commands(self):
        self.assertEqual(build.PROFILE, ("--state.scheme", "hash", "--gcmode", "full", "--syncmode", "full"))
        self.assertEqual(build.OFFLINE, ("--maxpeers", "0", "--nodiscover", "--port", "0", "--ipcdisable"))
        arguments = build.build_arguments(COMMIT)
        self.assertEqual(arguments[:4], [build.GO, "build", "-trimpath", "-buildvcs=false"])
        self.assertEqual(arguments[4:6], ["-ldflags", "-X github.com/ethereum/go-ethereum/internal/version.gitCommit=" + COMMIT])
        self.assertEqual(arguments[-3:], ["-o", "/work/generated/geth", "./cmd/geth"])
        self.assertEqual(build.capture_config("a" * 32, COMMIT),
                         {"store_id": "a" * 32, "producer_id": "bsc-capture-smoke", "run_id": "run-1",
                          "native_commit": COMMIT, "genesis_file": "/config/genesis.json"})

    def test_patch_set(self):
        rows = [{"patch": {"path": "/adapters/" + name}} for name in build.PATCHES]
        build.check_patch_set(rows)
        for wrong in (rows[:-1], rows[::-1], rows + [{"patch": {"path": "/x/09-other.diff"}}], []):
            with self.assertRaises(Rejected):
                build.check_patch_set(wrong)


class EpochTest(unittest.TestCase):
    def test_source_time_of_the_accepted_vanilla_configuration(self):
        root = Path(tempfile.mkdtemp()).resolve()
        (root / "oci").mkdir()
        config = root / "oci" / "config.json"
        config.write_text(json.dumps({"created": "2026-11-03T02:04:27Z"}))
        manifest = {"builds": {"bsc": {"oci_config": reference(config)}}}
        self.assertEqual(build.source_date_epoch(root / "manifest.json", manifest), EPOCH)
        for created in ("2026-11-03T02:04:27", "yesterday", 17, None):
            config.write_text(json.dumps({"created": created}))
            with self.subTest(created=created), self.assertRaises(Rejected):
                build.source_date_epoch(root / "manifest.json", manifest)
        with self.assertRaises(Rejected):
            build.source_date_epoch(root / "manifest.json", {"builds": {"bsc": {}}})


class FakeNative:
    """Stands in for Docker at the Native boundary; writes what each command would write."""

    instances = []
    version, config, control, binary = VERSION, CONFIG, {}, b"geth"

    def __init__(self, output, deadline):
        self.output, self.nonce, self.commands, self.deadline = output, "n", [], deadline
        self.cleanup_complete = True
        FakeNative.instances.append(self)
        (output / "logs").mkdir()

    def command(self, image, argv, mounts, **kwargs):
        targets = {target: source for source, target, _ in mounts}
        self.commands.append((image, argv, kwargs.get("environment"), kwargs.get("entrypoint")))
        stdout = self.output / "logs" / f"{len(self.commands):03d}.stdout"
        if argv[:2] == [build.GO, "build"]:
            (targets["/work/generated"] / "geth").write_bytes(self.binary)
        elif argv == [build.GO, "version"]:
            stdout.write_text("go version go1.27.3 linux/amd64\n")
        elif argv == ["version"]:
            stdout.write_text(self.version)
        elif argv[-1] == "dumpconfig":
            stdout.write_text(self.config)
        elif argv[-1] == "dumpgenesis":
            stdout.write_text(json.dumps({"config": {"chainId": 97}, "alloc": {}}))
        elif kwargs.get("entrypoint") == "/bin/sh":
            name = argv[-1].split("/work/")[1].split(".out")[0]
            code, text = self.control[name]
            (targets["/work"] / (name + ".exit")).write_text(f"{code}\n")
            (targets["/work"] / (name + ".out")).write_text(text)
        stdout.touch(exist_ok=True)
        return stdout


class FakeDocker:
    """Answers the Docker CLI of the host: inspect, version, build and remove."""

    def __init__(self):
        self.calls, self.labels, self.removed = [], build.image_labels(facts()), []

    def __call__(self, argv, **kwargs):
        return FakeDocker.answer(self, argv, **kwargs)

    def answer(self, argv, **kwargs):
        if argv[0] != "docker":
            return RUN(argv, **kwargs)
        self.calls.append(argv)
        image = lambda identifier: json.dumps([{"Architecture": "amd64", "Os": "linux", "Id": identifier, "Config": {
            "Labels": self.labels, "Entrypoint": ["/usr/local/bin/geth"]},
            "RootFS": {"Layers": ["sha256:" + "5" * 64] + (["sha256:" + "6" * 64] if identifier == IMAGE_ID else [])}}]).encode()
        if argv[1] == "ps":
            return done(argv, 0, b"")
        if argv[:3] == ["docker", "image", "inspect"] and "--format" in argv:
            return done(argv, 1, b"", b"Error: No such image")
        if argv[:3] == ["docker", "image", "inspect"] and argv[3] == DEBIAN:
            return done(argv, 0, json.dumps([{"Architecture": "amd64", "Os": "linux", "Id": BASE_ID, "Config": {},
                                              "RepoDigests": ["debian@" + DEBIAN.partition("@")[2]],
                                              "RootFS": {"Layers": ["sha256:" + "5" * 64]}}]).encode())
        if argv[:2] == ["docker", "version"]:
            return done(argv, 0, b"29.8.2\n")
        if argv[1] == "build":
            position = argv.index("--label")
            self.labels = {}
            while argv[position] == "--label":
                self.labels[argv[position + 1].partition("=")[0]] = argv[position + 1].partition("=")[2]
                position += 2
            Path(argv[argv.index("--iidfile") + 1]).write_text(IMAGE_ID + "\n")
            return done(argv, 0, b"", b"")
        if argv[:3] == ["docker", "image", "inspect"]:
            return done(argv, 0, image(IMAGE_ID))
        if argv[:3] == ["docker", "image", "rm"]:
            self.removed.append(argv[-1])
            return done(argv, 0, b"")
        raise AssertionError("unexpected Docker command: " + " ".join(argv))


class ProcedureFixture(unittest.TestCase):
    """Temporary roots and fakes; it defines no tests of its own."""

    def setUp(self):
        FakeNative.instances = []
        self.root = Path(tempfile.mkdtemp()).resolve()
        self.output, self.accepted = self.root / "out", self.root / "accepted"
        (self.accepted / "cache" / "bsc" / "mod").mkdir(parents=True)
        (self.accepted / "oci").mkdir()
        config = self.accepted / "oci" / "config.json"
        config.write_text(json.dumps({"created": "2026-11-03T02:04:27Z"}))
        manifest = self.accepted / "manifest.json"
        manifest.write_text(json.dumps({
            "evidence_kind": "actual-build", "task_id": "E01-01.builds",
            "builds": {"bsc": {"source_commit": COMMIT, "compiler_image": GOLANG, "oci_config": reference(config)}},
            "image_inputs": {"golang-build-image": {"reference": GOLANG},
                             "debian-runtime-image": {"reference": DEBIAN}}}))
        self.sha = reference(manifest)["sha256"]
        lock = self.root / "candidate-lock.json"
        lock.write_text(json.dumps({"components": {
            "bsc": {"source_repository": SELECTED["repository"], "version": "v1.8.2", "commit": COMMIT},
            "go": {"version": "go1.27.3"}, "golang-build-image": {"pull_reference": GOLANG},
            "debian-runtime-image": {"pull_reference": DEBIAN}}}))
        self.docker = FakeDocker()
        patches = [mock.patch.object(build, "LOCK_PATH", lock),
                   mock.patch.object(build.pins, "source_commit", return_value="b" * 40),
                   mock.patch.object(build.pins, "Native", FakeNative),
                   mock.patch.object(runner, "LOCK", self.root / "runner.lock"),
                   mock.patch.object(build.subprocess, "run", self.docker),
                   mock.patch.object(build.probe14, "extract_source", self.extract),
                   mock.patch.object(build.bsc_lifecycle, "install", self.install)]
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def extract(self, manifest, manifest_path, root, deadline):
        source = root / "source"
        source.mkdir()
        (source / "go.mod").write_text("module github.com/ethereum/go-ethereum\n")
        (source / "go.sum").write_text("")
        return {"source_commit": COMMIT}, reference(source / "go.mod"), source

    def install(self, source, root, deadline):
        directory = root / "patches"
        directory.mkdir()
        patched = []
        for index, name in enumerate(build.PATCHES):
            path = directory / name
            path.write_text(name + "\n")
            patched.append({"patch": reference(path), "files": [
                {"path": f"core/file{index}.go", "new": index % 2 == 0, "before_sha256": None,
                 "after_sha256": f"{index:064x}"}]})
        installed = [{"relative": "extractor/bsc/lifecycle/lifecycle.go", "source": reference(source / "go.mod"),
                      "retained": reference(source / "go.mod")}]
        return patched, installed

    @property
    def native(self):
        """The owned container commands (image, argv, environment, entry point) of the last run."""
        return [row for row in FakeNative.instances[-1].commands if isinstance(row, tuple)]


class BuildTest(ProcedureFixture):
    def test_build_passes_and_records_the_provenance(self):
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual((proof["result"], proof["error"], proof["cleanup_complete"]), ("PASS", None, True))
        self.assertEqual((proof["image"], proof["previous_image_id"]), (TAG, None))
        provenance = json.loads((self.output / "provenance.json").read_text())
        self.assertEqual(provenance["image_id"], IMAGE_ID)
        self.assertEqual(provenance["runtime_image_id"], BASE_ID)
        self.assertEqual(provenance["docker_server_version"], "29.8.2")
        self.assertEqual(provenance["source_date_epoch"], EPOCH)
        self.assertEqual(provenance["upstream"], {"repository": "bnb-chain/bsc", "version": "v1.8.2", "commit": COMMIT})
        self.assertEqual([row["name"] for row in provenance["patches"]], list(build.PATCHES))
        self.assertEqual(provenance["installed"][0]["path"], "extractor/bsc/lifecycle/lifecycle.go")
        self.assertEqual(provenance["effective_config"], build.CONFIG_VALUES)
        self.assertEqual(provenance["version_output"]["Git Commit"], COMMIT)
        binary = self.output / "bsc" / "generated" / "geth"
        self.assertEqual(provenance["binary_sha256"], hashlib.sha256(binary.read_bytes()).hexdigest())
        self.assertEqual(proof["provenance"], reference(self.output / "provenance.json"))
        context = self.output / "image" / "geth"
        self.assertEqual(oct(context.stat().st_mode & 0o777), "0o755")
        self.assertEqual(int(context.stat().st_mtime), EPOCH)
        self.assertEqual((self.output / "image-id.txt").read_text().strip(), IMAGE_ID)
        command = next(argv for argv in self.docker.calls if argv[1] == "build")
        self.assertEqual(self.docker.labels, build.image_labels({**provenance}))
        self.assertEqual(command[command.index("--tag") + 1], TAG)
        self.assertEqual(command[command.index("--iidfile") + 1], str(self.output / "image-id.txt"))
        self.assertEqual(command[-1], str(self.output / "image"))
        build_args = [command[index + 1] for index, part in enumerate(command) if part == "--build-arg"]
        self.assertEqual(build_args, ["BASE_IMAGE=" + DEBIAN, "SOURCE_DATE_EPOCH=" + str(EPOCH)])
        self.assertEqual(command[2:7], ["--platform", "linux/amd64", "--pull=false", "--network", "none"])
        self.assertEqual(self.docker.removed, [])
        self.assertEqual([argv for image, argv, _, _ in self.native[:4]], [
            self.native[0][1][:2] + ["extractor", *sorted(f"core/file{index}.go" for index in range(8))],
            [build.GO, "version"], build.build_arguments(COMMIT), [build.GO, "version", "-m", "/work/generated/geth"]])
        self.assertEqual([(image, argv) for image, argv, _, _ in self.native[4:]],
                         [(TAG, ["version"]), (TAG, ["--chapel", *build.PROFILE, "--datadir", "/work/dumpconfig",
                                                     "dumpconfig"])])
        self.assertTrue(all(image == GOLANG for image, _, _, _ in self.native[:4]))
        self.assertTrue(all(entry is None for _, _, _, entry in self.native))
        record = json.loads((self.output / "build.json").read_text())
        self.assertEqual((record["result"], record["source_commit"], record["cleanup_complete"]), ("PASS", "b" * 40, True))
        self.assertEqual(len(record["commands"]), 6 + 5)  # six owned commands and five host records
        self.assertEqual(set(record["commands"][0]), {"request", "result", "stdout", "stderr"})
        self.assertEqual(json.loads(Path(record["commands"][0]["request"]["path"]).read_text())["argv"][:4],
                         ["docker", "image", "inspect", DEBIAN])

    def test_a_failed_check_removes_the_tag_and_keeps_the_fail(self):
        with mock.patch.object(FakeNative, "version", VERSION.replace(COMMIT, "0" * 40)):
            proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual(proof["result"], "FAIL")
        self.assertIn("Version output differs", proof["error"])
        self.assertEqual(self.docker.removed, [TAG])
        self.assertFalse((self.output / "provenance.json").exists())
        self.assertEqual(json.loads((self.output / "build.json").read_text())["result"], "FAIL")

    def test_a_failed_image_check_removes_the_tag(self):
        def empty(docker, argv, **kwargs):
            if argv[:3] == ["docker", "image", "inspect"] and argv[3] == TAG:
                return done(argv, 0, b"[]")
            return FakeDocker.answer(docker, argv, **kwargs)
        with mock.patch.object(FakeDocker, "__call__", empty):
            proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual((proof["result"], self.docker.removed), ("FAIL", [TAG]))
        self.assertIn("exactly one image", proof["error"])

    def test_refusals_leave_no_output(self):
        with self.assertRaises(Rejected):
            build.build(self.accepted / "manifest.json", "0" * 64, self.output)
        with self.assertRaises(Rejected):
            build.build(self.accepted / "manifest.json", self.sha, self.accepted / "new")
        self.assertFalse((self.accepted / "new").exists())
        self.assertFalse(self.output.exists())
        self.assertEqual(self.docker.calls, [])


class SmokeTest(ProcedureFixture):
    def setUp(self):
        super().setUp()
        FakeNative.control = {"start": (0, START_LOG), **REFUSALS}
        FakeNative.version = VERSION

    def test_smoke_passes(self):
        proof = build.smoke(TAG, self.output, 20)
        self.assertEqual((proof["result"], proof["error"]), ("PASS", None))
        self.assertEqual(proof["start"]["exit_code"], 0)
        self.assertEqual({name: row["exit_code"] for name, row in proof["controls"].items()},
                         {"snap-sync": 1, "foreign-store": 1})
        self.assertEqual(proof["config"]["store_id"], build.SMOKE_STORE)
        self.assertEqual(proof["config"]["native_commit"], COMMIT)
        self.assertTrue((self.output / "smoke.json").is_file())
        foreign = json.loads((self.output / "smoke" / "config" / "foreign.json").read_text())
        self.assertEqual(foreign["store_id"], build.FOREIGN_STORE)
        self.assertEqual(json.loads(Path(proof["genesis"]["path"]).read_text())["config"]["chainId"], 97)
        home = ["--chapel", "--datadir", "/home"]
        self.assertEqual([argv for image, argv, _, _ in self.native], [
            ["--chapel", "dumpgenesis"], [*home, "--state.scheme", "hash", "init", "/config/genesis.json"],
            ["-c", build.start_script([*home, *build.PROFILE, *build.OFFLINE], "start", 20)],
            ["-c", build.start_script([*home, "--state.scheme", "hash", "--gcmode", "full", *build.OFFLINE], "snap-sync")],
            ["-c", build.start_script([*home, *build.PROFILE, *build.OFFLINE], "foreign-store")]])
        self.assertTrue(all(argv[0] == "-c" for argv in
                            [row[1] for row in self.native if row[3] == "/bin/sh"]))
        self.assertEqual([row[2] for row in self.native],
                         [None, None, {"EXTRACTOR_BSC_CONFIG": "/config/capture.json"},
                          {"EXTRACTOR_BSC_CONFIG": "/config/capture.json"},
                          {"EXTRACTOR_BSC_CONFIG": "/config/foreign.json"}])
        self.assertTrue(all(image == TAG for image, _, _, _ in self.native))
        self.assertEqual([row[3] for row in self.native], [None, None, "/bin/sh", "/bin/sh", "/bin/sh"])

    def test_a_control_that_passes_fails_the_smoke(self):
        FakeNative.control = {**FakeNative.control, "snap-sync": (0, START_LOG)}
        proof = build.smoke(TAG, self.output, 20)
        self.assertEqual(proof["result"], "FAIL")
        self.assertIn("passed", proof["error"])

    def test_a_control_refused_by_another_rule_fails_the_smoke(self):
        FakeNative.control = {**FakeNative.control, "foreign-store": (1, "Fatal: genesis does not build the home's genesis block")}
        proof = build.smoke(TAG, self.output, 20)
        self.assertEqual(proof["result"], "FAIL")
        self.assertIn("does not name", proof["error"])

    def test_an_image_without_the_labels_is_refused(self):
        for labels in ({}, {**build.image_labels(facts()), "io.extractor.source-commit": "short"},
                       {**build.image_labels(facts()), "io.extractor.patches": "01-core-import-seam.diff=" + "0" * 64}):
            with self.subTest(labels=list(labels)[:2]):
                self.docker.labels = labels
                proof = build.smoke(TAG, self.output, 20)
                self.assertEqual(proof["result"], "FAIL")
                self.assertIn("not a capture build", proof["error"])
                FakeNative.instances[-1].cleanup_complete = True
                self.output = self.root / ("out-" + str(len(FakeNative.instances)))
        self.assertFalse(self.native)

    def test_arguments_are_checked(self):
        for image, seconds in (("latest", 20), ("a/b:tag", 5), ("a/b:tag", 121), ("a/b:tag", "20"), ("A/B:tag", 20)):
            with self.subTest(image=image, seconds=seconds), self.assertRaises(Rejected):
                build.smoke(image, self.root / "none", seconds)
        self.assertFalse((self.root / "none").exists())


if __name__ == "__main__":
    unittest.main()
