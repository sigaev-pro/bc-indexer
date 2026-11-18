"""Portable rejection controls; these do not execute BSC."""
import copy
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bsc_modern_init_driver", HERE / "native.py")
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def report():
    before = {"coinbase": "10000", "validator": "0", "reward": "0", "burn": "0", "nonce": "0",
              "slot_0": "0", "slot_1": "0", "slot_3": "0", "slot_6": "0", "slot_15": "0", "slot_20": "0",
              "slot_23": "0", "slot_24": "0", "validator_index": "0"}
    initialized = dict(before, nonce="1", slot_0="1", slot_1="21")
    after = dict(initialized, coinbase="0", validator="8375", reward="625", burn="1000", nonce="2",
                 slot_6="1000", slot_15="625", slot_20="1")
    return {"schema_version": 1, "scope": "modern-validator-init-deposit-prerequisite",
            "import_observation": "NOT_RUN", "system_transaction_state": "NOT_RUN", "public_profiles": "NOT_RUN",
            "complete_system_initialization": "NOT_RUN", "header_number": "100000001", "header_time": 1795046401,
            "code_hashes": {row["address"]: row["bytecode_sha256"] for row in driver.load(HERE / "provenance.json")["contracts"]},
            "rows": [{"method": "init", "before": before, "after": initialized, "hash": "0x" + "1" * 64},
                     {"method": "deposit", "before": initialized, "after": after, "hash": "0x" + "2" * 64}]}


class ModernInitControls(unittest.TestCase):
    def test_baseline(self):
        driver.check_report(report())

    def test_preconditions_and_transition(self):
        for index in range(2):
            for phase in ("before", "after"):
                for key in report()["rows"][index][phase]:
                    with self.subTest(index=index, phase=phase, key=key):
                        bad = copy.deepcopy(report())
                        bad["rows"][index][phase][key] = "999999"
                        with self.assertRaises(driver.Rejected):
                            driver.check_report(bad)

    def test_no_import_credit(self):
        for key in ("import_observation", "system_transaction_state", "public_profiles", "complete_system_initialization"):
            bad = report()
            bad[key] = "PASS"
            with self.assertRaises(driver.Rejected):
                driver.check_report(bad)

    def test_bytecode_provenance(self):
        bad = report()
        bad["code_hashes"]["0x0000000000000000000000000000000000001000"] = "a" * 64
        with self.assertRaises(driver.Rejected):
            driver.check_report(bad)

    def test_missing_row(self):
        bad = report()
        bad["rows"].pop()
        with self.assertRaises(driver.Rejected):
            driver.check_report(bad)

    def test_shape_identity_and_context(self):
        cases = [("schema_version", True), ("schema_version", 2), ("scope", "full-import"),
                 ("rows", None), ("rows", [None, None]), ("code_hashes", {}),
                 ("header_number", "1"), ("header_time", 0)]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                bad = report()
                bad[key] = value
                with self.assertRaises(driver.Rejected):
                    driver.check_report(bad)
        for key, value in (("method", "wrong"), ("hash", "malformed")):
            bad = report()
            bad["rows"][0][key] = value
            with self.assertRaises(driver.Rejected):
                driver.check_report(bad)

    def test_tool_identity_before_binary(self):
        good = {"kind": "bsc-modern-init-tool", "native_source_commit": driver.COMMIT,
                "probe": "native_test.go", "driver": "native.py", "provenance": "provenance.json", "binary": {}}
        for key in ("kind", "native_source_commit", "probe", "driver", "provenance"):
            with self.subTest(key=key):
                bad = dict(good)
                bad[key] = "wrong"
                with patch.object(driver, "pinned_json", return_value=({}, bad)), \
                        patch.object(driver, "reference", side_effect=lambda p: p.name), \
                        patch.object(driver, "artifact", return_value=Path("/binary")), \
                        patch.object(driver.pins, "owned_attempt", return_value="UNEXPECTED"):
                    with self.assertRaises(driver.Rejected):
                        driver.run(Path("/tool.json"), "unused", Path("/output"))


if __name__ == "__main__":
    unittest.main()
