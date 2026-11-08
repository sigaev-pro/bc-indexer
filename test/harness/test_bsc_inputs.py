"""Portable controls at the BSC CLI admission boundary; no native execution."""

import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from bsc_inputs import canonical_digest
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
                          "GOFLAGS=-mod=readonly -modfile=/work/metadata/go.mod", "GOMODCACHE=/modules"):
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


if __name__ == "__main__":
    unittest.main()
