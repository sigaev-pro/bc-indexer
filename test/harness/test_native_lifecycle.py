"""Portable controls for the E05-02.reorg module (ETH block); no Go toolchain needed."""

import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import native_lifecycle

ROOT = native_lifecycle.ROOT


class Layout(unittest.TestCase):
    def test_check_lists_the_eth_block_files(self):
        files = native_lifecycle.check()
        self.assertEqual(sorted(files), sorted(native_lifecycle.BLOCK_FILES))
        for digest in files.values():
            self.assertRegex(digest, r"^[0-9a-f]{64}$")

    def test_missing_file_is_refused(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for part in ("test", "test/native_lifecycle", "test/native_lifecycle/eth"):
            (root / part).mkdir()
        for name in native_lifecycle.BLOCK_FILES:
            (root / "test/native_lifecycle/eth" / name).write_text("package eth\n")
        with self.assertRaisesRegex(ValueError, "README.md"):
            native_lifecycle.check(root)
        (root / "test/native_lifecycle/README.md").write_text("# Native lifecycle\n")
        native_lifecycle.check(root)
        (root / "test/native_lifecycle/eth/store.go").unlink()
        with self.assertRaisesRegex(ValueError, "store.go"):
            native_lifecycle.check(root)


class Assembly(unittest.TestCase):
    def test_layout_binds_the_local_modules(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        packages = native_lifecycle.assemble(target)
        for package in ("extractor/native_lifecycle_eth", "extractor/finality", "extractor/lifecycle",
                        "extractor/decision", "extractor/admission", "extractor/source_inputs"):
            self.assertIn(package, packages)
        self.assertEqual(sorted(p.name for p in (target / "extractor/native_lifecycle_eth").iterdir()),
                         sorted(native_lifecycle.BLOCK_FILES))
        # The assembled files are the block files unchanged, and the module
        # graph binds every repository-local module the block composes.
        for name in native_lifecycle.BLOCK_FILES:
            self.assertEqual((target / "extractor/native_lifecycle_eth" / name).read_bytes(),
                             (ROOT / "test/native_lifecycle/eth" / name).read_bytes())
        go_mod = (target / "go.mod").read_text()
        for module in native_lifecycle.LOCAL_MODULES:
            self.assertIn(f"replace {module} => ", go_mod)
        for required in ("protocol/semantic v0.0.0", "test/lifecycle_validator v0.0.0"):
            self.assertIn(required, go_mod)
        with self.assertRaises(Exception):
            native_lifecycle.assemble(target)

    def test_existing_target_is_refused_by_the_finality_assembly(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        native_lifecycle.assemble(target)
        with self.assertRaises(Exception):
            native_lifecycle.assemble(target)


if __name__ == "__main__":
    unittest.main()
