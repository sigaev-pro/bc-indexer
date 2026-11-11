"""Portable controls for the shared E01-02.admission core; no Go toolchain needed."""

import contextlib
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission
import source_inputs

ROOT = admission.ROOT
PACKAGE = ROOT / "adapters" / "geth" / "admission"
BINDING = ROOT / "test" / "harness" / "fixtures" / "admission_core"
SHARED = ["README.md", "admission.go", "admission.mutants.json", "admission_test.go", "canonical.go",
          "canonical_test.go", "vectors.json"]


class SharedCopies(unittest.TestCase):
    def test_repository_copies_are_identical(self):
        self.assertEqual(sorted(admission.check_identical()), SHARED)

    def copy_tree(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in admission.FAMILIES:
            shutil.copytree(ROOT / "adapters" / family / "admission", root / "adapters" / family / "admission")
        return root

    def test_changed_byte_is_refused(self):
        root = self.copy_tree()
        path = root / "adapters" / "bor" / "admission" / "admission.go"
        data = bytearray(path.read_bytes())
        data[-2] ^= 1
        path.write_bytes(bytes(data))
        with self.assertRaisesRegex(ValueError, "shared file admission.go differs"):
            admission.check_identical(root)

    def test_missing_and_extra_files_are_refused(self):
        root = self.copy_tree()
        (root / "adapters" / "bsc" / "admission" / "README.md").unlink()
        with self.assertRaisesRegex(ValueError, "file names differ"):
            admission.check_identical(root)
        root = self.copy_tree()
        (root / "adapters" / "geth" / "admission" / "family.go").write_text("package admission\n")
        with self.assertRaisesRegex(ValueError, "file names differ"):
            admission.check_identical(root)
        root = self.copy_tree()
        (root / "adapters" / "geth" / "admission" / "nested").mkdir()
        (root / "adapters" / "geth" / "admission" / "nested" / "x.go").write_text("package x\n")
        with self.assertRaisesRegex(ValueError, "file names differ"):
            admission.check_identical(root)

    def test_native_directory_is_family_only(self):
        root = self.copy_tree()
        native = root / "adapters" / "bsc" / "admission" / "native"
        native.mkdir(exist_ok=True)
        (native / "patch.diff").write_text("bsc only\n")
        self.assertEqual(sorted(admission.check_identical(root)), SHARED)
        (root / "adapters" / "geth" / "admission" / "native.go").write_text("package admission\n")
        with self.assertRaisesRegex(ValueError, "file names differ"):
            admission.check_identical(root)

    def test_missing_or_empty_package_is_refused(self):
        root = self.copy_tree()
        shutil.rmtree(root / "adapters" / "bor" / "admission")
        with self.assertRaisesRegex(ValueError, "missing shared package"):
            admission.check_identical(root)
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in admission.FAMILIES:
            (root / "adapters" / family / "admission").mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "no files"):
            admission.check_identical(root)


