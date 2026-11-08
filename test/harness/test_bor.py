"""Portable controls for the private Bor imported-chain comparison seam."""

import copy
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import Rejected
import bor


class BorObservationTests(unittest.TestCase):
    def setUp(self):
        self.expected = json.loads(Path(__file__).with_name("fixtures").joinpath("bor/expected.json").read_text())
        self.identities = ["0x" + c * 64 for c in "abc"]
        self.config = "d" * 64
        self.prepared = [{"number": n, "hash": self.identities[n], "timestamp": 100 + n} for n in (1, 2)]
        tx = {**self.expected["user_transaction"], "hash": "0x" + "1" * 64, "index": 0, "type": 0}
        logs = [{**copy.deepcopy(row), "transaction_hash": tx["hash"], "transaction_index": 0,
                 "block_hash": self.identities[1], "block_number": 1, "log_index": n, "removed": False}
                for n, row in enumerate(self.expected["logs"])]
        receipt = {"transaction_hash": tx["hash"], "transaction_index": 0, "block_hash": self.identities[1],
                   "block_number": 1, "status": 1, "type": 0, "gas_used": 21000, "cumulative_gas_used": 21000,
                   "effective_gas_price": "5000000000", "contract_address": None, "logs": logs}
        self.observed = {"schema_version": 1, "chain_id": 15001, "genesis_hash": self.identities[0],
                         "config_sha256": self.config, "head_number": 2, "head_hash": self.identities[2], "blocks": []}
        for n, row in enumerate(self.expected["blocks"], 1):
            self.observed["blocks"].append({"number": n, "hash": self.identities[n], "parent_hash": self.identities[n - 1],
                "state_root": "0x" + "e" * 64, "timestamp": 100 + n, "coinbase": "0x" + "0" * 40,
                "author": self.expected["validator"], "gas_limit": row["gas_limit"], "gas_used": row["gas_used"],
                "base_fee": row["base_fee"], "withdrawals": 0, "transactions": [tx] if n == 1 else [],
                "receipts": [receipt] if n == 1 else []})
        self.states = [{"root": "e" * 64, "accounts": copy.deepcopy(self.expected["accounts"])} for _ in range(2)]

    def compare(self):
        bor.compare_native(self.expected, self.observed, self.states, self.identities, self.prepared, self.config)

    def test_fixed_fee_arithmetic_and_complete_positive(self):
        sender = self.expected["user_transaction"]["from"]
        self.assertEqual(10**19 - 10**18 - 21000 * 5 * 10**9, int(self.expected["accounts"][sender]["balance"]))
        self.assertEqual(875262500, 10**9 - (10**9 * (10000000 - 21000) // 10000000 // 8))
        self.assertEqual([21000, 0], [row["gas_used"] for row in self.expected["blocks"]])
        self.compare()

    def test_header_chain_and_config_are_bound(self):
        for key, value in (("genesis_hash", "0x" + "0" * 64), ("head_hash", self.identities[1]),
                           ("head_number", 1), ("chain_id", 137), ("config_sha256", "0" * 64)):
            with self.subTest(key=key):
                self.setUp(); self.observed[key] = value
                with self.assertRaises(Rejected): self.compare()
        self.setUp(); self.prepared[0]["hash"] = "0x" + "0" * 64
        with self.assertRaises(Rejected): self.compare()

    def test_native_author_fee_and_empty_block_are_distinct(self):
        cases = [(0, "author", "0x" + "0" * 40), (0, "coinbase", self.expected["validator"]),
                 (0, "base_fee", "0"), (1, "base_fee", "875000000"), (0, "gas_limit", 10000000),
                 (1, "gas_used", 21000), (1, "transactions", [self.observed["blocks"][0]["transactions"][0]]),
                 (1, "parent_hash", self.identities[0]), (1, "timestamp", 101), (1, "withdrawals", 1)]
        for n, key, value in cases:
            with self.subTest(key=key, block=n):
                self.setUp(); self.observed["blocks"][n][key] = value
                with self.assertRaises(Rejected): self.compare()

    def test_receipt_and_both_log_payloads_are_joined(self):
        for field, value in (("transaction_hash", self.identities[0]), ("gas_used", 0), ("status", 0),
                             ("effective_gas_price", "4000000000"), ("transaction_index", True)):
            with self.subTest(field=field):
                self.setUp(); self.observed["blocks"][0]["receipts"][0][field] = value
                with self.assertRaises(Rejected): self.compare()
        for mutation in ("missing", "reordered", "data", "index", "removed"):
            with self.subTest(mutation=mutation):
                self.setUp(); logs = self.observed["blocks"][0]["receipts"][0]["logs"]
                if mutation == "missing": logs.pop()
                elif mutation == "reordered": logs.reverse()
                elif mutation == "data": logs[1]["data"] = logs[0]["data"]
                elif mutation == "index": logs[1]["log_index"] = 0
                else: logs[1]["removed"] = True
                with self.assertRaises(Rejected): self.compare()

    def test_state_is_complete_and_empty_block_preserves_root(self):
        for mutation in ("missing", "balance", "nonce", "duplicate", "root"):
            with self.subTest(mutation=mutation):
                self.setUp(); accounts = self.states[0]["accounts"]; address = self.expected["validator"]
                if mutation == "missing": del accounts[address]
                elif mutation == "balance": accounts[address]["balance"] = "1000000000000000000000"
                elif mutation == "nonce": accounts[address]["nonce"] = 1
                elif mutation == "duplicate": accounts[address.upper()] = accounts[address]
                else:
                    self.states[1]["root"] = "f" * 64
                    self.observed["blocks"][1]["state_root"] = "0x" + "f" * 64
                with self.assertRaises(Rejected): self.compare()


if __name__ == "__main__":
    unittest.main()
