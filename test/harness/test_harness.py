"""Behavioral tests at the local harness CLI boundary."""

import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import os
import time
import signal


HERE = Path(__file__).resolve().parent


def write_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True) + "\n")


def ref(path):
    data = path.read_bytes()
    return {"path": str(path), "sha256": hashlib.sha256(data).hexdigest(),
            "size_bytes": len(data)}


def admitted_fixture(root):
    """Synthetic admission inputs only; these never represent a native run."""
    binary = root / "geth"
    binary.write_bytes(b"portable admission binary")
    binary_ref = ref(binary)
    binary_ref["path"] = "geth"
    manifest = root / "manifest.json"
    write_json(manifest, {"schema_version": 1, "evidence_kind": "actual-build", "task_id": "E01-01.builds",
                         "builds": {"geth": {"binary": binary_ref, "source_commit": "1" * 40}},
                         "image_inputs": {"debian-runtime-image": {"reference": "docker.io/library/debian@sha256:" + "2" * 64}},
                         "profile_bindings": [{"id": "ethereum-mainnet", "native_component": "geth",
                                               "native_binary_sha256": binary_ref["sha256"]}]})
    fixture = {"schema_version": 1, "prepared_blocks": []}
    for field, source in (("genesis", "genesis.json"), ("expected", "expected.json"), ("generator", "generate.go")):
        path = root / source
        path.write_bytes((HERE / "fixtures" / source).read_bytes())
        fixture[field] = ref(path)
    for field in ("blocks", "full_chain"):
        path = root / field
        path.write_bytes(b"not native RLP; admission fixture only")
        fixture[field] = ref(path)
    config = json.loads((root / "genesis.json").read_text())["config"]
    fixture["identity"] = {"kind": "private-local", "chain_id": 31337, "genesis_hash": "0x" + "0" * 64,
                           "config_sha256": hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}
    for row in json.loads((root / "expected.json").read_text())["blocks"]:
        fixture["prepared_blocks"].append({key: row[key] for key in ("number", "gas_used", "transactions", "base_fee")})
        fixture["prepared_blocks"][-1].update(hash="0x" + "1" * 64, state_root="0x" + "2" * 64)
    write_json(root / "fixture.json", fixture)
    request = {"schema_version": 1, "build_manifest": ref(manifest), "profile_association": "ethereum-mainnet",
               "fixture": ref(root / "fixture.json"), "deadline_seconds": 300}
    write_json(root / "request.json", request)
    return request, fixture


