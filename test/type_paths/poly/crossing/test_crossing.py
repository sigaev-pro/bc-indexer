"""Owned-file controls and exact rendering checks; no native execution claim."""

import importlib.util
import hashlib
import json
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
    def test_portable_go_bodies_match_frozen_producer(self):
        source = (HERE / "probe.go").read_bytes()
        origin = json.loads((HERE / "controls/source.json").read_text())
        self.assertEqual(hashlib.sha256(source).hexdigest(), origin["source_sha256"])
        text = source.decode()
        body = text[text.index("func crossingKind("):text.index("func crossingCase(")].rstrip() + "\n"
        selected = (HERE / "controls/logic.go").read_text()
        selected = selected[selected.index("func crossingKind("):]
        self.assertEqual(body, selected)
        self.assertEqual(hashlib.sha256(body.encode()).hexdigest(), origin["selected_body_sha256"])

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

    def test_pin_drift_and_nonunique_seams_are_rejected(self):
        corpus = (HERE.parents[3] / "test/native_corpus/poly/main.go").read_bytes()
        source = native.support.parent.render_support(corpus).decode()
        source = source.replace("import (", "// removed imports (")
        with patch.object(native.support.parent, "render_support", return_value=source.encode()):
            with self.assertRaises(ValueError):
                native.support.render_support(corpus)
        with patch.dict(native.support.ACTIVATION_PINS, {"probe.go": "0" * 64}):
            with self.assertRaises(ValueError):
                native.support.activation_source("probe.go")
        with self.assertRaises(ValueError):
            native.support.replace_once("duplicate duplicate", "duplicate", "new")

    def test_unexpected_helper_seams_fail_closed(self):
        corpus = (HERE.parents[3] / "test/native_corpus/poly/main.go").read_bytes()
        source = native.support.activation_source("probe.go")
        for changed in (source + b"\nfunc activationCase(", source.replace(b"Nonce: 1,", b"Nonce: 2,", 1)):
            with self.subTest(changed=changed[-40:]), patch.object(native.support, "activation_source", return_value=changed):
                with self.assertRaises(ValueError):
                    native.support.render_support(corpus)
        with patch.object(native.support.parent, "render_support", return_value=b"package main\n"):
            with self.assertRaises(ValueError):
                native.support.render_support(corpus)


if __name__ == "__main__":
    unittest.main()
