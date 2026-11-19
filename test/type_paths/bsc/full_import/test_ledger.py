"""Independent small ledger cases; native execution is checked separately."""
import copy
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bsc_full_import_ledger_controls", HERE / "judge.py")
judge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(judge)
FUND = 100000 * 10**18
STAKE = 2001 * 10**18


def fixtures():
    accounts = judge.ledger_addresses()
    genesis = {"alloc": {judge.SENDER[2:]: {"balance": str(FUND)}}}
    state = {address: {"balance": "0", "nonce": 0} for address in accounts["all"]}
    state[judge.SENDER]["balance"] = str(FUND)
    rows = []

    def save(before, system, phase, data="", kind=0, gas=0, value=0):
        rows.append((dict(before=before, after=copy.deepcopy(state), system=system, type=kind, data=data,
                          value=str(value), receipt={"gasUsed": gas}), phase))

    before = copy.deepcopy(state)
    state[judge.VALIDATOR]["nonce"] = 1
    save(before, True, "warmup", judge.INIT)

    before = copy.deepcopy(state)
    state[judge.SENDER] = {"balance": str(FUND - STAKE - 200000000000000), "nonce": 1}
    state[judge.SYSTEM]["balance"] = "200000000000000"
    state[judge.CREDIT] = {"balance": str(STAKE), "nonce": 1}
    state[judge.STAKE_HUB]["nonce"] = 1
    save(before, False, "registration", gas=100000, value=STAKE)

    # OnTxStart sees the protocol fee sweep already applied.
    state[judge.SYSTEM]["balance"] = "0"
    state[judge.VALIDATOR]["balance"] = "200000000000000"
    before = copy.deepcopy(state)
    state[judge.VALIDATOR] = {"balance": "0", "nonce": 2}
    state[judge.VALIDATOR_CONTRACT]["balance"] = "167500000000000"
    state[judge.REWARD]["balance"] = "12500000000000"
    state[judge.BURN]["balance"] = "20000000000000"
    save(before, True, "registration", judge.DEPOSIT, value=200000000000000)

    before = copy.deepcopy(state)
    state[judge.VALIDATOR]["nonce"] = 3
    state[judge.VALIDATOR_CONTRACT]["balance"] = "0"
    state[judge.REWARD]["balance"] = "180000000000000"
    save(before, True, "warmup", judge.ELECTION)

    before = copy.deepcopy(state)
    state[judge.SENDER] = {"balance": str(FUND - STAKE - 250000000000001), "nonce": 2}
    state[judge.SYSTEM]["balance"] = "50000000000000"
    state[judge.AUTHORITY] = {"balance": "1", "nonce": 1}
    save(before, False, "success", kind=4, gas=50000, value=1)

    before = copy.deepcopy(state)
    state[judge.SENDER] = {"balance": str(FUND - STAKE - 310000000000001), "nonce": 3}
    state[judge.SYSTEM]["balance"] = "110000000000000"
    state[judge.AUTHORITY]["nonce"] = 2
    save(before, False, "revert", kind=4, gas=60000, value=1)

    before = copy.deepcopy(state)
    state[judge.SENDER] = {"balance": str(FUND - STAKE - 340000000131074), "nonce": 4}
    state[judge.SYSTEM]["balance"] = "140000000131072"
    state[judge.SUCCESS]["balance"] = "1"
    save(before, False, "success", kind=3, gas=30000, value=1)
    return genesis, accounts, rows


def check(rows):
    genesis, addresses, _ = fixtures()
    ledger = judge.ledger_module.BalanceLedger(genesis, addresses, judge.q)
    for row, phase in rows:
        ledger.apply(row, phase)
    return ledger


class LedgerControls(unittest.TestCase):
    def test_absolute_genesis_fees_stake_sweep_election_and_authorization(self):
        ledger = check(fixtures()[2])
        self.assertEqual(ledger.balances[judge.SENDER], FUND - STAKE - 340000000131074)
        self.assertEqual(ledger.balances[judge.CREDIT], STAKE)
        self.assertEqual(ledger.nonces[judge.AUTHORITY], 2)

    def test_every_account_balance_and_nonce_at_both_boundaries(self):
        original = fixtures()[2]
        for index in range(len(original)):
            for boundary in ("before", "after"):
                for address in original[index][0][boundary]:
                    for field in ("balance", "nonce"):
                        rows = copy.deepcopy(original)
                        current = rows[index][0][boundary][address][field]
                        rows[index][0][boundary][address][field] = int(current) + 1
                        with self.subTest(index=index, boundary=boundary, address=address, field=field), self.assertRaises(ValueError):
                            check(rows)

    def test_correlated_balances_cannot_reset_between_transactions(self):
        rows = fixtures()[2]
        for boundary in ("before", "after"):
            account = rows[4][0][boundary][judge.SENDER]
            account["balance"] = str(int(account["balance"]) + 1000)
        with self.assertRaisesRegex(ValueError, "before absolute balance"):
            check(rows)

    def test_missing_protocol_sweep_is_rejected(self):
        rows = fixtures()[2]
        rows[2][0]["before"][judge.SYSTEM]["balance"] = "200000000000000"
        rows[2][0]["before"][judge.VALIDATOR]["balance"] = "0"
        with self.assertRaisesRegex(ValueError, "before absolute balance"):
            check(rows)

    def test_unexpected_account_and_boolean_nonce_are_rejected(self):
        for option in ("extra", "missing", "bool"):
            rows = fixtures()[2]
            state = rows[0][0]["after"]
            if option == "extra": state["0x" + "9" * 40] = {"balance": "0", "nonce": 0}
            if option == "missing": del state[judge.REWARD]
            if option == "bool": state[judge.VALIDATOR]["nonce"] = True
            with self.subTest(option=option), self.assertRaises(ValueError):
                check(rows)

    def test_partial_deposit_cannot_leave_validator_fees(self):
        rows = fixtures()[2][:3]
        row = rows[2][0]
        row["value"] = str(int(row["value"]) - 10000)
        row["after"][judge.VALIDATOR]["balance"] = "10000"
        for address, reduction in ((judge.REWARD, 625), (judge.BURN, 1000), (judge.VALIDATOR_CONTRACT, 8375)):
            row["after"][address]["balance"] = str(int(row["after"][address]["balance"]) - reduction)
        with self.assertRaisesRegex(ValueError, "consume the expected fee balance"):
            check(rows)

    def test_impossible_transfers_and_unmodelled_dispatch(self):
        genesis, addresses, rows = fixtures()
        for amount in (-1, FUND + 1):
            ledger = judge.ledger_module.BalanceLedger(genesis, addresses, judge.q)
            with self.subTest(amount=amount), self.assertRaises(ValueError):
                ledger.move(judge.SENDER, judge.REWARD, amount)
        for field, value in (("data", "deadbeef"),):
            bad = copy.deepcopy(rows[:1]); bad[0][0][field] = value
            with self.assertRaisesRegex(ValueError, "unmodelled ledger system"):
                check(bad)
        for kind in (True, 5):
            bad = copy.deepcopy(rows[:2]); bad[1][0]["type"] = kind
            with self.assertRaisesRegex(ValueError, "invalid fixture user type"):
                check(bad)
        bad = copy.deepcopy(rows[:2]); bad[1] = (bad[1][0], "warmup")
        with self.assertRaisesRegex(ValueError, "unmodelled ledger user phase"):
            check(bad)


if __name__ == "__main__":
    unittest.main()
