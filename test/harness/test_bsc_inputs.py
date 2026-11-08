"""Portable controls at the BSC CLI admission boundary; no native execution."""

import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
from bsc_inputs import canonical_digest
import bsc_inputs
import artifacts
import bsc_run
import test_bsc
from test_harness import ref, write_json

HERE = Path(__file__).resolve().parent


def fixture(root):
    for name in ("bsc", "source.tar", "observer", "go.mod", "go.sum", "blocks", "full_chain"):
        (root / name).write_bytes(("synthetic admission only: " + name).encode())
    (root / "helper.go").write_bytes((HERE / "fixtures/bsc/helper.go").read_bytes())
    (root / "expected.json").write_bytes((HERE / "fixtures/bsc/expected.json").read_bytes())
    build_ref = ref(root / "bsc"); build_ref["path"] = "bsc"
    archive = ref(root / "source.tar"); archive["path"] = "source.tar"
    closure = {"source_commit": "1" * 40, "source_archive": archive, "artifacts": []}
    compiler = "docker.io/library/golang@sha256:" + "3" * 64
    manifest = {"evidence_kind": "actual-build", "task_id": "E01-01.builds", "builds": {
        "bsc": {"binary": build_ref, "source_commit": "1" * 40}}, "module_closures": {"bsc": closure},
        "image_inputs": {"debian-runtime-image": {"reference": "docker.io/library/debian@sha256:" + "2" * 64},
                         "golang-build-image": {"reference": compiler}},
        "profile_bindings": [{"id": "bsc-mainnet", "native_component": "bsc", "native_binary_sha256": build_ref["sha256"]}]}
    write_json(root / "manifest.json", manifest)
    commands = []
    for index, tail in enumerate((
        ["/usr/local/go/bin/go", "mod", "verify"],
        ["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath", "-o", "/work/generated/bsc-helper", "/work/helper.go"],
        ["/work/generated/bsc-helper", "verify-key", "/work/validator-keystore.json"],
    )):
        argv = ["/docker", "run", "--network", "none", "--platform", "linux/amd64", "--pull", "never", "--read-only"]
        if index < 2:
            for value in ("CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64", "GOPROXY=off", "GOSUMDB=off",
                          "GOFLAGS=-mod=readonly -modfile=/work/metadata/go.mod", "GOMODCACHE=/modules",
                          "GOTMPDIR=/work/build-temp", "TMPDIR=/work/build-temp"):
                argv += ["--env", value]
            argv += ["--workdir", "/source"]
        argv += [compiler, *tail]
        write_json(root / f"{index}.request", {"argv": argv})
        write_json(root / f"{index}.result", {"argv": argv, "exit_code": 0, "reason": None, "cleanup_complete": True})
        write_json(root / f"{index}.cleanup", {"absent": True})
        (root / f"{index}.stdout").write_text("0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9\n" if index == 2 else "")
        (root / f"{index}.stderr").write_bytes(b"")
        commands.append({key: ref(root / f"{index}.{key}") for key in ("request", "result", "cleanup", "stdout", "stderr")})
    tool = {"schema_version": 1, "kind": "bsc-native-observer", "build_manifest": ref(root / "manifest.json"),
            "source_commit": "1" * 40, "source_archive": ref(root / "source.tar"), "module_closure_sha256": canonical_digest(closure),
            "compiler": compiler, "source": ref(root / "helper.go"), "binary": ref(root / "observer"),
            "public_validator": "0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9", "commands": commands,
            "metadata": [ref(root / name) for name in ("go.mod", "go.sum")]}
    write_json(root / "tool.json", tool)
    genesis = {"config": {"chainId": 714}}
    write_json(root / "genesis.json", genesis)
    scenario = {"schema_version": 1, "kind": "bsc-parlia", "identity": {"kind": "private-local", "chain_id": 714,
                "genesis_hash": "0x" + "a" * 64, "config_sha256": canonical_digest(genesis["config"])},
                "native_config": genesis["config"], "prepared_blocks": [{"number": n, "hash": "0x" + str(n) * 64,
                "state_root": "0x" + "b" * 64, "timestamp": n * 3, "transactions": 10 if n == 1 else 0, "gas_used": 30000 if n == 1 else 0} for n in (1, 2)]}
    for name in ("genesis", "expected", "blocks", "full_chain"):
        scenario[name] = ref(root / (name + ".json" if name in ("genesis", "expected") else name))
    write_json(root / "fixture.json", scenario)
    request = {"schema_version": 1, "kind": "bsc-native-import", "build_manifest": ref(root / "manifest.json"),
               "profile_association": "bsc-mainnet", "fixture": ref(root / "fixture.json"), "deadline_seconds": 300}
    write_json(root / "request.json", request)
    return tool, scenario, request


