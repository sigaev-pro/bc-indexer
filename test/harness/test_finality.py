"""Portable controls for the E05-01.eth_finality core; no Go toolchain needed."""

import fnmatch
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import finality

ROOT = finality.ROOT
PACKAGE = finality.PACKAGE
FILES = ["README.md", "finality.go", "finality.mutants.json", "finality_test.go", "vectors.json"]


class Vectors(unittest.TestCase):
    def test_stored_vectors_match_the_formulas(self):
        self.assertEqual(finality.check_vectors(), sorted(finality.vectors()))

    def test_each_digest_is_distinct_and_hex(self):
        values = finality.vectors()
        digests = [v for k, v in values.items() if k.endswith("_digest")]
        self.assertEqual(len(set(digests)), len(digests))
        for digest in digests:
            self.assertRegex(digest, r"^[0-9a-f]{64}$")
        # A retained record differs from the same live record: the origin
        # rewrite changes the digest.
        self.assertNotEqual(values["live_safe_digest"], values["retained_safe_digest"])
        # The startup fallback and the consensus setter of the same block
        # differ by basis.
        self.assertNotEqual(values["restart_safe_digest"], values["live_safe_digest"])


class Package(unittest.TestCase):
    def test_files_are_complete(self):
        self.assertEqual(sorted(p.name for p in PACKAGE.iterdir()), sorted(FILES + ["native"]))

    def test_no_native_block_yet(self):
        native = PACKAGE / "native"
        self.assertTrue(native.is_dir())
        self.assertEqual([p.name for p in native.iterdir()], ["README.md"])
        self.assertIn("E05", native.joinpath("README.md").read_text() + PACKAGE.joinpath("README.md").read_text())


class Assembly(unittest.TestCase):
    def test_layout_and_existing_target(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        packages = finality.assemble(target)
        self.assertIn("extractor/finality", packages)
        for earlier in ("extractor/lifecycle", "extractor/decision", "extractor/admission",
                        "extractor/source_inputs"):
            self.assertIn(earlier, packages)
        self.assertEqual(sorted(p.name for p in (target / "extractor/finality").iterdir()),
                         sorted(finality.PACKAGE_FILES))
        with self.assertRaises(Exception):
            finality.assemble(target)

    def test_go_mutant_table_matches_the_assembled_files(self):
        table = json.loads((PACKAGE / "finality.mutants.json").read_text())
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        finality.assemble(target)
        names = {mutant["name"] for mutant in table["mutants"]}
        self.assertEqual(len(names), len(table["mutants"]))
        for mutant in table["mutants"]:
            path = target / mutant["file"]
            self.assertTrue(fnmatch.fnmatch(mutant["file"], "extractor/finality/*.go"), mutant["name"])
            self.assertEqual(path.read_text().count(mutant["original"]), 1, mutant["name"])


if __name__ == "__main__":
    unittest.main()
