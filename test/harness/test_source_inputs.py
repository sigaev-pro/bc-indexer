"""Portable controls for the shared E01-02.inputs core; no Go toolchain needed."""

import contextlib
import io
from pathlib import Path
import re
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import source_inputs

ROOT = source_inputs.ROOT
GO_TEST = ROOT / "adapters" / "geth" / "source_inputs" / "inputs_test.go"


class SharedCopies(unittest.TestCase):
    def test_repository_copies_are_identical(self):
        digests = source_inputs.check_identical()
        self.assertEqual(sorted(digests), ["README.md", "inputs.go", "inputs_test.go",
                                           "source_inputs.mutants.json"])

    def copy_tree(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        for family in source_inputs.FAMILIES:
            shutil.copytree(ROOT / "adapters" / family / "source_inputs", root / "adapters" / family / "source_inputs")
        return root

    def test_changed_byte_is_refused(self):
        root = self.copy_tree()
        path = root / "adapters" / "bor" / "source_inputs" / "inputs.go"
        data = bytearray(path.read_bytes())
        data[-2] ^= 1
        path.write_bytes(bytes(data))
        with self.assertRaisesRegex(ValueError, "shared file inputs.go differs"):
            source_inputs.check_identical(root)

    def test_missing_file_is_refused(self):
        root = self.copy_tree()
        (root / "adapters" / "bsc" / "source_inputs" / "README.md").unlink()
        with self.assertRaisesRegex(ValueError, "file names differ"):
            source_inputs.check_identical(root)

    def test_extra_file_is_refused(self):
        root = self.copy_tree()
        (root / "adapters" / "geth" / "source_inputs" / "family.go").write_text("package sourceinputs\n")
        with self.assertRaisesRegex(ValueError, "file names differ"):
            source_inputs.check_identical(root)

    def test_nested_files_count_and_empty_directories_do_not(self):
        root = self.copy_tree()
        (root / "adapters" / "geth" / "source_inputs" / "empty").mkdir()
        self.assertIn("inputs.go", source_inputs.check_identical(root))
        (root / "adapters" / "geth" / "source_inputs" / "empty" / "x.go").write_text("package x\n")
        with self.assertRaisesRegex(ValueError, "file names differ"):
            source_inputs.check_identical(root)

    def test_missing_package_is_refused(self):
        root = self.copy_tree()
        shutil.rmtree(root / "adapters" / "bor" / "source_inputs")
        with self.assertRaisesRegex(ValueError, "missing shared package"):
            source_inputs.check_identical(root)

    def test_empty_packages_are_refused(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in source_inputs.FAMILIES:
            (root / "adapters" / family / "source_inputs").mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "no files"):
            source_inputs.check_identical(root)

    def test_cli_exit_codes(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(source_inputs.main(["check"]), 0)
            self.assertEqual(source_inputs.main(["vectors"]), 0)
        root = self.copy_tree()
        (root / "adapters" / "geth" / "source_inputs" / "README.md").write_text("changed\n")
        original = source_inputs.ROOT
        source_inputs.ROOT = root
        try:
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr):
                self.assertEqual(source_inputs.main(["check"]), 1)
            self.assertIn("README.md differs", stderr.getvalue())
        finally:
            source_inputs.ROOT = original


class IndependentVectors(unittest.TestCase):
    def test_go_expected_values_match_python_reference(self):
        text = GO_TEST.read_text()
        constants = dict(re.findall(r'^\twant(\w+)\s*=\s*"([0-9a-f]{64})"$', text, re.MULTILINE))
        self.assertEqual(constants, source_inputs.vectors())

    def test_reference_formulas_on_known_values(self):
        # SHA-256 of the empty string is a published constant.
        self.assertEqual(source_inputs.sha(b"").hex(),
                         "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
        self.assertEqual(source_inputs.u64(1), b"\x00" * 7 + b"\x01")
        self.assertEqual(source_inputs.tag("x"), b"x\x00")

    def test_go_vector_inputs_match_python_inputs(self):
        text = GO_TEST.read_text()
        self.assertIn(f'const observation = "{source_inputs.OBSERVATION}"', text)
        for request, response in source_inputs.REQUESTS:
            self.assertIn(request.decode(), text)
        self.assertIn('[]byte(`{"result":[]}`)', text)
        self.assertEqual(source_inputs.REQUESTS[0][1], b'{"result":[]}')
        self.assertIn("out[i] = byte(i)", text)
        self.assertIn("}(), 300)", text)
        self.assertEqual(source_inputs.REQUESTS[1][1], bytes(range(256)) * 300)
        self.assertIn('{Name: "header", Data: []byte("header-bytes")}, {Name: "body", Data: []byte{}}', text)
        self.assertEqual(source_inputs.ITEMS, (("header", b"header-bytes"), ("body", b"")))


if __name__ == "__main__":
    unittest.main()