class IndependentVectors(unittest.TestCase):
    def test_vector_file_matches_python_reference(self):
        self.assertEqual(json.loads((PACKAGE / "vectors.json").read_text()), admission.vectors())

    def test_reference_formulas_on_known_values(self):
        self.assertEqual(admission.sha(b"").hex(),
                         "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
        self.assertEqual(admission.u64(258), b"\x00" * 6 + b"\x01\x02")
        self.assertEqual(admission.text("ab"), b"\x00" * 7 + b"\x02ab")
        self.assertEqual(admission.tag("x"), b"x\x00")
        self.assertEqual(admission.observation_key("p", "r", 255), "p.r.00000000000000ff")
        with self.assertRaisesRegex(ValueError, "bad digest"):
            admission.raw("AB" * 32)

    def test_consumed_formula_equals_source_inputs(self):
        capsule, chain = "11" * 32, "22" * 32
        self.assertEqual(admission.consumed_digest(capsule, 5, chain),
                         source_inputs.consumed_digest(bytes.fromhex(capsule), 5, bytes.fromhex(chain)).hex())
        tag = 'tagConsumed  = "extractor-source-inputs-consumed-v1"'
        self.assertIn(tag, (PACKAGE / "admission.go").read_text())
        self.assertIn('tagConsumed = "extractor-source-inputs-consumed-v1"',
                      (ROOT / "adapters" / "geth" / "source_inputs" / "inputs.go").read_text())

    def test_digests_change_with_every_input(self):
        v = admission.vectors()
        base = (v["key"], 0, dict(v["candidate"]), v["pin_digest"], v["capsule_digest"])
        reference = admission.admitted_digest(*base)
        variants = [("other", 0, base[2], base[3], base[4]), (base[0], 1, base[2], base[3], base[4]),
                    (base[0], 0, base[2], "33" * 32, base[4]), (base[0], 0, base[2], base[3], "33" * 32)]
        for field in ("number", "hash", "parent_hash", "parent_root"):
            candidate = dict(base[2])
            candidate[field] = 8 if field == "number" else "44" * 32
            variants.append((base[0], 0, candidate, base[3], base[4]))
        for variant in variants:
            self.assertNotEqual(admission.admitted_digest(*variant), reference)
        ready = admission.exec_ready_digest(v["admitted_digest"], v["consumed_digest"], v["exec_digest"])
        self.assertEqual(ready, v["exec_ready_digest"])
        for index in range(3):
            args = [v["admitted_digest"], v["consumed_digest"], v["exec_digest"]]
            args[index] = "55" * 32
            self.assertNotEqual(admission.exec_ready_digest(*args), ready)

    def test_binding_vectors_use_the_source_inputs_vectors(self):
        inputs = source_inputs.vectors()
        binding = admission.vectors()["binding"]
        self.assertEqual(binding["capsule_digest"], inputs["CapsuleDigest"])
        self.assertEqual(binding["consumed_digest"], inputs["ConsumedDigest"])
        text = (BINDING / "binding_test.go").read_text()
        for request, _ in source_inputs.REQUESTS:
            self.assertIn(request.decode(), text)
        self.assertIn('response0 = []byte(`{"result":[]}`)', text)
        self.assertIn("out[i] = byte(i)", text)
        self.assertIn("}(), 300)", text)
        self.assertIn('{Name: "header", Data: []byte("header-bytes")}, {Name: "body", Data: []byte{}}', text)
        self.assertEqual(source_inputs.ITEMS, (("header", b"header-bytes"), ("body", b"")))


class Canonical(unittest.TestCase):
    def test_accepted_forms(self):
        cases = {
            b'{"b":1,"a":{"d":[1,{"z":0,"y":1}],"c":null}}': b'{"a":{"c":null,"d":[1,{"y":1,"z":0}]},"b":1}',
            b' [ true , false ] ': b"[true,false]",
            b"123456789012345678901234567890": b"123456789012345678901234567890",
            b'"\\u00e9<\\u2028>"': '"é< >"'.encode(),
            b'"\\u001f\\n\\t\\"\\\\"': b'"\\u001f\\u000a\\u0009\\"\\\\"',
            '{"é":1,"e":2,"\U0001f600":3}'.encode(): '{"e":2,"é":1,"\U0001f600":3}'.encode(),
        }
        for data, want in cases.items():
            self.assertEqual(admission.canonical(data), want, data)
            self.assertEqual(admission.canonical(want), want, want)

    def test_refused_forms(self):
        for data in (b"1.5", b"2E1", b"-0", b"00", b'{"a":1,"a":1}', b'"\\udfff"', b"\xef\xbb\xbf1",
                     b'"\xc3"', b'"\x1f"', b"1 1", b"Infinity", b"-Infinity", b"",
                     b"[" * (admission.MAX_DEPTH + 1) + b"]" * (admission.MAX_DEPTH + 1),
                     b'{"a":' * (admission.MAX_DEPTH + 1) + b"1" + b"}" * (admission.MAX_DEPTH + 1),
                     b'{"\\ud800":1}'):
            with self.assertRaises(admission.CanonicalError, msg=data):
                admission.canonical(data)

    def test_depth_limit_is_inclusive(self):
        arrays = b"[" * admission.MAX_DEPTH + b"]" * admission.MAX_DEPTH
        self.assertEqual(admission.canonical(arrays), arrays)
        objects = b'{"a":' * admission.MAX_DEPTH + b"1" + b"}" * admission.MAX_DEPTH
        self.assertEqual(admission.canonical(objects), objects)

    def test_vector_cases_cover_both_outcomes(self):
        cases = admission.vectors()["canonical"]
        self.assertEqual(len(cases), len(admission.CANONICAL_INPUTS))
        self.assertGreaterEqual(sum(case["want"] is None for case in cases), 20)
        self.assertGreaterEqual(sum(case["want"] is not None for case in cases), 8)


class Assemble(unittest.TestCase):
    def test_layout(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        packages = admission.assemble(target)
        self.assertEqual(packages, ["extractor/admission", "extractor/admission_binding", "extractor/source_inputs"])
        self.assertEqual((target / "go.mod").read_text(), "module github.com/ethereum/go-ethereum\n\ngo 1.25\n")
        self.assertEqual(sorted(p.name for p in (target / "extractor" / "admission").iterdir()),
                         ["admission.go", "admission_test.go", "canonical.go", "canonical_test.go", "vectors.json"])
        self.assertEqual(sorted(p.name for p in (target / "extractor" / "source_inputs").iterdir()),
                         ["inputs.go", "inputs_test.go"])
        self.assertEqual(sorted(p.name for p in (target / "extractor" / "admission_binding").iterdir()),
                         ["binding.go", "binding_test.go"])
        self.assertEqual((target / "extractor" / "admission" / "admission.go").read_bytes(),
                         (PACKAGE / "admission.go").read_bytes())
        with self.assertRaisesRegex(ValueError, "target exists"):
            admission.assemble(target)

    def test_refuses_differing_copies(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        for family in admission.FAMILIES:
            shutil.copytree(ROOT / "adapters" / family / "admission", root / "adapters" / family / "admission")
        (root / "adapters" / "bsc" / "admission" / "README.md").write_text("changed\n")
        with self.assertRaisesRegex(ValueError, "README.md differs"):
            admission.assemble(root / "module", root)
        self.assertFalse((root / "module").exists())


class CommandLine(unittest.TestCase):
    def test_exit_codes(self):
        target = Path(self.enterContext(tempfile.TemporaryDirectory())) / "module"
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(admission.main(["check"]), 0)
            self.assertEqual(admission.main(["vectors"]), 0)
            self.assertEqual(admission.main(["assemble", str(target)]), 0)
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(admission.main(["assemble"]), 1)
            self.assertEqual(admission.main(["assemble", str(target)]), 1)
        self.assertIn("needs a target directory", stderr.getvalue())
        self.assertIn("target exists", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