class AdmissionTests(unittest.TestCase):
    def cli(self, root):
        return subprocess.run([sys.executable, "-I", str(HERE / "run.py"), "check", str(root / "request.json")],
                              capture_output=True, text=True, timeout=10)

    def test_complete_admission_has_no_native_side_effect(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            admitted_fixture(root)
            before = set(root.iterdir())
            result = self.cli(root)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(json.loads(result.stdout), {"valid": True, "native_dispatched": False})
            self.assertEqual(set(root.iterdir()), before)

    def test_raw_genesis_rlp_oracle_and_manifest_tampering_rejects(self):
        for filename in ("genesis.json", "blocks", "full_chain", "expected.json", "manifest.json"):
            with self.subTest(filename=filename), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                admitted_fixture(root)
                with (root / filename).open("ab") as stream:
                    stream.write(b"changed")
                result = self.cli(root)
                self.assertEqual(result.returncode, 2)
                self.assertIn("Artifact bytes changed", result.stdout)

    def test_closed_request_and_deadline_types_reject(self):
        for field, value in (("schema_version", True), ("deadline_seconds", True), ("deadline_seconds", 1801),
                             ("deadline_seconds", 0), ("profile_association", "ethereum-sepolia"), ("extra", 1)):
            with self.subTest(field=field, value=value), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                request, _ = admitted_fixture(root)
                request[field] = value
                write_json(root / "request.json", request)
                self.assertEqual(self.cli(root).returncode, 2)

    def test_private_identity_and_complete_block_shape_are_required(self):
        for change in ("chain", "config", "block", "missing", "unknown"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                request, fixture = admitted_fixture(root)
                if change == "chain": fixture["identity"]["chain_id"] = 1
                if change == "config": fixture["identity"]["config_sha256"] = "0" * 64
                if change == "block": fixture["prepared_blocks"][1]["number"] = 1
                if change == "missing": fixture["prepared_blocks"].pop()
                if change == "unknown": fixture["identity"]["public_finalized"] = True
                write_json(root / "fixture.json", fixture)
                request["fixture"] = ref(root / "fixture.json")
                write_json(root / "request.json", request)
                self.assertEqual(self.cli(root).returncode, 2)

    def test_symlinked_artifact_rejects_even_with_matching_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            request, fixture = admitted_fixture(root)
            (root / "alias").symlink_to(root / "blocks")
            fixture["blocks"]["path"] = str(root / "alias")
            write_json(root / "fixture.json", fixture)
            request["fixture"] = ref(root / "fixture.json")
            write_json(root / "request.json", request)
            result = self.cli(root)
            self.assertEqual(result.returncode, 2)
            self.assertIn("Symlink", result.stdout)

    def test_changed_binary_rejects_without_native_dispatch_or_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            binary = root / "geth"
            binary.write_bytes(b"admitted binary")
            manifest = root / "manifest.json"
            build_ref = ref(binary)
            build_ref["path"] = "geth"
            write_json(manifest, {
                "schema_version": 1, "evidence_kind": "actual-build",
                "task_id": "E01-01.builds",
                "builds": {"geth": {"binary": build_ref, "source_commit": "1" * 40}},
                "image_inputs": {"debian-runtime-image": {
                    "reference": "docker.io/library/debian@sha256:" + "2" * 64}},
                "profile_bindings": [{"id": "ethereum-mainnet", "native_component": "geth",
                                      "native_binary_sha256": build_ref["sha256"]}],
            })
            request = root / "request.json"
            write_json(request, {"schema_version": 1, "build_manifest": ref(manifest),
                                 "profile_association": "ethereum-mainnet",
                                 "fixture": {}, "deadline_seconds": 120})
            binary.write_bytes(b"changed binary")
            output = root / "output"
            result = subprocess.run([sys.executable, "-I", str(HERE / "run.py"), "run",
                                     str(request), "--output", str(output)],
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertIn("Artifact bytes changed", result.stdout)
            self.assertFalse(output.exists())


class OwnershipTests(unittest.TestCase):
    def test_exited_leader_does_not_leave_a_live_descendant(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            docker = root / "docker"
            docker.write_text("#!/usr/bin/env python3\nimport os,sys,time\nfrom pathlib import Path\n"
                              "if sys.argv[1]=='run':\n"
                              " child=os.fork()\n"
                              " if child==0:\n"
                              "  Path(__file__).with_name('child.pid').write_text(str(os.getpid()))\n"
                              "  time.sleep(30)\n"
                              " else: os._exit(0)\n")
            docker.chmod(0o700)
            request = root / "command.json"
            write_json(request, {"argv": [str(docker), "run"], "docker": str(docker), "container": "owned-test",
                                 "deadline": time.monotonic() + 2, "output": str(root / "command")})
            read_fd, write_fd = os.pipe()
            child_pid = None
            try:
                result = subprocess.run([sys.executable, "-I", str(HERE / "owned.py"), str(request), str(read_fd)],
                                        pass_fds=(read_fd,), capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 2, result.stderr)
                child_pid = int((root / "child.pid").read_text())
                with self.assertRaises(ProcessLookupError):
                    os.kill(child_pid, 0)
                proof = json.loads((root / "command.result.json").read_text())
                self.assertTrue(proof["cleanup_complete"])
                self.assertTrue(proof["process_group_absent"])
            finally:
                os.close(read_fd)
                os.close(write_fd)
                if child_pid is not None:
                    check = subprocess.run(["ps", "-p", str(child_pid), "-o", "command="], capture_output=True, text=True)
                    if str(docker) in check.stdout:
                        os.kill(child_pid, signal.SIGKILL)

    def test_uncertain_cleanup_fences_the_next_native_command(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            inputs = root / "inputs"
            inputs.mkdir()
            admitted_fixture(inputs)
            docker = root / "docker"
            docker.write_text("#!/usr/bin/env python3\nimport sys\n"
                              "if sys.argv[1:3]==['container','ls']: print('still-owned')\n")
            docker.chmod(0o700)
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"])
            result = subprocess.run([sys.executable, "-I", str(HERE / "run.py"), "run", str(inputs / "request.json"),
                                     "--output", str(root / "output")], env=env,
                                    capture_output=True, text=True, timeout=15)
            self.assertEqual(result.returncode, 2, result.stderr)
            proof = json.loads((root / "output/proof.json").read_text())
            self.assertFalse(proof["cleanup_complete"])
            self.assertEqual(proof["result"], "FAIL")
            self.assertEqual(len(proof["commands"]), 1)

    def test_parent_loss_and_signals_cleanup_without_further_dispatch(self):
        for mode in ("parent", signal.SIGTERM, signal.SIGINT):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                docker = root / "docker"
                docker.write_text("#!/usr/bin/env python3\nimport sys,time\nfrom pathlib import Path\n"
                                  "if sys.argv[1]=='run':\n Path(__file__).with_name('started').touch()\n time.sleep(30)\n")
                docker.chmod(0o700)
                request = root / "command.json"
                write_json(request, {"argv": [str(docker), "run"], "docker": str(docker), "container": "owned-test",
                                     "deadline": time.monotonic() + 10, "output": str(root / "command")})
                read_fd, write_fd = os.pipe()
                process = subprocess.Popen([sys.executable, "-I", str(HERE / "owned.py"), str(request), str(read_fd)],
                                           pass_fds=(read_fd,), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                os.close(read_fd)
                try:
                    deadline = time.monotonic() + 5
                    while not (root / "started").exists() and time.monotonic() < deadline:
                        time.sleep(0.01)
                    self.assertTrue((root / "started").exists())
                    if mode == "parent":
                        os.close(write_fd)
                        write_fd = None
                    else:
                        process.send_signal(mode)
                    stdout, stderr = process.communicate(timeout=10)
                    self.assertEqual(process.returncode, 2, stderr)
                    result = json.loads((root / "command.result.json").read_text())
                    self.assertEqual(result["reason"], "PARENT_EXIT" if mode == "parent" else "INTERRUPTED")
                    self.assertTrue(result["cleanup_complete"])
                finally:
                    if write_fd is not None: os.close(write_fd)
                    if process.poll() is None:
                        process.terminate()
                        process.communicate(timeout=10)

    def test_native_timeout_is_bounded_and_removes_only_its_container(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            docker = root / "docker"
            docker.write_text("#!/usr/bin/env python3\nimport sys,time\nfrom pathlib import Path\n"
                              "p=Path(__file__).with_name('calls')\n"
                              "with p.open('a') as f: f.write(' '.join(sys.argv[1:])+'\\n')\n"
                              "if sys.argv[1]=='run': time.sleep(30)\n")
            docker.chmod(0o700)
            request = root / "command.json"
            write_json(request, {"argv": [str(docker), "run", "--name", "test-owned"],
                                 "docker": str(docker), "container": "test-owned",
                                 "deadline": time.monotonic() + 0.2, "output": str(root / "command")})
            read_fd, write_fd = os.pipe()
            try:
                result = subprocess.run([sys.executable, "-I", str(HERE / "owned.py"),
                                         str(request), str(read_fd)], pass_fds=(read_fd,),
                                        capture_output=True, text=True, timeout=15)
            finally:
                os.close(read_fd)
                os.close(write_fd)
            self.assertEqual(result.returncode, 2, result.stderr)
            proof = json.loads((root / "command.result.json").read_text())
            self.assertEqual(proof["reason"], "DEADLINE")
            self.assertTrue(proof["cleanup_complete"])
            self.assertIn("rm -f test-owned", (root / "calls").read_text())


class ComparisonTests(unittest.TestCase):
    def test_native_export_prefix_cannot_pass_with_matching_account_values(self):
        sys.path.insert(0, str(HERE))
        try:
            from artifacts import admit
            from compare import compare
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                admitted_fixture(root)
                admitted = admit(root / "request.json")
                (root / "partial-export").write_bytes((root / "full_chain").read_bytes()[:-1])
                with self.assertRaisesRegex(ValueError, "Complete native chain"):
                    compare(admitted, [], root / "partial-export")
        finally:
            sys.path.pop(0)

    def test_wrong_expected_balance_is_detected(self):
        sys.path.insert(0, str(HERE))
        try:
            from compare import compare_state
            observed = {"root": "11" * 32, "accounts": {
                "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf": {"balance": "8999958000000000000", "nonce": 1},
                "0x1000000000000000000000000000000000000000": {"balance": "1000000000000000000", "nonce": 0},
                "0x2000000000000000000000000000000000000000": {"balance": "2000023625000000000", "nonce": 0},
            }}
            expected = json.loads((HERE / "fixtures/expected.json").read_text())["blocks"][0]
            compare_state(observed, expected, "0x" + "11" * 32)
            expected["accounts"]["1000000000000000000000000000000000000000"]["balance"] = "999"
            with self.assertRaisesRegex(ValueError, "Account state mismatch"):
                compare_state(observed, expected, "0x" + "11" * 32)
            expected["accounts"]["1000000000000000000000000000000000000000"]["balance"] = "1000000000000000000"
            del observed["accounts"]["0x1000000000000000000000000000000000000000"]
            with self.assertRaisesRegex(ValueError, "account set"):
                compare_state(observed, expected, "0x" + "11" * 32)
        finally:
            sys.path.pop(0)


if __name__ == "__main__":
    unittest.main()
