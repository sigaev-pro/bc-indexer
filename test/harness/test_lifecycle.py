"""Portable controls for the shared E01-02.lifecycle_seams core; no Go toolchain needed."""

import copy
import fnmatch
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lifecycle

ROOT = lifecycle.ROOT
PACKAGE = ROOT / "adapters" / "geth" / "lifecycle"
SHARED = ["README.md", "callers.json", "finality.go", "lifecycle.go", "lifecycle.mutants.json", "lifecycle_test.go",
          "vectors.json"]


class SharedCopies(unittest.TestCase):
    def copy_tree(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in lifecycle.FAMILIES:
            shutil.copytree(ROOT / "adapters" / family / "lifecycle", root / "adapters" / family / "lifecycle")
        return root

    def test_repository_copies_are_identical(self):
        self.assertEqual(sorted(lifecycle.check_identical()), SHARED)

    def test_changed_byte_is_refused(self):
        root = self.copy_tree()
        path = root / "adapters" / "bor" / "lifecycle" / "callers.json"
        data = bytearray(path.read_bytes())
        data[-2] ^= 1
        path.write_bytes(bytes(data))
        with self.assertRaisesRegex(ValueError, "shared file callers.json differs"):
            lifecycle.check_identical(root)

    def test_missing_extra_and_native_files(self):
        root = self.copy_tree()
        (root / "adapters" / "bsc" / "lifecycle" / "finality.go").unlink()
        with self.assertRaisesRegex(ValueError, "file names differ"):
            lifecycle.check_identical(root)
        root = self.copy_tree()
        native = root / "adapters" / "geth" / "lifecycle" / "native"
        # The client blocks add their own native/ directory.
        native.mkdir(exist_ok=True)
        (native / "patches.json").write_text("{}\n")
        self.assertEqual(sorted(lifecycle.check_identical(root)), SHARED)

    def test_missing_or_empty_package_is_refused(self):
        root = self.copy_tree()
        shutil.rmtree(root / "adapters" / "bsc" / "lifecycle")
        with self.assertRaisesRegex(ValueError, "missing shared package"):
            lifecycle.check_identical(root)
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in lifecycle.FAMILIES:
            (root / "adapters" / family / "lifecycle").mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "no files"):
            lifecycle.check_identical(root)


class IndependentVectors(unittest.TestCase):
    def test_vector_file_matches_python_reference(self):
        self.assertEqual(json.loads((PACKAGE / "vectors.json").read_text()), lifecycle.vectors())

    def test_no_op_finality_has_no_digest(self):
        v = lifecycle.vectors()
        with self.assertRaisesRegex(ValueError, "no-op"):
            lifecycle.finality_digest("SAFE", v["fcu_caller"], v["old_finalized"],
                                      {"number": 9, "hash": lifecycle.ZERO})
        with self.assertRaisesRegex(ValueError, "no-op"):
            lifecycle.finality_digest("SAFE", v["fcu_caller"], v["old_finalized"], v["old_finalized"])
        moved = dict(v["old_finalized"], number=v["old_finalized"]["number"] + 1)
        with self.assertRaisesRegex(ValueError, "two numbers"):
            lifecycle.finality_digest("SAFE", v["fcu_caller"], v["old_finalized"], moved)

    def test_digests_bind_their_fields(self):
        v = lifecycle.vectors()
        self.assertNotEqual(v["finalized_digest"], lifecycle.finality_digest(
            "SAFE", v["fcu_caller"], v["old_finalized"], v["new_finalized"]))
        self.assertNotEqual(v["attested_digest"], lifecycle.attestation_digest(
            "GENESIS_FALLBACK", v["target"], v["source"]))


