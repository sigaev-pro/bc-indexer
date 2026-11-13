"""Portable controls of the E01-03.smoke_corpus oracle, reference executor and comparator.

No native client runs here. The gas vectors are the published EIP-2200 and
EIP-3529 tables; the case totals are the hand arithmetic of ORACLE.md.
"""

import copy
import json
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

import corpus_compare as compare  # noqa: E402
import corpus_evm as evm_ref  # noqa: E402
import corpus_oracle as oracle  # noqa: E402

GWEI = 10 ** 9
ETHER = 10 ** 18


def expected(family):
    """The oracle's own output, so a changed rule is caught here and not only by the file check."""
    return json.loads(oracle.render(family))


def run_code(rules, code, original, warm=True):
    """Run `code` at a contract whose slot 0 holds `original`; return (gas used, refund)."""
    address = "00000000000000000000000000000000000000aa"
    storage = {0: original} if original else {}
    state = {address: evm_ref.Account(code=code, storage=storage)}
    tx = evm_ref.Transaction(rules, state, address)
    if warm:
        tx.warm_slots.add((address, 0))
    gas = 100_000
    output, left, reverted = tx.run(code, address, address, 0, b"", gas, 0, {"calls": []})
    return gas - left, tx.refund


# (code, gas used, refund, original) from the EIP-2200 specification.
EIP2200 = [
    ("60006000556000600055", 1612, 0, 0), ("60006000556001600055", 20812, 0, 0),
    ("60016000556000600055", 20812, 19200, 0), ("60016000556002600055", 20812, 0, 0),
    ("60016000556001600055", 20812, 0, 0), ("60006000556000600055", 5812, 15000, 1),
    ("60006000556001600055", 5812, 4200, 1), ("60006000556002600055", 5812, 0, 1),
    ("60026000556000600055", 5812, 15000, 1), ("60026000556003600055", 5812, 0, 1),
    ("60026000556001600055", 5812, 4200, 1), ("60026000556002600055", 5812, 0, 1),
    ("60016000556000600055", 5812, 15000, 1), ("60016000556002600055", 5812, 0, 1),
    ("60016000556001600055", 1612, 0, 1), ("600160005560006000556001600055", 40818, 19200, 0),
    ("600060005560016000556000600055", 10818, 19200, 1),
]

# (code, gas used, refund, original) from the EIP-3529 specification (warm slot).
EIP3529 = [
    ("60006000556000600055", 212, 0, 0), ("60006000556001600055", 20112, 0, 0),
    ("60016000556000600055", 20112, 19900, 0), ("60016000556002600055", 20112, 0, 0),
    ("60016000556001600055", 20112, 0, 0), ("60006000556000600055", 3012, 4800, 1),
    ("60006000556001600055", 3012, 2800, 1), ("60006000556002600055", 3012, 0, 1),
    ("60026000556000600055", 3012, 4800, 1), ("60026000556003600055", 3012, 0, 1),
    ("60026000556001600055", 3012, 2800, 1), ("60026000556002600055", 3012, 0, 1),
    ("60016000556000600055", 3012, 4800, 1), ("60016000556002600055", 3012, 0, 1),
    ("60016000556001600055", 212, 0, 1), ("600160005560006000556001600055", 40118, 19900, 0),
    ("600060005560016000556000600055", 5918, 7600, 1),
]


