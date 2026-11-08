"""Independent controls for the private BSC native observation boundary."""

import copy
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import Rejected
import bsc


class BscObservationTests(unittest.TestCase):
    def setUp(self):
        self.expected = json.loads(Path(__file__).with_name("fixtures").joinpath("bsc/expected.json").read_text())
        self.identities = ["0x" + char * 64 for char in "abc"]
        self.prepared = [{"number": n, "hash": self.identities[n]} for n in (1, 2)]
        rows = [self.expected["user_transaction"], *self.expected["system_transactions"]]
        transactions, receipts = [], []
        cumulative = 0
        for index, row in enumerate(rows):
            tx_hash = "0x" + f"{index + 1:064x}"
            transactions.append({**row, "hash": tx_hash, "index": index, "type": 0})
            gas = 21000 if index == 0 else 100 + index
            cumulative += gas
            receipts.append({"transaction_hash": tx_hash, "transaction_index": index,
                             "block_hash": self.identities[1], "block_number": 1,
                             "status": 1, "gas_used": gas, "cumulative_gas_used": cumulative,
                             "logs": []})
        self.observation = {"schema_version": 1, "chain_id": 714, "genesis_hash": self.identities[0],
                            "head_number": 2, "head_hash": self.identities[2], "blocks": [
            {"number": 1, "hash": self.identities[1], "parent_hash": self.identities[0],
             "state_root": "0x" + "d" * 64, "coinbase": self.expected["validator"], "difficulty": "2",
             "gas_used": cumulative, "base_fee": None, "withdrawals": 0,
             "transactions": transactions, "receipts": receipts},
            {"number": 2, "hash": self.identities[2], "parent_hash": self.identities[1],
             "state_root": "0x" + "d" * 64, "coinbase": self.expected["validator"], "difficulty": "2",
             "gas_used": 0, "base_fee": None, "withdrawals": 0, "transactions": [], "receipts": []}]}
        accounts = {address: dict(values) for address, values in self.expected["accounts"].items()}
        accounts[self.expected["validator"]]["balance"] = "123"
        self.states = [{"root": "d" * 64, "accounts": copy.deepcopy(accounts)} for _ in (1, 2)]

    def compare(self):
        return bsc.compare_native(self.expected, self.observation, self.states, self.identities, self.prepared)

    def test_independently_constructed_native_shape(self):
        result = self.compare()
        self.assertEqual(result["checked_scenarios"], ["native-transfer", "no-user-transaction-block"])
        self.assertIn("system-transaction-gas", result["unchecked"])

    def test_identity_prefix_duplicate_and_receipt_join_reject(self):
        for field, value in (("head_number", 1), ("genesis_hash", "0x" + "0" * 64),
                             ("head_hash", "0x" + "0" * 64)):
            with self.subTest(field=field):
                changed = copy.deepcopy(self.observation)
                changed[field] = value
                with self.assertRaises(Rejected):
                    bsc.compare_native(self.expected, changed, self.states, self.identities, self.prepared)
        for mutate in (
            lambda x: x["blocks"][0]["transactions"].append(x["blocks"][0]["transactions"][0]),
            lambda x: x["blocks"][0]["receipts"][1].update(transaction_hash=x["blocks"][0]["receipts"][0]["transaction_hash"]),
            lambda x: x["blocks"][0]["receipts"][0].update(gas_used=20999),
            lambda x: x["blocks"][0]["transactions"][1].update(nonce=1),
            lambda x: x["blocks"][0]["transactions"][8].update(value="0"),
            lambda x: x["blocks"][1].update(state_root="0x" + "e" * 64),
            lambda x: x["blocks"][1].update(difficulty="1"),
            lambda x: x["blocks"][1].update(base_fee="0"),
        ):
            with self.subTest(mutation=mutate):
                changed = copy.deepcopy(self.observation)
                mutate(changed)
                with self.assertRaises(Rejected):
                    bsc.compare_native(self.expected, changed, self.states, self.identities, self.prepared)

    def test_account_oracle_and_empty_block_invariant(self):
        self.states[1]["accounts"][self.expected["user_transaction"]["from"]]["balance"] = "1"
        with self.assertRaises(Rejected):
            self.compare()
        self.setUp()
        self.states[0]["accounts"][bsc.SYSTEM_ADDRESS]["balance"] = "1"
        with self.assertRaises(Rejected):
            self.compare()

    def test_false_prepared_identity_and_boolean_types_reject(self):
        self.prepared[0]["hash"] = "0x" + "0" * 64
        with self.assertRaises(Rejected):
            self.compare()
        self.setUp()
        self.observation["blocks"][0]["receipts"][0]["status"] = True
        with self.assertRaises(Rejected):
            self.compare()


if __name__ == "__main__":
    unittest.main()
