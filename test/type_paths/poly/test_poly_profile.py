"""Portable profile controls, not native applicability evidence."""

import copy
import importlib.util
from pathlib import Path
import unittest

_spec = importlib.util.spec_from_file_location(
    "type_paths_poly_profile", Path(__file__).with_name("poly_profile.py"))
profile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(profile)


class PolygonPrivateProfileTests(unittest.TestCase):
    def setUp(self):
        self.genesis = {"config": {"chainId": 15001, "londonBlock": 1,
                                   "bor": {"sprint": {"0": 4, "32": 2}}},
                        "alloc": {"1001": {"balance": "0x0", "code": "0x6000"}}}

    def test_private_profile_preserves_native_contracts_and_input(self):
        original = copy.deepcopy(self.genesis)
        result = profile.private_genesis(self.genesis)
        self.assertEqual(self.genesis, original)
        self.assertEqual(result["alloc"], original["alloc"])
        self.assertEqual(result["config"]["chainId"], 15001)
        self.assertEqual(result["config"]["bor"]["sprint"], {"0": 4})
        self.assertEqual(result["config"]["bor"]["stateSyncConfirmationDelay"], {"0": 0})
        self.assertIsNone(result["config"]["bor"]["hampiBlock"])
        self.assertNotIn("osakaTime", result["config"])

    def test_scheduled_rules_are_active_without_modifying_source_schedule(self):
        result = profile.private_genesis(self.genesis)["config"]
        for field in profile.ETHEREUM_SCHEDULE:
            self.assertEqual(result[field], 0, field)
        for field in profile.BOR_SCHEDULE:
            self.assertEqual(result["bor"][field], 0, field)
        self.assertEqual(profile.BOR_SCHEDULE["madhugiriBlock"], 80084800)
        self.assertEqual(profile.BOR_SCHEDULE["uburuBlock"], 96787900)
        self.assertEqual(profile.ETHEREUM_SCHEDULE["pragueBlock"], 73440256)

    def test_refuses_wrong_chain_and_engine(self):
        for change in ({"chainId": 137}, {"bor": None}):
            with self.subTest(change=change):
                bad = copy.deepcopy(self.genesis)
                bad["config"].update(change)
                with self.assertRaises(ValueError):
                    profile.private_genesis(bad)

    def test_refuses_unscheduled_and_non_bor_rules(self):
        for field in ("hampiBlock", "osakaTime", "osakaBlock", "terminalTotalDifficulty"):
            with self.subTest(field=field):
                bad = copy.deepcopy(self.genesis)
                target = bad["config"]["bor"] if field == "hampiBlock" else bad["config"]
                target[field] = 0
                with self.assertRaises(ValueError):
                    profile.private_genesis(bad)


if __name__ == "__main__":
    unittest.main()