class ReferenceExecutorTests(unittest.TestCase):
    def test_eip2200_vectors(self):
        for code, used, refund, original in EIP2200:
            with self.subTest(code=code, original=original):
                self.assertEqual(run_code(evm_ref.ISTANBUL, bytes.fromhex(code), original), (used, refund))

    def test_eip3529_vectors(self):
        for code, used, refund, original in EIP3529:
            with self.subTest(code=code, original=original):
                self.assertEqual(run_code(evm_ref.LONDON, bytes.fromhex(code), original), (used, refund))

    def test_cold_slot_adds_2100(self):
        used, _ = run_code(evm_ref.LONDON, bytes.fromhex("6001600055"), 0, warm=False)
        self.assertEqual(used, 6 + 22100)

    def test_sstore_sentry(self):
        # A no-op SSTORE costs only 800, but 2300 gas left is at the EIP-2200 sentry.
        code = bytes.fromhex("6000600055")
        state = {"aa" * 20: evm_ref.Account(code=code)}
        tx = evm_ref.Transaction(evm_ref.ISTANBUL, state, "aa" * 20)
        with self.assertRaises(evm_ref.Halt):
            tx.run(code, "aa" * 20, "aa" * 20, 0, b"", 2306, 0, {"calls": []})
        _, left, _ = tx.run(code, "aa" * 20, "aa" * 20, 0, b"", 2307, 0, {"calls": []})
        self.assertEqual(left, 2307 - 6 - 800)

    def test_call_forwards_all_but_one_64th(self):
        caller, callee = "00000000000000000000000000000000000000aa", "00000000000000000000000000000000000000bb"
        code = evm_ref.assemble([("PUSH1", 0)] * 5 + [("PUSH20", int(callee, 16)), "GAS", "CALL", "STOP"])
        # The callee halts on a stack underflow and so consumes all the gas it got.
        state = {caller: evm_ref.Account(code=code), callee: evm_ref.Account(code=evm_ref.assemble(["POP"]))}
        tx = evm_ref.Transaction(evm_ref.ISTANBUL, state, caller)
        _, left, _ = tx.run(code, caller, caller, 0, b"", 100_000, 0, {"calls": []})
        remaining = 100_000 - 15 - 3 - 2 - 700
        self.assertEqual(left, remaining // 64)

    def test_recipient_is_warm(self):
        # The transaction recipient calls itself with no gas: a warm access costs 100, not 2600.
        address = "00000000000000000000000000000000000000aa"
        code = evm_ref.assemble([("PUSH1", 0)] * 5 + [("PUSH20", int(address, 16)), ("PUSH1", 0), "CALL", "STOP"])
        state = {"bb" * 20: evm_ref.Account(balance=ETHER), address: evm_ref.Account(code=code)}
        result = evm_ref.execute(evm_ref.LONDON, state, "bb" * 20, address, 0, b"", 100_000)
        self.assertEqual(result["gas_used"], 21000 + 15 + 3 + 3 + 100)

    def test_refund_is_capped(self):
        # Istanbul: clearing slot 0 costs 5000 and earns 15000, capped at half the gas used.
        address = "00000000000000000000000000000000000000aa"
        state = {"bb" * 20: evm_ref.Account(balance=ETHER),
                 address: evm_ref.Account(code=bytes.fromhex("6000600055"), storage={0: 1})}
        result = evm_ref.execute(evm_ref.ISTANBUL, state, "bb" * 20, address, 0, b"", 100_000)
        self.assertEqual((result["gas_used"], result["refund"]), (13003, 13003))

    def test_zero_value_call_to_a_missing_account_creates_nothing(self):
        state = {"bb" * 20: evm_ref.Account(balance=ETHER)}
        result = evm_ref.execute(evm_ref.LONDON, state, "bb" * 20, "cc" * 20, 0, b"", 21000)
        self.assertEqual(result["status"], 1)
        self.assertNotIn("cc" * 20, state)

    def test_unknown_opcode_is_refused(self):
        with self.assertRaises(evm_ref.Unsupported):
            run_code(evm_ref.LONDON, bytes.fromhex("6001600001"), 0)
        with self.assertRaises(evm_ref.Unsupported):
            evm_ref.disassemble(bytes.fromhex("01"))

    def test_assembler_round_trip(self):
        for code in (oracle.PARENT_CODE, oracle.CHILD_CODE, oracle.RECEIVER_CODE):
            self.assertEqual(evm_ref.assemble([
                (item.split()[0], int(item.split()[1], 16)) if item.startswith("PUSH") else item
                for item in evm_ref.disassemble(code)]), code)
        with self.assertRaises(ValueError):
            evm_ref.assemble([("PUSH1", 256)])
        with self.assertRaises(ValueError):
            evm_ref.assemble([("SSTORE", 1)])

    def test_memory_cost(self):
        self.assertEqual(evm_ref.memory_cost(1), 3)
        self.assertEqual(evm_ref.memory_cost(32), 96 + 2)

    def test_intrinsic_gas(self):
        self.assertEqual(evm_ref.intrinsic_gas(b""), 21000)
        self.assertEqual(evm_ref.intrinsic_gas(b"\0\1"), 21020)

    def test_value_call_to_an_empty_account_pays_new_account(self):
        # PUSH1 0 x4, PUSH1 1 (value), PUSH20 target, PUSH2 0, CALL, STOP
        target = "00000000000000000000000000000000000000bb"
        code = evm_ref.assemble([("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 1),
                                 ("PUSH20", int(target, 16)), ("PUSH2", 0), "CALL", "STOP"])
        caller = "00000000000000000000000000000000000000aa"
        state = {caller: evm_ref.Account(balance=1, code=code)}
        tx = evm_ref.Transaction(evm_ref.ISTANBUL, state, caller)
        _, left, _ = tx.run(code, caller, caller, 0, b"", 100_000, 0, {"calls": []})
        # 7 pushes (21), CALL 700 + 9000 + 25000. The callee has no code, so its 2300 stipend
        # comes back to the caller unused, which makes the net cost 2300 lower.
        self.assertEqual(100_000 - left, 21 + 700 + 9000 + 25000 - 2300)
        self.assertEqual(state[target].balance, 1)


class RevertJournalTests(unittest.TestCase):
    """A reverted frame restores the refund counter and the warm sets (EIP-2929)."""

    def test_revert_restores_refund_and_warm_sets(self):
        child, other = "00000000000000000000000000000000000000cc", "00000000000000000000000000000000000000dd"
        # Clear slot 0 (a refund), touch `other` with a zero-gas CALL, then revert.
        code = evm_ref.assemble([("PUSH1", 0), ("PUSH1", 0), "SSTORE",
                                 ("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 0),
                                 ("PUSH20", int(other, 16)), ("PUSH1", 0), "CALL", "POP",
                                 ("PUSH1", 0), ("PUSH1", 0), "REVERT"])
        state = {child: evm_ref.Account(code=code, storage={0: 1}), "bb" * 20: evm_ref.Account()}
        tx = evm_ref.Transaction(evm_ref.LONDON, state, "bb" * 20)
        ok, left, _, frame = tx.call("bb" * 20, child, 0, b"", 100_000)
        self.assertFalse(ok)
        self.assertEqual(frame["error"], "execution reverted")
        self.assertEqual(tx.refund, 0)
        self.assertNotIn((child, 0), tx.warm_slots)
        self.assertNotIn(other, tx.warm_accounts)
        self.assertEqual(state[child].storage, {0: 1})
        self.assertGreater(left, 0)

    def test_call_input_memory_is_charged(self):
        caller = "00000000000000000000000000000000000000aa"
        # CALL a missing account with no value and 64 bytes of input at memory 0.
        code = evm_ref.assemble([("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 64), ("PUSH1", 0), ("PUSH1", 0),
                                 ("PUSH20", 0xEE), ("PUSH1", 0), "CALL", "STOP"])
        state = {caller: evm_ref.Account(code=code)}
        tx = evm_ref.Transaction(evm_ref.ISTANBUL, state, caller)
        _, left, _ = tx.run(code, caller, caller, 0, b"", 100_000, 0, {"calls": []})
        self.assertEqual(100_000 - left, 21 + 700 + 6)  # two memory words cost 6


class HandArithmeticTests(unittest.TestCase):
    """The totals written in ORACLE.md, recomputed here by hand."""

    def tx(self, family, block, index):
        return expected(family)["blocks"][block - 1]["transactions"][index]

    def test_child_revert_totals(self):
        # London: 6 + 22100 (cold set) + 9 + 750 (LOG1) + 6 + 6 (MSTORE, 1 word) + 6 = 22883.
        self.assertEqual(self.tx("eth", 1, 3)["receipt"]["gas_used"], 21000 + 22883)
        self.assertEqual(self.tx("poly", 1, 3)["receipt"]["gas_used"], 21000 + 22883)
        # Istanbul: 6 + 20000 + 9 + 750 + 6 + 6 + 6 = 20783.
        self.assertEqual(self.tx("bsc", 1, 2)["receipt"]["gas_used"], 21000 + 20783)

    def test_nested_totals(self):
        # London: 22106 + 20 + (11603 + 30000 - 9417) + 22106 + 22105 + 22109 + 8 + 1015 = 121655.
        self.assertEqual(self.tx("eth", 1, 2)["receipt"]["gas_used"], 21000 + 121655)
        self.assertEqual(self.tx("poly", 1, 2)["receipt"]["gas_used"], 21000 + 121655)
        # Istanbul: 20006 + 20 + (9703 + 30000 - 11517) + 20006 + 20005 + 20009 + 8 + 1015 = 109255.
        self.assertEqual(self.tx("bsc", 1, 1)["receipt"]["gas_used"], 21000 + 109255)

    def test_nested_effects(self):
        for family, index in (("eth", 2), ("bsc", 1), ("poly", 2)):
            with self.subTest(family=family):
                tx = self.tx(family, 1, index)
                parent = tx["state_after"][oracle.PARENT]
                child = tx["state_after"][oracle.CHILD]
                self.assertEqual(tx["receipt"]["status"], 1)
                self.assertEqual(parent["balance"], "5")
                self.assertEqual(child["balance"], "0")
                self.assertEqual([int(parent["storage"][str(s)], 16) for s in range(4)], [1, 1, 32, 0xEE])
                self.assertEqual([int(child["storage"][str(s)], 16) for s in range(4)], [0, 0, 0, 0])
                inner = tx["frame"]["calls"]
                self.assertEqual(len(inner), 1)
                self.assertEqual(inner[0]["error"], "execution reverted")
                self.assertEqual(inner[0]["output"], "00" * 31 + "ee")
                parent_logs = [log for log in tx["receipt"]["logs"] if log["address"] == oracle.PARENT]
                self.assertEqual(parent_logs, [{"address": oracle.PARENT, "topics": ["00" * 31 + "a1"],
                                                "data": "00" * 31 + "05"}])
                self.assertFalse([log for log in tx["receipt"]["logs"] if log["address"] == oracle.CHILD])

    def test_direct_revert_keeps_value_and_charges_fee(self):
        for family, index, price in (("eth", 3, 2 * GWEI), ("bsc", 2, 5 * GWEI), ("poly", 3, 5 * GWEI)):
            with self.subTest(family=family):
                tx = self.tx(family, 1, index)
                before = tx["state_before"][oracle.SENDER]
                after = tx["state_after"][oracle.SENDER]
                used = tx["receipt"]["gas_used"]
                self.assertEqual(tx["receipt"]["status"], 0)
                self.assertEqual(int(before["balance"]) - int(after["balance"]), used * price)
                self.assertEqual(after["nonce"], before["nonce"] + 1)
                self.assertEqual(tx["state_after"][oracle.CHILD]["balance"], "0")

    def test_ethereum_balances(self):
        block = expected("eth")["blocks"][0]
        # Sender: 10 ETH - 1 ETH - 1 gwei - 5 wei - fees (42 + 46.2 + 285.31 + 87.766) * 10^12.
        fees = (42_000 + 46_200 + 285_310 + 87_766) * 10 ** 9
        self.assertEqual(int(block["state_after_block"][oracle.SENDER]["balance"]),
                         10 * ETHER - ETHER - GWEI - 5 - fees)
        # Coinbase: tips (23.625 + 27.825 + 160.486875 + 49.368375) * 10^12 plus the 2 ETH reward.
        tips = 23_625_000_000_000 + 27_825_000_000_000 + 160_486_875_000_000 + 49_368_375_000_000
        self.assertEqual(int(block["state_after_block"][oracle.ETH_COINBASE]["balance"]), 2 * ETHER + tips)
        # The fee transaction: effective price min(2.2, 0.875 + 1.5) = 2.2 gwei, tip 1.325 gwei.
        fee = self.tx("eth", 1, 1)
        self.assertEqual(fee["effective_gas_price"], str(2_200_000_000))
        self.assertEqual(fee["fee"]["tip"], str(1_325_000_000 * 21000))

    def test_base_fees(self):
        eth = expected("eth")["blocks"]
        self.assertEqual(eth[0]["base_fee"], "875000000")
        self.assertEqual(int(eth[1]["base_fee"]), 875_000_000 - 875_000_000 * (5_000_000 - 228_538) // 5_000_000 // 8)
        poly = expected("poly")["blocks"]
        self.assertEqual(poly[0]["base_fee"], str(GWEI))
        b2 = GWEI - GWEI * (10_000_000 - 228_538) // 10_000_000 // 8
        b3 = b2 - b2 * 10_000_000 // 10_000_000 // 8
        b4 = b3 - b3 * 10_000_000 // 10_000_000 // 64
        self.assertEqual([int(b["base_fee"]) for b in poly[1:]], [b2, b3, b4])

    def test_bor_logs(self):
        transfer = self.tx("poly", 1, 0)["receipt"]["logs"]
        # The E01-01 Bor oracle values for the same transfer.
        self.assertEqual([int(transfer[0]["data"][i:i + 64], 16) for i in range(0, 320, 64)],
                         [ETHER, 9_999_895_000_000_000_000, 0, 8_999_895_000_000_000_000, ETHER])
        self.assertEqual([int(transfer[1]["data"][i:i + 64], 16) for i in range(0, 320, 64)],
                         [84_000_000_000_000, 10 * ETHER, 1000 * ETHER, 9_999_916_000_000_000_000,
                          1_000_000_084_000_000_000_000])
        self.assertEqual(transfer[0]["topics"][0], f"{oracle.BOR_TRANSFER_SIG:064x}")
        self.assertEqual(transfer[1]["topics"][0], f"{oracle.BOR_FEE_SIG:064x}")
        nested = self.tx("poly", 1, 2)["receipt"]["logs"]
        self.assertEqual([log["address"] for log in nested],
                         [oracle.BOR_FEE_ADDRESS, oracle.PARENT, oracle.BOR_FEE_ADDRESS])
        reverted = self.tx("poly", 1, 3)["receipt"]["logs"]
        self.assertEqual([log["topics"][0] for log in reverted], [f"{oracle.BOR_FEE_SIG:064x}"])
        fee = self.tx("poly", 1, 1)
        self.assertEqual(fee["effective_gas_price"], str(3 * GWEI))
        self.assertEqual((fee["fee"]["tip"], fee["fee"]["burnt"]), (str(2 * GWEI * 21000), str(GWEI * 21000)))

    def test_bor_burn_and_signer(self):
        block = expected("poly")["blocks"][0]
        self.assertEqual(int(block["state_after_block"][oracle.BOR_BURN]["balance"]), 228_538 * GWEI)
        tips = (84_000 + 42_000 + 570_620 + 175_532) * 10 ** 9
        self.assertEqual(int(block["state_after_block"][oracle.BOR_SIGNER]["balance"]), 1000 * ETHER + tips)
        # Sender: 10 coins - 1 coin - 1 gwei - 5 wei - fees (105 + 63 + 713.275 + 219.415) * 10^12.
        fees = (105_000 + 63_000 + 713_275 + 219_415) * 10 ** 9
        self.assertEqual(int(block["state_after_block"][oracle.SENDER]["balance"]),
                         10 * ETHER - ETHER - GWEI - 5 - fees)

    def test_bsc_fee_distribution(self):
        block = expected("bsc")["blocks"][0]
        fees = (21000 + 130255 + 41783) * 5 * GWEI
        system = {row["kind"]: row for row in block["system"] if row["kind"] != "init"}
        self.assertEqual(int(system["system-reward"]["value"]), fees // 16)
        self.assertEqual(int(system["validator-deposit"]["value"]), fees - fees // 16)
        self.assertEqual([row["nonce"] for row in block["system"]], list(range(9)))
        self.assertEqual(block["state_after_block"][oracle.BSC_SYSTEM]["balance"], "0")
        self.assertEqual(block["tx_count"], 12)

    def test_state_sync(self):
        block = expected("poly")["blocks"][3]
        sync = block["system"][0]
        receiver = block["state_after_block"][oracle.RECEIVER]["storage"]
        self.assertEqual(int(receiver["0"], 16), 1)
        self.assertEqual(receiver["1"], "00" * 12 + oracle.BOR_STATE_RECEIVER)
        self.assertEqual(int(receiver["2"], 16), len(oracle.STATE_SYNC_DATA))
        self.assertEqual(bytes.fromhex(receiver["3"]), oracle.STATE_SYNC_DATA.ljust(32, b"\0"))
        self.assertEqual(sync["logs"], [{"address": oracle.RECEIVER, "topics": ["00" * 31 + "5e"],
                                         "data": "00" * 31 + "01"}])
        self.assertEqual(block["gas_used"], 0)

    def test_oracle_files_match(self):
        result = subprocess.run([sys.executable, str(HERE / "corpus_oracle.py"), "check"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        for family in oracle.FAMILIES:
            self.assertEqual(oracle.render(family), (HERE / "expected" / f"{family}.json").read_text())

    def test_every_family_lists_every_case(self):
        for family in oracle.FAMILIES:
            with self.subTest(family=family):
                names = [case["case"] for case in expected(family)["cases"]]
                self.assertEqual(names, list(oracle.CASE_NAMES))
        eth = {case["case"]: case for case in expected("eth")["cases"]}
        self.assertFalse(eth["system-action"]["applicable"])
        self.assertIn("system transaction", eth["system-action"]["reason"])


def synthetic(family):
    """A perfect observation built from the expected file, as a control for the comparator."""
    data = expected(family)
    blocks, root = [], None
    for block in data["blocks"]:
        number = block["number"]
        root = root if block.get("state_root_unchanged") else f"{number:064x}"
        transactions = []
        for tx in block["transactions"]:
            row = {key: value for key, value in tx["transaction"].items() if key != "case"}
            row.update(index=tx["index"], hash=f"{number:032x}{tx['index']:032x}",
                       receipt={**tx["receipt"], "effective_gas_price": tx["effective_gas_price"]},
                       frame=copy.deepcopy(tx["frame"]), state_before=copy.deepcopy(tx["state_before"]),
                       state_after=copy.deepcopy(tx["state_after"]))
            transactions.append(row)
        system = [dict({key: row[key] for key in compare.SYSTEM_FIELDS}, index=len(transactions) + offset)
                  for offset, row in enumerate(r for r in block["system"] if "from" in r)]
        syncs = [{"id": row["event"]["id"], "logs": copy.deepcopy(row["logs"]),
                  "state_receiver_last_state_id": row["state_receiver_last_state_id"]}
                 for row in block["system"] if row["kind"] == "state-sync"]
        blocks.append({"number": number, "hash": f"{number + 100:064x}", "state_root": root,
                       "base_fee": block["base_fee"], "gas_limit": block["gas_limit"] or 30_000_000,
                       "gas_used": block["gas_used"] if block["gas_used"] is not None else 999_999,
                       "body_tx_count": block["tx_count"], "transactions": transactions,
                       "system_transactions": system, "state_sync": syncs,
                       "system_nonce_after": dict(block.get("system_nonce_after", {})),
                       "state_after_block": copy.deepcopy(block["state_after_block"])})
    return {"schema_version": 1, "kind": "native-corpus-observation", "family": family, "chain_id": data["chain_id"],
            "source": {"tool": "synthetic control"}, "blocks": blocks,
            "decisions": [{"block": block["number"], "kind": "IMPORT"} for block in data["blocks"]],
            "reimport": {"new_observations": 0, "new_decisions": 0}}


class ComparatorTests(unittest.TestCase):
    def verdicts(self, family, observation):
        report = compare.compare(expected(family), observation)
        return report, {case["case"]: case["verdict"] for case in report["cases"]}

    def test_perfect_observations_pass(self):
        for family in oracle.FAMILIES:
            with self.subTest(family=family):
                report, verdicts = self.verdicts(family, synthetic(family))
                self.assertEqual(report["result"], "PASS", report["differences"])
                applicable = {c["case"] for c in expected(family)["cases"] if c["applicable"]}
                self.assertEqual({k for k, v in verdicts.items() if v == "PASS"}, applicable)
        _, eth = self.verdicts("eth", synthetic("eth"))
        self.assertEqual(eth["system-action"], "NOT_APPLICABLE")

    def assert_fails(self, family, mutate, failing, passing=()):
        observation = synthetic(family)
        mutate(observation)
        report, verdicts = self.verdicts(family, observation)
        self.assertEqual(report["result"], "FAIL")
        self.assertTrue(report["differences"])
        for case in failing:
            self.assertEqual(verdicts[case], "FAIL", case)
        for case in passing:
            self.assertEqual(verdicts[case], "PASS", case)
        return report

    def test_transaction_mismatches(self):
        def tx(observation, block, index):
            return observation["blocks"][block - 1]["transactions"][index]

        cases = [
            (lambda o: tx(o, 1, 0)["receipt"].update(status=0), ["transfer"], ["nested-revert", "fee"]),
            (lambda o: tx(o, 1, 0).update(value="1"), ["transfer"], ["fee"]),
            (lambda o: tx(o, 1, 1)["receipt"].update(effective_gas_price="1"), ["fee"], ["transfer"]),
            (lambda o: tx(o, 1, 1)["receipt"].update(gas_used=21001), ["fee"], ["nested-revert"]),
            (lambda o: tx(o, 1, 2)["receipt"]["logs"].pop(), ["nested-revert"], ["transfer"]),
            (lambda o: tx(o, 1, 2)["frame"]["calls"][0].update(error=None), ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 2)["frame"]["calls"].clear(), ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 2)["state_after"][oracle.CHILD]["storage"].update({"0": "00" * 31 + "01"}),
             ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 3)["state_after"][oracle.SENDER].update(nonce=3), ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 2)["state_after"][oracle.PARENT].update(code="00"), ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 3)["state_before"].pop(oracle.SENDER), ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 2).update(gas=100_000), ["nested-revert"], ["fee"]),
            (lambda o: tx(o, 1, 2)["receipt"].update(cumulative_gas_used=1), ["nested-revert"], ["fee"]),
        ]
        for mutate, failing, passing in cases:
            with self.subTest(failing=failing):
                self.assert_fails("eth", mutate, failing, passing)

    def test_block_mismatches_fail_the_block(self):
        cases = [
            (lambda o: o["blocks"][1].update(body_tx_count=1), ["empty-block"], ["transfer"]),
            (lambda o: o["blocks"][1].update(base_fee="1"), ["empty-block"], ["transfer"]),
            (lambda o: o["blocks"][1].update(gas_used=1), ["empty-block"], ["transfer"]),
            (lambda o: o["blocks"][1].update(gas_limit=1), ["empty-block"], ["transfer"]),
            (lambda o: o["blocks"][1]["state_after_block"][oracle.ETH_COINBASE].update(balance="1"),
             ["empty-block"], ["transfer"]),
            (lambda o: o["blocks"][0]["transactions"].pop(), ["transfer", "fee"], ["empty-block"]),
            (lambda o: o["decisions"].pop(), ["empty-block"], ["transfer"]),
            (lambda o: o["decisions"].append({"block": 1, "kind": "IMPORT"}), ["transfer"], ["empty-block"]),
        ]
        for mutate, failing, passing in cases:
            with self.subTest(failing=failing):
                self.assert_fails("eth", mutate, failing, passing)

    def test_whole_observation_mismatches_fail_every_case(self):
        names = ["transfer", "nested-revert", "fee", "empty-block"]
        for mutate in (lambda o: o.update(chain_id=1), lambda o: o.update(family="bsc"),
                       lambda o: o["reimport"].update(new_observations=1),
                       lambda o: o["reimport"].update(new_decisions=1),
                       lambda o: o["blocks"].pop()):
            with self.subTest():
                self.assert_fails("eth", mutate, names)

    def test_a_decision_of_an_unknown_block_fails(self):
        for row in ({"block": 0, "kind": "REJECT"}, {"block": 3, "kind": "IMPORT"}):
            with self.subTest(row=row):
                observation = synthetic("eth")
                observation["decisions"].append(row)
                report, verdicts = self.verdicts("eth", observation)
                self.assertEqual(report["result"], "FAIL")
                self.assertEqual(verdicts["transfer"], "FAIL")

    def test_a_difference_outside_every_case_still_fails(self):
        # Block 3 of POLY belongs to no case; its difference fails the result, not a case.
        observation = synthetic("poly")
        observation["blocks"][2]["base_fee"] = "1"
        report, verdicts = self.verdicts("poly", observation)
        self.assertEqual(report["result"], "FAIL")
        self.assertEqual(set(verdicts.values()), {"PASS"})

    def test_bsc_system_transactions(self):
        def system(o):
            return o["blocks"][0]["system_transactions"]
        cases = [
            (lambda o: system(o)[7].update(value="1"), ["fee"], ["system-action", "transfer"]),
            (lambda o: system(o)[8].update(data=""), ["fee"], ["system-action"]),
            (lambda o: system(o)[0].update(to="00" * 19 + "01"), ["system-action"], ["fee", "transfer"]),
            (lambda o: system(o)[3].update(status=0), ["system-action"], ["transfer"]),
            (lambda o: system(o)[2].update(nonce=9), ["system-action"], ["transfer"]),
            (lambda o: system(o)[1].update(index=1), ["system-action"], ["transfer"]),
            (lambda o: system(o).pop(), ["system-action", "fee", "transfer"], ["empty-block"]),
            (lambda o: o["blocks"][0]["system_nonce_after"].clear(), ["transfer", "fee"], ["empty-block"]),
            (lambda o: o["blocks"][1].update(state_root="ff" * 32), ["empty-block"], ["transfer"]),
        ]
        for mutate, failing, passing in cases:
            with self.subTest(failing=failing):
                self.assert_fails("bsc", mutate, failing, passing)

    def test_state_sync_mismatches(self):
        def sync(o):
            return o["blocks"][3]["state_sync"]
        cases = [
            (lambda o: sync(o)[0].update(id=2), ["system-action"], ["transfer", "empty-block"]),
            (lambda o: sync(o)[0]["logs"].clear(), ["system-action"], ["transfer"]),
            (lambda o: sync(o)[0].update(state_receiver_last_state_id=0), ["system-action"], ["transfer"]),
            (lambda o: sync(o).clear(), ["system-action"], ["transfer"]),
            (lambda o: o["blocks"][3]["state_after_block"][oracle.RECEIVER]["storage"].update({"3": "00" * 32}),
             ["system-action"], ["transfer"]),
            (lambda o: o["blocks"][1].update(state_root="ff" * 32), ["empty-block"], ["transfer", "system-action"]),
            (lambda o: o["blocks"][0]["transactions"][0]["receipt"]["logs"].reverse(), ["transfer"],
             ["fee", "system-action"]),
        ]
        for mutate, failing, passing in cases:
            with self.subTest(failing=failing):
                self.assert_fails("poly", mutate, failing, passing)

    def test_format_errors(self):
        cases = [
            lambda o: o.update(schema_version=2),
            lambda o: o.update(kind="other"),
            lambda o: o.update(family="tron"),
            lambda o: o.update(chain_id="1"),
            lambda o: o.update(source={}),
            lambda o: o.update(blocks=[]),
            lambda o: o["blocks"][0].update(hash="0x" + "00" * 31),
            lambda o: o["blocks"][0].update(base_fee="0x1"),
            lambda o: o["blocks"][0].update(gas_used="1"),
            lambda o: o["blocks"][0].pop("state_sync"),
            lambda o: o["blocks"][0]["transactions"][0].update(hash="AB" * 32),
            lambda o: o["blocks"][0]["transactions"][0]["receipt"].update(status="1"),
            lambda o: o["blocks"][0]["transactions"][0]["receipt"].update(effective_gas_price="01"),
            lambda o: o["blocks"][0]["transactions"][2]["receipt"]["logs"][0].update(extra=1),
            lambda o: o["blocks"][0]["transactions"][2]["frame"]["calls"][0].update(value="0x5"),
            lambda o: o["blocks"][0]["transactions"][2]["frame"].pop("error"),
            lambda o: o["blocks"][0]["transactions"][0]["state_after"][oracle.SENDER].update(balance=1),
            lambda o: o["blocks"][0]["transactions"][2]["state_after"][oracle.PARENT]["storage"].update({"0": "1"}),
            lambda o: o["blocks"][0]["state_after_block"].update(
                {"0x" + oracle.SENDER: dict(o["blocks"][0]["state_after_block"][oracle.SENDER])}),
            lambda o: o["blocks"][0]["transactions"][0]["state_after"].update(
                {oracle.SENDER.upper(): dict(o["blocks"][0]["transactions"][0]["state_after"][oracle.SENDER])}),
            lambda o: o.pop("decisions"),
            lambda o: o["decisions"].append("IMPORT"),
            lambda o: o["decisions"].append({"block": True, "kind": "IMPORT"}),
            lambda o: o["blocks"][0].update(body_tx_count=True),
            lambda o: o["blocks"][0].update(body_tx_count="4"),
            lambda o: o["blocks"][0]["transactions"][0]["receipt"].update(status=True),
            lambda o: o["blocks"][0].update(base_fee="\u00b2"),
            lambda o: o["blocks"][0]["transactions"][2]["frame"].update(type="call"),
            lambda o: o["blocks"][0]["transactions"][2]["frame"].update(to="0x" + oracle.PARENT),
            lambda o: o["blocks"][0]["transactions"][2]["frame"].update(**{"from": oracle.SENDER[:-2]}),
            lambda o: o["blocks"][0]["transactions"][2]["frame"].update(error=1),
            lambda o: o["blocks"][0]["transactions"][0]["state_after"][oracle.SENDER].update(nonce="4"),
            lambda o: o["blocks"][0]["transactions"][0]["state_after"][oracle.SENDER].update(nonce=-1),
            lambda o: o["blocks"][0]["transactions"][2]["state_after"][oracle.PARENT].update(code="0x60"),
            lambda o: o["blocks"][0]["system_transactions"].append({"index": 9}),
            lambda o: o["blocks"][0]["state_sync"].append({"id": "1", "logs": []}),
            lambda o: o["blocks"][0]["state_sync"].append({"id": 1, "logs": [{"address": "00"}]}),
            lambda o: o["reimport"].pop("new_decisions"),
        ]
        for number, mutate in enumerate(cases):
            with self.subTest(number=number):
                observation = synthetic("eth")
                mutate(observation)
                with self.assertRaises(compare.FormatError):
                    compare.compare(expected("eth"), observation)

    def test_command_line(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            good = Path(directory) / "good.json"
            good.write_text(json.dumps(synthetic("eth")))
            report = Path(directory) / "report.json"
            args = [sys.executable, str(HERE / "corpus_compare.py"), str(HERE / "expected" / "eth.json"), str(good)]
            result = subprocess.run(args + ["--report", str(report)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertEqual(json.loads(report.read_text())["result"], "PASS")
            again = subprocess.run(args + ["--report", str(report)], capture_output=True, text=True)
            self.assertNotEqual(again.returncode, 0)  # an existing report is never overwritten
            bad = synthetic("eth")
            bad["blocks"][0]["transactions"][0]["receipt"]["status"] = 0
            good.write_text(json.dumps(bad))
            self.assertEqual(subprocess.run(args, capture_output=True).returncode, 1)
            bad["schema_version"] = 3
            good.write_text(json.dumps(bad))
            failed = subprocess.run(args, capture_output=True, text=True)
            self.assertEqual(failed.returncode, 1)
            self.assertIn("format_error", json.loads(failed.stdout))
            self.assertEqual(subprocess.run(args[:3], capture_output=True).returncode, 2)
            good.write_text("{not json")
            unreadable = subprocess.run(args, capture_output=True, text=True)
            self.assertEqual(unreadable.returncode, 1)
            self.assertIn("format_error", json.loads(unreadable.stdout))


if __name__ == "__main__":
    # The mutant table runs with --regenerate: the expected files are first rewritten from the
    # (mutated) oracle, so the file check cannot kill an oracle or executor mutant and only the
    # hand arithmetic and unit tests can.
    if "--regenerate" in sys.argv:
        sys.argv.remove("--regenerate")
        for name in oracle.FAMILIES:
            (HERE / "expected" / f"{name}.json").write_text(oracle.render(name))
    unittest.main()