class CallerMap(unittest.TestCase):
    def setUp(self):
        self.data = lifecycle.load_map()

    def test_shipped_map_follows_the_rules(self):
        summary = lifecycle.check_map(self.data)
        for client in lifecycle.CLIENTS:
            self.assertEqual(summary[client]["covered"], sorted(lifecycle.EFFECTS))
            self.assertGreater(summary[client]["callers"], 0)

    def test_sources_are_the_accepted_native_trees(self):
        lock = json.loads((ROOT / ".project/bootstrap/components/candidate-lock.json").read_text())["components"]
        self.assertEqual(self.data["sources"], {c: lock[c]["commit"] for c in lifecycle.CLIENTS})
        self.assertEqual(self.data["sources"], {
            "geth": "1df30b18d9ed35ee9d28ffd4648442c2204554ad",
            "bsc": "54afaff4da31ac9e49ba646a4f65104edac5a2f3",
            "bor": "3a018e38467594508ca31ebeaf804837fb92834a"})

    def test_d100_and_d101_items_are_mapped(self):
        """Every item that D100 and D101 hand to E01-02.lifecycle_seams has a caller row."""
        notes = {c["id"]: c for c in self.data["callers"]}
        for caller_id in ("geth.set-canonical", "geth.recover-ancestors", "geth.sync-target", "bsc.import",
                          "bsc.system-call-api", "bsc.database-commands", "bor.seal-write", "bor.milestone",
                          "bor.manual-commands"):
            self.assertIn(caller_id, notes)

    def broken(self, index, **change):
        data = copy.deepcopy(self.data)
        data["callers"][index].update(change)
        return data

    def first(self, route):
        return next(i for i, c in enumerate(self.data["callers"]) if c["route"] == route)

    def test_rule_violations_are_refused(self):
        obs, dec, ref, ro = (self.first(r) for r in ("OBSERVATION", "DECISION", "REFUSE", "READ_ONLY"))
        cases = {
            "unknown field": self.broken(obs, extra=1),
            "client": self.broken(obs, client="erigon"),
            "id prefix": self.broken(obs, id="erigon.import"),
            "id of another client": self.broken(obs, id="bsc.extra-caller" if self.data["callers"][obs]["client"]
                                                != "bsc" else "geth.extra-caller"),
            "id pattern": self.broken(obs, id=self.data["callers"][obs]["client"] + ".Import"),
            "source": self.broken(obs, source="core/blockchain.go"),
            "entry": self.broken(obs, entry=[]),
            "empty entry": self.broken(obs, entry=[""]),
            "trigger": self.broken(obs, trigger="SOMETIMES"),
            "repeated effect": self.broken(obs, effects=["EXECUTION", "EXECUTION"]),
            "unknown effect": self.broken(obs, effects=["EXECUTION", "PRUNE"]),
            "refusal off REFUSE": self.broken(obs, refusal="x"),
            "observation without execution": self.broken(obs, effects=["CANONICAL"]),
            "observation finality kind": self.broken(obs, kinds=["FINALITY"]),
            "decision execution": self.broken(dec, effects=["EXECUTION"], kinds=["IMPORT"]),
            "decision kinds": self.broken(dec, kinds=self.data["callers"][dec]["kinds"] + ["IMPORT"]),
            "refuse without refusal": self.broken(ref, refusal=""),
            "refuse kind": self.broken(ref, kinds=["IMPORT"]),
            "read-only effect": self.broken(ro, effects=["CANONICAL"]),
            "route": self.broken(ref, route="BYPASS", refusal=""),
            "status": self.broken(obs, status="DONE"),
            "status with no patch": self.broken(dec, status="DONE"),
            "guarded without patch": self.broken(obs, status="GUARDED", patch=""),
            "planned with patch": self.broken(obs, status="PLANNED", patch="x"),
            "owner": self.broken(obs, owner="lifecycle"),
            "duplicate id": self.broken(dec, id=self.data["callers"][obs]["id"],
                                        client=self.data["callers"][obs]["client"]),
        }
        for name, data in cases.items():
            with self.subTest(name), self.assertRaises(ValueError):
                lifecycle.check_map(data)

    def test_effect_coverage_is_required(self):
        data = copy.deepcopy(self.data)
        data["callers"] = [c for c in data["callers"] if not (c["client"] == "bor" and "FINALITY" in c["effects"]
                                                              and c["route"] in ("OBSERVATION", "DECISION"))]
        with self.assertRaisesRegex(ValueError, "bor has no routed caller"):
            lifecycle.check_map(data)
        data = copy.deepcopy(self.data)
        data["sources"]["bor"] = "abc"
        with self.assertRaisesRegex(ValueError, "sources"):
            lifecycle.check_map(data)
        for version in (2, True, "1"):
            data = copy.deepcopy(self.data)
            data["schema_version"] = version
            with self.subTest(version=version), self.assertRaisesRegex(ValueError, "schema"):
                lifecycle.check_map(data)

    def test_table_lists_every_caller(self):
        text = lifecycle.table(self.data)
        for c in self.data["callers"]:
            self.assertIn(f"`{c['id']}`", text)


class Assembly(unittest.TestCase):
    def test_layout_and_existing_target(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        packages = lifecycle.assemble(target)
        self.assertIn("extractor/lifecycle", packages)
        self.assertEqual(sorted(p.name for p in (target / "extractor/lifecycle").iterdir()),
                         sorted(lifecycle.PACKAGE_FILES))
        with self.assertRaises(Exception):
            lifecycle.assemble(target)

    def test_go_mutant_table_matches_the_assembled_files(self):
        table = json.loads((PACKAGE / "lifecycle.mutants.json").read_text())
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        lifecycle.assemble(target)
        names = {mutant["name"] for mutant in table["mutants"]}
        self.assertEqual(len(names), len(table["mutants"]))
        for mutant in table["mutants"]:
            path = target / mutant["file"]
            self.assertTrue(fnmatch.fnmatch(mutant["file"], "extractor/lifecycle/*.go"), mutant["name"])
            self.assertEqual(path.read_text().count(mutant["original"]), 1, mutant["name"])


if __name__ == "__main__":
    unittest.main()
