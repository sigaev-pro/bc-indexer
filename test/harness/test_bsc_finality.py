#!/usr/bin/env python3
"""Portable controls of the BSC finality harness (run by bootstrap/ci)."""

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import bsc_finality  # noqa: E402

PACKAGE = bsc_finality.PACKAGE
REORG = bsc_finality.REORG


class Vectors(unittest.TestCase):
    def test_vector_file_matches_python_reference(self):
        stored = json.loads((PACKAGE / "vectors.json").read_text())
        self.assertEqual(stored, bsc_finality.vectors())

    def test_check_accepts_the_shipped_file(self):
        self.assertEqual(bsc_finality.check(), bsc_finality.vectors())

    def test_digests_bind_their_fields(self):
        v = bsc_finality.vectors()
        # The attested digest changes with the finalized source alone.
        changed = dict(v["finalized"])
        changed["hash"] = changed["hash"][:-1] + "2"
        other = bsc_finality.attested_digest(v["snapshot_head"], v["justified"], changed)
        self.assertNotEqual(other, v["attested_digest"])
        # The fallback digest changes with the reason alone.
        other = bsc_finality.fallback_digest(v["fallback_head"], "PRE_PLATO", v["genesis"])
        self.assertNotEqual(other, v["fallback_digest"])
        # The early digest changes with the owned vote snapshot alone.
        e = v["early"]
        other = bsc_finality.early_digest(e["origin"], e["evaluation"], e["source"], e["target"], e["candidate"],
                                          e["validators"], e["count"], e["quorum"], e["matching"], "other votes")
        self.assertNotEqual(other, v["early_digest"])

    def test_quorum_is_two_thirds_ceil(self):
        # ceil(2n/3) with a floor of two validators, the native Parlia bound
        # the early evidence validation requires.
        for count, quorum in ((1, 2), (2, 2), (3, 2), (4, 3), (20, 14), (21, 14), (22, 15)):
            self.assertEqual(bsc_finality.quorum_of(count), quorum, count)
        v = bsc_finality.vectors()
        self.assertEqual(v["early"]["quorum"], bsc_finality.quorum_of(v["early"]["count"]))


class Assembly(unittest.TestCase):
    def test_layout_and_existing_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "module"
            packages = bsc_finality.assemble(target)
            self.assertIn("extractor/bsc/finality", packages)
            self.assertIn("extractor/native_lifecycle_bsc", packages)
            self.assertEqual(sorted(p.name for p in (target / "extractor/bsc/finality").iterdir()),
                             sorted(bsc_finality.PACKAGE_FILES))
            self.assertEqual(sorted(p.name for p in (target / "extractor/native_lifecycle_bsc").iterdir()),
                             sorted(bsc_finality.REORG_FILES))
            # The reorg block is its own lifecycle test package, not a file
            # inside the finality package, and the module graph binds every
            # repository-local module it composes.
            go_mod = (target / "go.mod").read_text()
            for module in bsc_finality.LOCAL_MODULES:
                self.assertIn(f"replace {module} => ", go_mod)
            for required in ("adapters/lifecycle_registry v0.0.0", "adapters/recovery_gc v0.0.0",
                             "protocol/semantic v0.0.0", "test/lifecycle_validator v0.0.0"):
                self.assertIn(required, go_mod)
            with self.assertRaises(Exception):
                bsc_finality.assemble(target)

    def test_package_files_exist(self):
        for name in bsc_finality.PACKAGE_FILES:
            self.assertTrue((PACKAGE / name).is_file(), name)
        for name in bsc_finality.REORG_FILES:
            self.assertTrue((REORG / name).is_file(), name)

    def test_main_commands(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(bsc_finality.main(["assemble", str(Path(tmp) / "module")]), 0)
        self.assertEqual(bsc_finality.main(["check"]), 0)
        self.assertEqual(bsc_finality.main(["vectors"]), 0)
        self.assertEqual(bsc_finality.main(["assemble"]), 1)


if __name__ == "__main__":
    unittest.main()
