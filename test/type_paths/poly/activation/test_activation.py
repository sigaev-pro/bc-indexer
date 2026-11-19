"""Real owned-file orchestration controls plus pinned activation seams."""

import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


native = load("poly_activation_driver_tests", HERE / "activation_native.py")
base = load("poly_activation_orchestration_contract", HERE.parent / "test_poly_orchestration.py")
base.driver = native.driver


class ActivationOwnershipTests(base.PolygonOrchestrationTests):
    """Run the accepted direct failure/ownership tests against this wrapper."""


class ActivationSeamTests(unittest.TestCase):
    def test_checked_source_render_keeps_capture_and_two_block_controls(self):
        source = (HERE.parents[3] / "test/native_corpus/poly/main.go").read_bytes()
        rendered = native.support.render_support(source).decode()
        self.assertEqual(rendered.count("package main"), 1)
        self.assertEqual(rendered.count("\nimport ("), 1)
        self.assertIn("corpusBlocks         = 2", rendered)
        for name in ("acceptedCorpusMain", "acceptedCurrentMain", "acceptedCorpusTransactions", "acceptedCurrentTransactions"):
            self.assertIn("func " + name + "(", rendered)
        self.assertNotIn("func main()", rendered)
        self.assertNotIn("func corpusTransactions(", rendered)
        self.assertIn("cfg.VMTrace = typePathTracer", rendered)
        self.assertIn("func activationTransaction", (HERE / "probe.go").read_text())
        with self.assertRaises(ValueError):
            native.support.render_support(source + b"\n")

    def test_changed_accepted_source_and_ambiguous_seam_fail_closed(self):
        with patch.dict(native.support.PINS, {"probe.go": "0" * 64}):
            with self.assertRaises(ValueError):
                native.support.accepted("probe.go")
        with self.assertRaises(ValueError):
            native.support.replace_once("twice twice", "twice", "new")

    def test_wrapper_retains_all_accepted_sources(self):
        with self.subTest(files=native.driver.FILES):
            self.assertIn("activation_support.py", native.driver.FILES)
        self.assertEqual(len(native.support.PINS), 6)


if __name__ == "__main__":
    unittest.main()
