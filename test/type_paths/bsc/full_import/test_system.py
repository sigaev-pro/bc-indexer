"""Source-derived initializer, deposit and election transition controls."""
import copy
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bsc_full_import_system_controls", HERE / "judge.py")
judge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(judge)

PROTECTOR = int("08e68ec70fa3b629784fdb28887e206ce8561e08", 16)
INITIALIZERS = [
    ("1000", "e1c7392a", 1, {"1": 21, "validator_index": 0, "3": 0, "first_validator_incoming": 0}),
    ("1001", "e1c7392a", 1, {"4": 50, "5": 150}),
    ("1003", "e1c7392a", 1, {"3": 0, "4": int.from_bytes(b"Binance-Chain-Tigris".ljust(32, b"\0"), "big"), "5": 10**16}),
    ("1004", "e1c7392a", 1, {"1": 2 * 10**15, "native_decimals": 18}),
    ("1005", "e1c7392a", 1, {"1": 1, "2": 5, "3": 1, "4": 80}),
    ("1006", "e1c7392a", 1, {"1": 10**20, "2": 10**17}),
    ("2000", "e1c7392a", 1, {"1": 50, "2": 0, "3": 0, "4": 2**64 - 1}),
    ("2002", "8129fc1c", (PROTECTOR << 24) | 1, {"53": 5000, "54": 2000 * 10**18, "55": 10**18, "56": 45, "74": 2}),
    ("2004", "8129fc1c", (PROTECTOR << 24) | 1, {}),
    ("2005", "8129fc1c", 1, {}), ("2006", "8129fc1c", 1, {}),
    ("3000", "8129fc1c", 1, {"1": 1, "51": PROTECTOR << 8}),
]


def base(to, data):
    before = {address: {"balance": "0", "nonce": 0, "storage": {}} for address in judge.ledger_addresses()["all"]}
    before[judge.VALIDATOR]["nonce"] = 9
    after = copy.deepcopy(before); after[judge.VALIDATOR]["nonce"] = 10
    return {"type": 0, "from": judge.VALIDATOR, "to": to, "value": "0", "nonce": 9,
            "gas_price": "0", "data": data, "receipt": {"status": 1}, "error": None, "before": before, "after": after}


def initializer(case):
    suffix, selector, flag, slots = case
    to = "0x" + suffix.zfill(40)
    row = base(to, selector)
    row["before"][to]["storage"] = {key: "0" for key in {"0": flag, **slots}}
    row["after"][to]["storage"] = {key: str(value) for key, value in {"0": flag, **slots}.items()}
    return row


def deposit(active):
    row = base(judge.VALIDATOR_CONTRACT, "f340fa01" + judge.VALIDATOR[2:].zfill(64))
    row["value"] = "10000"
    row["before"][judge.VALIDATOR]["balance"] = "10000"
    for address, before, after in ((judge.VALIDATOR_CONTRACT, 100, 8475), (judge.REWARD, 20, 645), (judge.BURN, 30, 1030)):
        row["before"][address]["balance"] = str(before); row["after"][address]["balance"] = str(after)
    initial = 10 if active else 0
    row["before"][judge.VALIDATOR_CONTRACT]["storage"] = {"0": "1", "23": "0", "24": "0", "validator_index": str(int(active)),
                                                          "3": str(initial), "first_validator_incoming": str(initial)}
    row["after"][judge.VALIDATOR_CONTRACT]["storage"] = {"6": "1000", "15": "625", "20": "1", "validator_index": str(int(active)),
                                                         "3": str(8385 if active else 0), "first_validator_incoming": str(8385 if active else 0)}
    return row


def election():
    row = base(judge.VALIDATOR_CONTRACT, "1e4c1524")
    row["before"][judge.VALIDATOR_CONTRACT]["storage"] = {"validator_index": "0"}
    row["after"][judge.VALIDATOR_CONTRACT]["storage"] = {"validator_index": "1", "1": "1", "3": "0", "first_validator_incoming": "0"}
    row["before"][judge.VALIDATOR_CONTRACT]["balance"] = "10000"
    row["before"][judge.REWARD]["balance"] = "100"
    row["after"][judge.REWARD]["balance"] = "10100"
    return row


class SystemControls(unittest.TestCase):
    def test_source_initializer_cases(self):
        for case in INITIALIZERS:
            with self.subTest(contract=case[0]): judge.check_system(initializer(case))

    def test_each_selected_initializer_slot_at_both_boundaries(self):
        for case in INITIALIZERS:
            original = initializer(case); to = original["to"]
            for boundary in ("before", "after"):
                for key in original[boundary][to]["storage"]:
                    row = copy.deepcopy(original)
                    row[boundary][to]["storage"][key] = str(int(row[boundary][to]["storage"][key]) + 1)
                    with self.subTest(contract=case[0], boundary=boundary, slot=key), self.assertRaises(ValueError):
                        judge.check_system(row)

    def test_common_native_system_contract(self):
        for field, value in (("type", 1), ("from", judge.SENDER), ("gas_price", "1"), ("nonce", 8),
                             ("error", "failure"), ("to", judge.SUCCESS), ("data", "deadbeef")):
            row = initializer(INITIALIZERS[0]); row[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError): judge.check_system(row)
        for change in ("status", "nonce", "sender_balance", "other_balance"):
            row = initializer(INITIALIZERS[0])
            if change == "status": row["receipt"]["status"] = 0
            if change == "nonce": row["after"][judge.VALIDATOR]["nonce"] = 11
            if change == "sender_balance": row["after"][judge.VALIDATOR]["balance"] = "1"
            if change == "other_balance": row["after"][judge.REWARD]["balance"] = "1"
            with self.subTest(change=change), self.assertRaises(ValueError): judge.check_system(row)

    def test_active_and_deprecated_deposit(self):
        for active in (False, True): judge.check_system(deposit(active))

    def test_deposit_preconditions_distribution_and_income(self):
        for active in (False, True):
            original = deposit(active)
            for boundary in ("before", "after"):
                for key in original[boundary][judge.VALIDATOR_CONTRACT]["storage"]:
                    row = copy.deepcopy(original)
                    row[boundary][judge.VALIDATOR_CONTRACT]["storage"][key] = "999999"
                    with self.subTest(active=active, boundary=boundary, key=key), self.assertRaises(ValueError): judge.check_system(row)
            for address in (judge.VALIDATOR_CONTRACT, judge.REWARD, judge.BURN):
                row = copy.deepcopy(original); row["after"][address]["balance"] = "999999"
                with self.subTest(active=active, address=address), self.assertRaises(ValueError): judge.check_system(row)
            for field, value in (("to", judge.REWARD), ("data", "f340fa01" + "0" * 64), ("value", "0")):
                row = copy.deepcopy(original); row[field] = value
                with self.subTest(active=active, field=field), self.assertRaises(ValueError): judge.check_system(row)

    def test_election_installs_active_validator_and_moves_dusk_balance(self):
        judge.check_system(election())
        for boundary in ("before", "after"):
            for key in election()[boundary][judge.VALIDATOR_CONTRACT]["storage"]:
                row = election(); row[boundary][judge.VALIDATOR_CONTRACT]["storage"][key] = "999"
                with self.subTest(boundary=boundary, key=key), self.assertRaises(ValueError): judge.check_system(row)
        for address in (judge.VALIDATOR_CONTRACT, judge.REWARD):
            row = election(); row["after"][address]["balance"] = "999"
            with self.subTest(address=address), self.assertRaises(ValueError): judge.check_system(row)


if __name__ == "__main__":
    unittest.main()
