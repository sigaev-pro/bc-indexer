"""Portable rejection controls; these tests grant no native runtime credit."""

import copy
import importlib.util
from pathlib import Path
import sys
import unittest

SPEC = importlib.util.spec_from_file_location("bsc_type_native_driver", Path(__file__).with_name("native.py"))
driver = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = driver
SPEC.loader.exec_module(driver)


def valid_report():
    rows = [{"case": "roundtrip-current-signer", "type": i, "hash": "0x" + f"{i:064x}",
             "sender": "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"} for i in range(5)]
    boundaries = {1: ("31302047", 1795046400), 2: ("31302047", 1795046400),
                  3: ("100000000", 1718863499), 4: ("100000000", 1742436599)}
    rows += [{"case": "pre-activation-signer-refusal", "type": i, "block": boundaries[i][0], "time": boundaries[i][1]}
             for i in range(1, 5)]
    return {"schema_version": 1, "scope": "native-decoder-signer-classifier", "rows": rows,
            "unknown_typed_ids_rejected": 123,
            "classification": [{"type": i, "system": True, "hash": "0x" + f"{i:064x}"} for i in range(5)],
            "classification_controls": 5, "import_observation": "NOT_RUN",
            "system_transaction_state": "NOT_RUN", "public_profiles": "NOT_RUN"}


class ReportTests(unittest.TestCase):
    def reject(self, report):
        with self.assertRaises((driver.Rejected, TypeError, ValueError)):
            driver.check_report(report)

    def test_complete_component_report(self):
        driver.check_report(valid_report())

    def test_missing_top_level_fields(self):
        for field in valid_report():
            with self.subTest(field=field):
                report = valid_report()
                del report[field]
                self.reject(report)

    def test_no_import_or_public_credit(self):
        for field in ("import_observation", "system_transaction_state", "public_profiles"):
            report = valid_report()
            report[field] = "PASS"
            self.reject(report)

    def test_malformed_shape(self):
        for report in (None, [], "PASS", 1):
            self.reject(report)
        for field, bad in (("schema_version", 2), ("schema_version", True), ("scope", "other"), ("rows", {}),
                           ("classification", {}), ("unknown_typed_ids_rejected", True),
                           ("unknown_typed_ids_rejected", 122), ("classification_controls", 4),
                           ("classification_controls", True)):
            report = valid_report()
            report[field] = bad
            self.reject(report)

    def test_every_native_case_is_required_once(self):
        for index in range(9):
            report = valid_report()
            del report["rows"][index]
            self.reject(report)
            report = valid_report()
            report["rows"][index] = copy.deepcopy(report["rows"][(index + 1) % 9])
            self.reject(report)
        report = valid_report()
        report["rows"][0] = None
        self.reject(report)

    def test_roundtrip_facts_bound(self):
        for field, value in (("sender", "0x" + "00" * 20), ("sender", None), ("hash", "not a hash"),
                             ("type", False), ("case", "other")):
            report = valid_report()
            report["rows"][0][field] = value
            self.reject(report)

    def test_activation_inputs_required(self):
        for field, bad in (("block", "-1"), ("block", 1), ("time", -1), ("time", True),
                           ("time", 1795046401), ("block", "31302048")):
            report = valid_report()
            report["rows"][5][field] = bad
            self.reject(report)

    def test_classification_requires_each_identity(self):
        for field, bad in (("type", False), ("type", 9), ("system", False), ("system", 1),
                           ("hash", "0x" + "ff" * 32)):
            report = valid_report()
            report["classification"][0][field] = bad
            self.reject(report)
        for bad in (None, {}, []):
            report = valid_report()
            report["classification"][0] = bad
            self.reject(report)
        report = valid_report()
        report["classification"].pop()
        self.reject(report)


if __name__ == "__main__":
    unittest.main()
