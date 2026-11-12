"""Portable controls for the shared E01-02.decision_bridge core; no Go toolchain needed."""

import contextlib
import fnmatch
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission
import decision

ROOT = decision.ROOT
PACKAGE = ROOT / "adapters" / "geth" / "decision"
SHARED = ["README.md", "decision.go", "decision.mutants.json", "decision_test.go", "vectors.json"]


class SharedCopies(unittest.TestCase):
    def copy_tree(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in decision.FAMILIES:
            for package in ("admission", "decision", "source_inputs"):
                shutil.copytree(ROOT / "adapters" / family / package, root / "adapters" / family / package)
        return root

    def test_repository_copies_are_identical(self):
        self.assertEqual(sorted(decision.check_identical()), SHARED)

    def test_changed_byte_is_refused(self):
        root = self.copy_tree()
        path = root / "adapters" / "bsc" / "decision" / "decision.go"
        data = bytearray(path.read_bytes())
        data[-2] ^= 1
        path.write_bytes(bytes(data))
        with self.assertRaisesRegex(ValueError, "shared file decision.go differs"):
            decision.check_identical(root)

    def test_missing_extra_and_native_files(self):
        root = self.copy_tree()
        (root / "adapters" / "bor" / "decision" / "README.md").unlink()
        with self.assertRaisesRegex(ValueError, "file names differ"):
            decision.check_identical(root)
        root = self.copy_tree()
        native = root / "adapters" / "geth" / "decision" / "native"
        native.mkdir()
        (native / "patch.diff").write_text("geth only\n")
        self.assertEqual(sorted(decision.check_identical(root)), SHARED)
        (root / "adapters" / "geth" / "decision" / "native.go").write_text("package decision\n")
        with self.assertRaisesRegex(ValueError, "file names differ"):
            decision.check_identical(root)

    def test_missing_or_empty_package_is_refused(self):
        root = self.copy_tree()
        shutil.rmtree(root / "adapters" / "bsc" / "decision")
        with self.assertRaisesRegex(ValueError, "missing shared package"):
            decision.check_identical(root)
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in decision.FAMILIES:
            (root / "adapters" / family / "decision").mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "no files"):
            decision.check_identical(root)


class IndependentVectors(unittest.TestCase):
    def test_vector_file_matches_python_reference(self):
        self.assertEqual(json.loads((PACKAGE / "vectors.json").read_text()), decision.vectors())

    def test_member_digest_is_the_admission_vector(self):
        v, a = decision.vectors(), admission.vectors()
        member = v["import_intent"]["members"][0]
        self.assertEqual(member["exec_ready_digest"], a["exec_ready_digest"])
        self.assertEqual(member["observation"], a["key"])
        self.assertEqual(member["block"], a["candidate"]["hash"])
        self.assertEqual(v["member_decided_digest"], admission.decided_digest(a["exec_ready_digest"],
                                                                              v["import_decided_digest"]))

    def test_marker_is_the_intent_json(self):
        v = decision.vectors()
        marker = json.loads(v["import_marker"])
        self.assertEqual(marker, v["import_intent"])
        self.assertEqual(list(marker), ["native_store_id", "producer", "sequence", "kind", "old_head", "new_head",
                                        "members", "evidence_digest", "events", "digest"])
        self.assertNotIn(" ", v["import_marker"])
        self.assertEqual(v["import_marker_key"], "extractor-decision-marker-v1/producer-1.run-1/0000000000000000")
        self.assertEqual(json.loads(v["finality_marker"])["members"], [])

    def test_digests_change_with_every_input(self):
        v = decision.vectors()
        reference = v["import_intent"]
        changes = [("native_store_id", "other"), ("producer", "other"), ("sequence", 9), ("kind", "CANONICAL"),
                   ("old_head", {"number": 1, "hash": reference["old_head"]["hash"]}),
                   ("new_head", {"number": 7, "hash": "00" * 32}), ("members", []),
                   ("evidence_digest", "11" * 32), ("events", False)]
        for name, value in changes:
            record = dict(reference)
            record[name] = value
            self.assertNotEqual(decision.intent_digest(record), reference["digest"], name)
        for field in ("observation", "block", "exec_ready_digest", "child_root"):
            record = json.loads(json.dumps(reference))
            record["members"][0][field] = "22" * 32
            self.assertNotEqual(decision.intent_digest(record), reference["digest"], field)
        self.assertNotEqual(decision.decided_digest(reference["digest"], "CHECKPOINT"), v["import_decided_digest"])
        self.assertNotEqual(decision.rejected_digest(reference["digest"], "33" * 32), v["import_rejected_digest"])

    def test_reference_formulas_on_known_values(self):
        record = {"native_store_id": "s", "producer": "p.r", "sequence": 1, "kind": "FINALITY",
                  "old_head": {"number": 2, "hash": "aa" * 32}, "new_head": {"number": 3, "hash": "bb" * 32},
                  "members": [], "evidence_digest": "cc" * 32, "events": True}
        data = (b"extractor-decision-intent-v1\x00" + b"\x00" * 7 + b"\x01s" + b"\x00" * 7 + b"\x03p.r"
                + b"\x00" * 7 + b"\x01" + b"\x00" * 7 + b"\x08FINALITY" + b"\x00" * 7 + b"\x02" + b"\xaa" * 32
                + b"\x00" * 7 + b"\x03" + b"\xbb" * 32 + b"\x00" * 8 + b"\xcc" * 32 + b"\x01")
        self.assertEqual(decision.intent_digest(record), decision.sha(data).hex())
        with self.assertRaisesRegex(ValueError, "bad digest"):
            decision.decided_digest("AB", "BATCH")
        self.assertEqual(decision.marker_key("p", "r", 258), "extractor-decision-marker-v1/p.r/0000000000000102")


