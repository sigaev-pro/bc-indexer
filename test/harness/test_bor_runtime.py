"""Synthetic checks of retained native runtime evidence, with no resources."""

import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import Rejected
from bor_inputs import admit
from bor_run import validate_runtime
import test_bor
from test_bor_inputs import fixture
from test_harness import ref, write_json

CONFIG = '''devfakeauthor = false
syncmode = "full"
gcmode = "archive"
[heimdall]
"bor.without" = true
[cache]
preimages = true
[pipeline]
enable-import-src = false
[parallelevm]
enable = false
[witness]
parallel-stateless-import = false
[relay]
enable-preconfs = false
[jsonrpc]
accept-preconf-tx = false
'''


def runtime_fixture(root):
    fixture(root)
    admitted = admit(root / "request.json", root / "tool.json", ref(root / "tool.json")["sha256"])
    sample = test_bor.BorObservationTests(); sample.setUp()
    admitted["fixture"]["identity"]["genesis_hash"] = sample.identities[0]
    admitted["fixture"]["prepared_blocks"] = sample.prepared
    sample.observed["config_sha256"] = admitted["fixture"]["identity"]["config_sha256"]
    runtime = root / "runtime"; runtime.mkdir()
    (runtime / "server-config.toml").write_text(CONFIG)
    args = ["/inputs/bor", "server", "--chain=/inputs/genesis.json", "--datadir=/work/runtime/data",
            "--ipcpath=/work/runtime/bor.ipc", "--ipcdisable=false", "--syncmode=full", "--gcmode=archive", "--cache=256",
            "--state.scheme=hash", "--db.engine=pebble", "--cache.preimages=true", "--bor.withoutheimdall=true",
            "--bor.devfakeauthor=false", "--mine=false", "--maxpeers=0", "--port=0", "--nodiscover=true", "--v5disc=false",
            "--http=false", "--ws=false", "--pipeline.enable-import-src=false", "--parallelevm.enable=false",
            "--witness.parallelstatelessimport=false", "--relay.enable-preconfs=false", "--accept-preconf-tx=false", "--log.json=true"]
    write_json(runtime / "server.argv.json", args)
    write_json(runtime / "readiness.json", {"head": 0, "genesis_hash": sample.identities[0],
               "node_info": {"protocols": {"eth": {"network": 15001, "config": admitted["fixture"]["native_config"]}}},
               "validators": [{"address": sample.expected["validator"]}]})
    write_json(runtime / "server-cleanup.json", {"wait_completed": True, "unexpected_exit": False,
                                                "forced_kill": False, "exit_code": 0, "pid": 123})
    (runtime / "full-chain.rlp").write_bytes((root / "full_chain").read_bytes())
    write_json(runtime / "observed.json", sample.observed)
    for n, state in enumerate(sample.states, 1):
        write_json(runtime / ("state-%d.json" % n), state)
    return admitted, runtime


class BorRuntimeEvidenceTests(unittest.TestCase):
    def test_complete_synthetic_runtime_record(self):
        with tempfile.TemporaryDirectory() as directory:
            admitted, runtime = runtime_fixture(Path(directory).resolve())
            validate_runtime(runtime, admitted, admitted["deadline"])

    def test_required_execution_settings_are_effectively_disabled(self):
        for field in ("devfakeauthor", "enable-import-src", "enable", "parallel-stateless-import", "enable-preconfs", "accept-preconf-tx"):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                admitted, runtime = runtime_fixture(Path(directory).resolve())
                (runtime / "server-config.toml").write_text(CONFIG.replace(field + " = false", field + " = true"))
                with self.assertRaises(Rejected): validate_runtime(runtime, admitted, admitted["deadline"])

    def test_forced_failed_or_unconfirmed_server_shutdown_cannot_pass(self):
        for field, value in (("exit_code", 1), ("exit_code", -1), ("exit_code", False),
                             ("forced_kill", True), ("unexpected_exit", True), ("wait_completed", False)):
            with self.subTest(field=field, value=value), tempfile.TemporaryDirectory() as directory:
                admitted, runtime = runtime_fixture(Path(directory).resolve())
                path = runtime / "server-cleanup.json"; record = json.loads(path.read_text())
                record[field] = value; write_json(path, record)
                with self.assertRaises(Rejected): validate_runtime(runtime, admitted, admitted["deadline"])

    def test_partial_dump_readiness_and_native_export_are_not_success(self):
        for mutation in ("continuation", "head", "network", "validator", "export", "launch"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                admitted, runtime = runtime_fixture(Path(directory).resolve())
                if mutation == "export": (runtime / "full-chain.rlp").write_bytes(b"valid prefix only")
                elif mutation == "continuation":
                    path = runtime / "state-1.json"; value = json.loads(path.read_text()); value["next"] = "0x01"; write_json(path, value)
                elif mutation == "launch":
                    path = runtime / "server.argv.json"; value = json.loads(path.read_text()); value[-1] = "--parallelevm.enable=true"; write_json(path, value)
                else:
                    path = runtime / "readiness.json"; value = json.loads(path.read_text())
                    if mutation == "head": value["head"] = 2
                    elif mutation == "network": value["node_info"]["protocols"]["eth"]["network"] = 137
                    else: value["validators"][0]["address"] = "0x" + "0" * 40
                    write_json(path, value)
                with self.assertRaises(Rejected): validate_runtime(runtime, admitted, admitted["deadline"])


if __name__ == "__main__":
    unittest.main()
