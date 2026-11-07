"""Run disposable candidate processes against protected inputs and artifacts."""

import shutil
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

import build_contract as contract
from build_product_commands import Commands
import build_task_verifier as verifier
import build_vectors


SOURCE = Path(__file__).resolve().parents[2]


class CommandBoundaryTests(unittest.TestCase):
    def commands(self, root):
        candidate = Path(root) / "candidate"
        candidate.mkdir()
        registry, _, _, _ = contract.inputs(SOURCE)
        for name in registry["external_lock_paths"]:
            path = candidate / name
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(SOURCE / name, path)
        return Commands(candidate, SOURCE, Path(root) / "controls", time.time() + 30)

    def test_external_locks_reject_identical_byte_rewrites(self):
        registry, _, _, _ = contract.inputs(SOURCE)
        for name in registry["external_lock_paths"]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                commands.call("unchanged", [sys.executable, "-I", "-c", "pass"])
                code = "from pathlib import Path; import sys; p=Path(sys.argv[1]); p.write_bytes(p.read_bytes())"
                with self.assertRaisesRegex(ValueError, "mutated an active input"):
                    commands.call("rewrite", [sys.executable, "-I", "-c", code, name])

    def test_candidate_descendants_are_stopped_on_exit_and_timeout(self):
        for timeout in (False, True):
            with self.subTest(timeout=timeout), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                marker = Path(root) / "late-write"
                child = "import time; from pathlib import Path; time.sleep(0.5); Path(" + repr(str(marker)) + ").write_text('changed')"
                parent = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-I','-c',sys.argv[1]]); "
                parent += "time.sleep(5)" if timeout else "pass"
                argv = [sys.executable, "-I", "-c", parent, child]
                if timeout:
                    with self.assertRaises(subprocess.TimeoutExpired):
                        commands.call("descendant", argv, timeout=0.1)
                else:
                    commands.call("descendant", argv)
                self.assertTrue(commands.outcomes[-1]["cleanup_confirmed"])
                time.sleep(0.6)
                self.assertFalse(marker.exists())

    def test_final_preflight_rejects_artifact_changes_after_candidate_success(self):
        for mutate in (False, True):
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                artifacts = Path(root) / "artifacts"
                manifest = build_vectors.fixture(artifacts, SOURCE)
                contract.validate_manifest(manifest, artifacts, SOURCE, allow_synthetic=True)
                script = commands.candidate / "build/native/verify.py"
                script.parent.mkdir(parents=True)
                script.write_text("import json,sys\nfrom pathlib import Path\nr=json.load(sys.stdin)\n"
                                  "m=json.loads((Path(r['root'])/r['manifest_path']).read_text())\n"
                                  + ("(Path(r['root'])/m['builds']['geth']['binary']['path']).write_bytes(b'changed after validation')\n" if mutate else "")
                                  + "print(json.dumps({'valid':True,'reason':'Validated'}))\n")
                # Only native semantics are replaced for this synthetic fixture;
                # actual reference hashing and candidate process execution remain.
                def inspect(value, directory, source, now):
                    return contract.validate_manifest(value, directory, source, allow_synthetic=True)
                with mock.patch.object(verifier.build_materials, "validate_materialized", side_effect=inspect) as final_check:
                    if mutate:
                        with self.assertRaisesRegex(ValueError, "artifact bytes differ"):
                            verifier.final_preflights(commands, manifest, artifacts, SOURCE)
                    else:
                        verifier.final_preflights(commands, manifest, artifacts, SOURCE)
                    self.assertEqual(final_check.call_count, 1)
                    self.assertEqual(len(commands.outcomes), len(manifest["plan"]["profiles"]))


if __name__ == "__main__":
    unittest.main()
