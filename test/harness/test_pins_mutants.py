"""Portable controls for the state-provider mutant driver; no Docker, no native code."""

import copy
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from artifacts import Rejected
import pins
import pins_mutants as driver

ROOT = Path(__file__).resolve().parents[2]
TABLE = ROOT / "test/harness/pins_native.mutants.json"


class TableTests(unittest.TestCase):
    def load(self, table, directory=None):
        path = Path(directory or tempfile.gettempdir()) / "table.json"
        path.write_text(json.dumps(table))
        return driver.load_table(path, Path(directory or ROOT / "adapters"))

    def test_the_table_of_the_repository_is_valid(self):
        table = self.load(json.loads(TABLE.read_text()))
        rows = table["mutants"]
        self.assertEqual(len(rows), 75)
        self.assertEqual({row["family"] for row in rows}, set(pins.FAMILIES))
        # Every shared mutant of a family that prunes block data runs there.
        for row in rows:
            if "legacy offset" in row["name"] and row["file"] == "provider.go":
                self.assertIn(row["family"], pins.PRUNING_CLIENTS)
        self.assertFalse(any("equivalent" in row for row in rows))

    def test_broken_tables_are_refused(self):
        good = json.loads(TABLE.read_text())
        first = good["mutants"][0]
        cases = {
            "language": dict(good, language="go"),
            "no mutants": dict(good, mutants=[]),
            "duplicate name": dict(good, mutants=good["mutants"] + [first]),
            "unknown family": dict(good, mutants=[dict(first, family="polygon")]),
            "unknown file": dict(good, mutants=[dict(first, file="../family.go")]),
            "changes nothing": dict(good, mutants=[dict(first, replacement=first["original"])]),
            "fragment absent": dict(good, mutants=[dict(first, original="nothing of the kind")]),
            "extra field": dict(good, mutants=[dict(first, extra=1)]),
            "missing field": dict(good, mutants=[{key: value for key, value in first.items() if key != "replacement"}]),
            "empty reason": dict(good, mutants=[dict(first, equivalent=" ")]),
        }
        for label, table in cases.items():
            with self.subTest(label), self.assertRaises(Rejected):
                self.load(table)

    def test_a_fragment_must_occur_exactly_once_in_the_family_source(self):
        table = json.loads(TABLE.read_text())
        with tempfile.TemporaryDirectory() as raw:
            adapters = Path(raw).resolve() / "adapters"
            for family in pins.FAMILIES:
                target = adapters / family / "state_provider"
                target.mkdir(parents=True)
                for name in pins.SOURCES:
                    shutil.copyfile(ROOT / "adapters" / family / "state_provider" / name, target / name)
            self.load(table, adapters)
            row = table["mutants"][0]
            source = adapters / row["family"] / "state_provider" / row["file"]
            source.write_text(source.read_text().replace(row["original"], row["original"] + "\n" + row["original"]))
            with self.assertRaises(Rejected):
                self.load(table, adapters)

    def test_selection_names_known_mutants_only(self):
        table = json.loads(TABLE.read_text())
        names = [row["name"] for row in driver.select(table, set())]
        self.assertEqual(names, [row["name"] for row in table["mutants"]])
        self.assertEqual([row["name"] for row in driver.select(table, {names[3], names[0]})], [names[0], names[3]])
        with self.assertRaises(Rejected):
            driver.select(table, {names[0], "unknown mutant"})


class AdmissionTests(unittest.TestCase):
    """Refusals before the run creates its output; no record and no lock left behind."""

    def test_refuses_before_creating_output(self):
        with tempfile.TemporaryDirectory() as raw, patch.object(pins, "source_commit", lambda: "c"), \
                patch.object(pins, "admit_docker", side_effect=RuntimeError("admitted")):
            directory = Path(raw).resolve()
            # The build manifest of the fake tool is the table file itself: any regular file does.
            table = directory / "table.json"
            table.write_text(TABLE.read_text())
            build = {"path": str(table), "sha256": hashlib.sha256(table.read_bytes()).hexdigest(),
                     "size_bytes": table.stat().st_size}
            tool = directory / "tool.json"
            tool.write_text(json.dumps({"kind": "pins-native-tool", "build_manifest": build,
                                        "families": {name: {} for name in pins.FAMILIES}}))
            pin = hashlib.sha256(tool.read_bytes()).hexdigest()
            output = directory / "out"
            with self.assertRaises(Rejected):
                driver.mutants(Path("tool.json"), pin, table, [], output)
            with self.assertRaises(Rejected):
                driver.mutants(tool, pin, table, [], Path("relative"))
            with self.assertRaises(Rejected):
                driver.mutants(tool, "0" * 64, table, [], output)
            families = iter(pins.FAMILIES)

            def fixture(path, sha, deadline):
                return next(families), {"build_manifest": build, "build_root": directory}
            with patch.object(pins, "load_fixture", fixture), self.assertRaisesRegex(RuntimeError, "admitted"):
                # A valid table, tool and fixtures reach the lock.
                driver.mutants(tool, pin, table, [(directory / name, "0" * 64) for name in pins.FAMILIES], output)
            table.write_text(json.dumps({"schema_version": 1, "language": driver.TABLE_LANGUAGE, "mutants": []}))
            with self.assertRaises(Rejected):
                driver.mutants(tool, pin, table, [], output)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
