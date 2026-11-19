"""Owned-file controls and exact rendering checks; no native execution claim."""

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


native = load("crossing_driver_tests", HERE / "crossing_native.py")
base = load("crossing_owned_contract", HERE.parent / "test_poly_orchestration.py")
base.driver = native.driver


class CrossingOwnershipTests(base.PolygonOrchestrationTests):
    def test_all_parent_sources_copied_and_retained(self):
        result = self.prepare()
        self.saved(result, self.prepared, "preparation.json", "PASS")
        for label, pins, read in (
            ("accepted-poly", native.support.parent.PINS, native.support.parent.accepted),
            ("activation-helpers", native.support.ACTIVATION_PINS, native.support.activation_source),
        ):
            directory = self.prepared / label
            self.assertEqual({p.name for p in directory.iterdir()}, set(pins))
            self.assertEqual({Path(ref["path"]).name for ref in result["code"] if Path(ref["path"]).parent == directory}, set(pins))
            for name in pins:
                self.assertEqual((directory / name).read_bytes(), read(name))

    def test_parent_snapshot_failure_preserves_owned_failure(self):
        real_copy = native.driver.copy_artifact
        def copy(source, destination, *args, **kwargs):
            if destination.parent.name == "activation-helpers":
                raise OSError("crossing parent snapshot failure")
            return real_copy(source, destination, *args, **kwargs)
        with patch.object(native.driver, "copy_artifact", side_effect=copy):
            result = self.prepare()
        self.saved(result, self.prepared, "preparation.json", "FAIL")
        self.assertIn("crossing parent snapshot failure", result["error"])
        self.assertEqual(result["commands"], [])


class CrossingRenderingTests(unittest.TestCase):
    def test_generator_changes_only_transaction_selection(self):
        corpus = (HERE.parents[3] / "test/native_corpus/poly/main.go").read_bytes()
        before = native.support.parent.render_support(corpus).decode()
        after = native.support.render_support(corpus).decode()
        def generator(text):
            return text[text.index("func generate("):text.index("func readBlocks(")]
        expected = generator(before).replace(
            "\ttxs, err := corpusTransactions(config, sender)\n\tif err != nil {\n\t\treturn err\n\t}\n", "")
        expected = expected.replace("\t\tif number == 1 {", "\t\ttxs, err := crossingTransactions(config, sender, number)\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif len(txs) != 0 {")
        self.assertEqual(generator(after), expected)
        self.assertEqual(after.count("package main"), 1)
        self.assertEqual(after.count("\nimport ("), 1)
        self.assertNotIn("func main()", after)
        self.assertNotIn("func activationCase(", after)
        with self.assertRaises(ValueError):
            native.support.render_support(corpus + b"\n")

    def test_helpers_preserve_native_proofs_and_record_collection(self):
        corpus = (HERE.parents[3] / "test/native_corpus/poly/main.go").read_bytes()
        generated = native.support.render_support(corpus).decode()
        activation = native.support.activation_source("probe.go").decode()
        unchanged = activation[activation.index("func activationView("):activation.index("func activationCase(")]
        self.assertTrue(generated.endswith(unchanged))

    def test_private_pairing_seam_preserves_other_profile_logic(self):
        corpus = (HERE.parents[3] / "test/native_corpus/poly/main.go").read_bytes()
        generated = native.support.render_support(corpus).decode()
        source = native.support.activation_source("probe.go").decode()
        original = source[source.index("func historical("):source.index("func activationTransaction(")]
        expected = original.replace("func historical(input, output string, ethPrefix, borPrefix int)",
                                    "func crossingInputs(input, output string, ethPrefix, borPrefix int, gate, borGate string)")
        expected = expected.replace("\tif ethPrefix < 2 {", "\tconfig[gate] = 2\n\tif borGate != \"\" {\n\t\tengine[borGate] = 2\n\t}\n\tif ethPrefix < 2 {")
        actual = generated[generated.index("func crossingInputs("):generated.index("func crossingTransaction(")]
        self.assertEqual(actual, expected)

    def test_pin_drift_and_nonunique_seams_are_rejected(self):
        with patch.dict(native.support.ACTIVATION_PINS, {"probe.go": "0" * 64}):
            with self.assertRaises(ValueError):
                native.support.activation_source("probe.go")
        with self.assertRaises(ValueError):
            native.support.replace_once("duplicate duplicate", "duplicate", "new")


if __name__ == "__main__":
    unittest.main()
