"""Portable public admission controls; synthetic authority never proves a build."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import Rejected
from bor_inputs import admit, canonical_digest, revalidate_authority
from test_bsc_inputs import fixture as base_fixture
from test_harness import ref, write_json

HERE = Path(__file__).resolve().parent
VALIDATOR = "0x71562b71999873db5b286df957af199ec94617f7"


def fixture(root):
    tool, scenario, request = base_fixture(root)
    manifest = json.loads((root / "manifest.json").read_text())
    manifest["builds"]["bor"] = manifest["builds"].pop("bsc")
    manifest["module_closures"]["bor"] = manifest["module_closures"].pop("bsc")
    manifest["profile_bindings"][0].update(id="polygon-pos-mainnet", native_component="bor")
    write_json(root / "manifest.json", manifest)
    (root / "helper.go").write_bytes((HERE / "fixtures/bor/helper.go").read_bytes())
    tool.update(kind="bor-native-observer", build_manifest=ref(root / "manifest.json"),
                source=ref(root / "helper.go"), public_validator=VALIDATOR,
                module_closure_sha256=canonical_digest(manifest["module_closures"]["bor"]))
    for index, row in enumerate(tool["commands"]):
        for field in ("request", "result"):
            path = Path(row[field]["path"])
            content = json.loads(path.read_text())
            content["argv"] = [part.replace("bsc-helper", "bor-helper").replace("validator-keystore.json", "validator-source.go")
                               for part in content["argv"]]
            write_json(path, content)
            row[field] = ref(path)
        if index == 2:
            path = Path(row["stdout"]["path"]); path.write_text(VALIDATOR + "\n"); row["stdout"] = ref(path)
    write_json(root / "tool.json", tool)
    genesis = {"config": {"chainId": 15001, "londonBlock": 1, "bor": {"jaipurBlock": 2}}, "alloc": {}}
    write_json(root / "genesis.json", genesis)
    (root / "expected.json").write_bytes((HERE / "fixtures/bor/expected.json").read_bytes())
    scenario.update(kind="bor-private", native_config=genesis["config"], genesis=ref(root / "genesis.json"), expected=ref(root / "expected.json"))
    scenario["identity"].update(chain_id=15001, config_sha256=canonical_digest(genesis["config"]))
    for n, block in enumerate(scenario["prepared_blocks"]):
        block["parent_hash"] = scenario["identity"]["genesis_hash"] if n == 0 else scenario["prepared_blocks"][0]["hash"]
    write_json(root / "fixture.json", scenario)
    request.update(kind="bor-native-import", profile_association="polygon-pos-mainnet",
                   build_manifest=ref(root / "manifest.json"), fixture=ref(root / "fixture.json"))
    write_json(root / "request.json", request)
    return tool, scenario, request


class BorAdmissionTests(unittest.TestCase):
    def cli(self, root, pin, mode="check"):
        return subprocess.run([sys.executable, "-I", str(HERE / "bor_run.py"), mode, str(root / "request.json"),
                               "--observer-tool", str(root / "tool.json"), "--observer-tool-sha256", pin,
                               "--output", str(root / "output")], capture_output=True, text=True, timeout=15)

    def test_public_synthetic_admission_does_not_dispatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); fixture(root)
            result = self.cli(root, ref(root / "tool.json")["sha256"])
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(json.loads(result.stdout), {"valid": True, "native_dispatched": False})
            self.assertFalse((root / "output").exists())

    def test_rehashed_fake_tool_cannot_replace_external_authority(self):
        for mode in ("check", "run"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve(); tool, _, _ = fixture(root)
                pin = ref(root / "tool.json")["sha256"]
                (root / "fake").write_bytes(b"emit perfect fake observations")
                tool["binary"] = ref(root / "fake"); write_json(root / "tool.json", tool)
                result = self.cli(root, pin, mode)
                self.assertEqual(result.returncode, 2)
                self.assertFalse((root / "output").exists())

    def test_command_provenance_and_private_header_chain_fail_closed(self):
        for mutation in ("compiler-command", "public-key", "parent", "observer-override"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve(); tool, scenario, request = fixture(root)
                if mutation == "compiler-command":
                    row = tool["commands"][1]
                    for key in ("request", "result"):
                        path = Path(row[key]["path"]); value = json.loads(path.read_text())
                        value["argv"][-1] = "/work/unreviewed.go"; write_json(path, value); row[key] = ref(path)
                elif mutation == "public-key": tool["public_validator"] = "0x" + "0" * 40
                elif mutation == "parent": scenario["prepared_blocks"][1]["parent_hash"] = "0x" + "0" * 64
                else: scenario["observer"] = tool["binary"]
                write_json(root / "tool.json", tool); write_json(root / "fixture.json", scenario)
                request["fixture"] = ref(root / "fixture.json"); write_json(root / "request.json", request)
                result = self.cli(root, ref(root / "tool.json")["sha256"], "run")
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertFalse((root / "output").exists())

    def test_final_authority_rejects_post_admission_manifest_change(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); fixture(root)
            value = admit(root / "request.json", root / "tool.json", ref(root / "tool.json")["sha256"])
            revalidate_authority(value)
            path = root / "tool.json"; path.write_bytes(path.read_bytes() + b" ")
            with self.assertRaises(Rejected): revalidate_authority(value)


if __name__ == "__main__":
    unittest.main()