class Assemble(unittest.TestCase):
    def test_layout(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        packages = decision.assemble(target)
        self.assertEqual(packages, ["extractor/admission", "extractor/admission_binding", "extractor/decision",
                                    "extractor/decision_binding", "extractor/source_inputs"])
        self.assertEqual(sorted(p.name for p in (target / "extractor" / "decision").iterdir()),
                         ["decision.go", "decision.mutants.json", "decision_test.go", "vectors.json"])
        self.assertEqual(sorted(p.name for p in (target / "extractor" / "decision_binding").iterdir()),
                         ["binding.go", "binding_test.go"])
        self.assertEqual((target / "extractor" / "decision" / "decision.go").read_bytes(),
                         (PACKAGE / "decision.go").read_bytes())
        table = json.loads((target / "extractor" / "decision" / "decision.mutants.json").read_text())
        self.assertEqual(table["root"], "../..")
        files = [path.relative_to(target).as_posix() for path in target.rglob("*") if path.is_file()]
        for pattern in table["copy"]:
            self.assertTrue(any(fnmatch.fnmatch(name, pattern) for name in files), pattern)
        self.assertTrue((target / table["mutants"][0]["file"]).is_file())
        with self.assertRaisesRegex(ValueError, "target exists"):
            decision.assemble(target)

    def test_refuses_differing_copies(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in decision.FAMILIES:
            for package in ("admission", "decision", "source_inputs"):
                shutil.copytree(ROOT / "adapters" / family / package, root / "adapters" / family / package)
        (root / "adapters" / "bsc" / "decision" / "README.md").write_text("changed\n")
        with self.assertRaisesRegex(ValueError, "README.md differs"):
            decision.assemble(root / "module", root)
        self.assertFalse((root / "module").exists())


class CommandLine(unittest.TestCase):
    def test_exit_codes(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(decision.main(["check"]), 0)
            self.assertEqual(decision.main(["vectors"]), 0)
            self.assertEqual(decision.main(["assemble", str(target)]), 0)
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(decision.main(["assemble"]), 1)
            self.assertEqual(decision.main(["assemble", str(target)]), 1)
        self.assertIn("needs a target directory", stderr.getvalue())
        self.assertIn("target exists", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