class BscInputTests(unittest.TestCase):
    def cli(self, root, pin, mode="check"):
        return subprocess.run([sys.executable, "-I", str(HERE / "bsc_run.py"), mode, str(root / "request.json"),
                               "--observer-tool", str(root / "tool.json"), "--observer-tool-sha256", pin,
                               "--output", str(root / "output")], capture_output=True, text=True, timeout=10)

    def test_synthetic_admission_dispatches_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); fixture(root)
            result = self.cli(root, ref(root / "tool.json")["sha256"])
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(json.loads(result.stdout), {"valid": True, "native_dispatched": False})
            self.assertFalse((root / "output").exists())

    def test_rehashed_fake_observer_rejects_at_original_external_pin(self):
        for mode in ("check", "run"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve(); tool, scenario, request = fixture(root)
                pin = ref(root / "tool.json")["sha256"]
                (root / "observer").write_text("fake observer returning expected values")
                tool["binary"] = ref(root / "observer"); write_json(root / "tool.json", tool)
                write_json(root / "fixture.json", scenario)
                request["fixture"] = ref(root / "fixture.json"); write_json(root / "request.json", request)
                result = self.cli(root, pin, mode)
                self.assertEqual(result.returncode, 2)
                self.assertIn("external trusted pin", result.stdout)
                self.assertFalse((root / "output").exists())

    def test_fixture_cannot_override_observer(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); _, scenario, request = fixture(root)
            pin = ref(root / "tool.json")["sha256"]
            scenario["observer"] = ref(root / "observer"); write_json(root / "fixture.json", scenario)
            request["fixture"] = ref(root / "fixture.json"); write_json(root / "request.json", request)
            result = self.cli(root, pin)
            self.assertEqual(result.returncode, 2)
            self.assertFalse((root / "output").exists())

    def test_referenced_observer_bytes_and_compile_provenance_reject(self):
        for mutation in ("binary", "source", "wrong-command", "different-result"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve(); tool, _, _ = fixture(root)
                if mutation in ("binary", "source"):
                    Path(tool[mutation]["path"]).write_bytes(b"changed")
                else:
                    key = "request" if mutation == "wrong-command" else "result"
                    path = Path(tool["commands"][1][key]["path"])
                    row = json.loads(path.read_text()); row["argv"][-1] = "/work/unreviewed.go"
                    write_json(path, row); tool["commands"][1][key] = ref(path)
                write_json(root / "tool.json", tool)
                result = self.cli(root, ref(root / "tool.json")["sha256"])
                self.assertEqual(result.returncode, 2, result.stdout)
                self.assertFalse((root / "output").exists())

    def test_tool_replacement_after_pinning_cannot_change_parsed_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); tool, _, _ = fixture(root)
            pin = ref(root / "tool.json")["sha256"]
            replacement = copy.deepcopy(tool)
            (root / "fake-observer").write_bytes(b"fake observer")
            replacement["binary"] = ref(root / "fake-observer")
            original = artifacts.reference
            def swap_after_reference(path, *args, **kwargs):
                result = original(path, *args, **kwargs)
                if Path(path) == root / "tool.json":
                    write_json(path, replacement)
                return result
            with patch.object(bsc_inputs, "reference", side_effect=swap_after_reference):
                admitted = bsc_inputs.admit(root / "request.json", root / "tool.json", pin)
            self.assertEqual(admitted["tool"]["binary"], tool["binary"])

    def test_tool_replacement_after_buffer_verification_parses_original_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); tool, _, _ = fixture(root)
            pin = ref(root / "tool.json")["sha256"]
            replacement = copy.deepcopy(tool)
            (root / "fake-observer").write_bytes(b"fake observer")
            replacement["binary"] = ref(root / "fake-observer")
            decode = artifacts.decode_json
            swaps = []
            def replace_before_parse(data, deadline=None):
                if b'"bsc-native-observer"' in data:
                    write_json(root / "tool.json", replacement)
                    swaps.append(True)
                return decode(data, deadline)
            with patch.object(artifacts, "decode_json", side_effect=replace_before_parse):
                admitted = bsc_inputs.admit(root / "request.json", root / "tool.json", pin)
            self.assertEqual(swaps, [True])
            self.assertEqual(admitted["tool"]["binary"], tool["binary"])
            self.assertNotEqual(ref(root / "tool.json")["sha256"], pin)

    def test_verified_json_rejects_replacements_and_malformed_json(self):
        for body in (b'{"value":1,"value":2}', b'{"value":NaN}'):
            with self.subTest(body=body), tempfile.TemporaryDirectory() as directory:
                path = Path(directory).resolve() / "input.json"
                path.write_bytes(body)
                with self.assertRaises(artifacts.Rejected):
                    artifacts.json_artifact(ref(path))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve() / "input.json"
            path.write_bytes(b'{"value":1}')
            original = ref(path)
            path.write_bytes(b'{"value":2}')
            with self.assertRaises(artifacts.Rejected):
                artifacts.json_artifact(original)
            path.write_bytes(b'{"value":1}')
            decode = artifacts.decode_json
            def replace_before_parse(data, deadline=None):
                path.write_bytes(b'{"value":2}')
                return decode(data, deadline)
            with patch.object(artifacts, "decode_json", side_effect=replace_before_parse):
                _, result = artifacts.json_artifact(original)
            self.assertEqual(result, {"value": 1})

    def test_measured_pass_cannot_retain_changed_authority_references(self):
        for changed_name in (None, "tool.json", "manifest.json", "fixture.json", "request.json"):
            with self.subTest(changed=changed_name), tempfile.TemporaryDirectory() as directory:
                base = Path(directory).resolve(); root = base / "inputs"; root.mkdir()
                _, scenario, request = fixture(root)
                shape = test_bsc.BscObservationTests(); shape.setUp()
                for n, block in enumerate(shape.observation["blocks"]):
                    scenario["prepared_blocks"][n].update({key: block[key] for key in ("hash", "state_root", "timestamp", "gas_used")})
                    scenario["prepared_blocks"][n]["transactions"] = len(block["transactions"])
                write_json(root / "fixture.json", scenario)
                request["fixture"] = ref(root / "fixture.json"); write_json(root / "request.json", request)
                pin = ref(root / "tool.json")["sha256"]
                class SimulatedNative:
                    def __init__(self, output, deadline):
                        self.output, self.commands, self.cleanup_complete, self.nonce = output, [], True, "synthetic-only"
                        (output / "logs").mkdir()
                    def command(self, image, argv, mounts, **kwargs):
                        index = len(self.commands)
                        stdout = self.output / "logs" / f"{index:03d}.stdout"
                        stderr = stdout.with_suffix(".stderr")
                        stdout.write_text(""); stderr.write_text("")
                        if "init" in argv:
                            for name in ("LOCK", "ancient/chain/FLOCK"):
                                lock = self.output / "state/data/geth/chaindata" / name
                                lock.parent.mkdir(parents=True, exist_ok=True)
                                lock.write_bytes(b"")
                        if "dump" in argv:
                            number = int(argv[-1])
                            write_json(stdout, shape.states[max(0, number - 1)])
                            write_json(stderr, {"msg": "State dump configured", "block": str(number), "hash": shape.identities[number]})
                        if "export" in argv:
                            (self.output / "state/export.rlp").write_bytes((root / "full_chain").read_bytes())
                        if "observe" in argv:
                            write_json(self.output / "observations/observation.json", shape.observation)
                            write_json(self.output / "observations/native-config.json", scenario["native_config"])
                            if changed_name:
                                with (root / changed_name).open("ab") as stream:
                                    stream.write(b" ")
                        self.commands.append({"synthetic": True})
                        return stdout
                with patch.object(bsc_run, "Native", SimulatedNative):
                    result = bsc_run.measured(root / "request.json", root / "tool.json", pin, base / "run", time.monotonic())
                self.assertEqual(result["result"], "PASS" if changed_name is None else "FAIL", result)
                if changed_name:
                    self.assertIn("Artifact bytes changed", result["error"])

    def test_observer_mounts_only_two_existing_empty_lock_files_writable(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            for name in ("inputs", "state", "observations"):
                (root / name).mkdir()
            locks = [root / "state/data/geth/chaindata" / name for name in ("LOCK", "ancient/chain/FLOCK")]
            for path in locks:
                path.parent.mkdir(parents=True, exist_ok=True); path.write_bytes(b"")
            mounts, refs = bsc_run.observer_mounts(root / "inputs", root / "state", root / "observations", time.monotonic() + 10)
            self.assertIn((root / "state", "/work", True), mounts)
            self.assertEqual([source for source, _, readonly in mounts if not readonly], [root / "observations", *locks])
            self.assertEqual([row["size_bytes"] for row in refs], [0, 0])
            locks[0].write_bytes(b"not a native empty lock")
            with self.assertRaises(artifacts.Rejected):
                bsc_run.observer_mounts(root / "inputs", root / "state", root / "observations", time.monotonic() + 10)
            locks[0].unlink(); locks[0].symlink_to(locks[1])
            with self.assertRaises(artifacts.Rejected):
                bsc_run.observer_mounts(root / "inputs", root / "state", root / "observations", time.monotonic() + 10)


if __name__ == "__main__":
    unittest.main()
