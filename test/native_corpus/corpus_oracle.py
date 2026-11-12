#!/usr/bin/env python3
"""Independent expected results of the E01-03.smoke_corpus cases.

Usage:
  corpus_oracle.py write    regenerate expected/<family>.json from this file
  corpus_oracle.py check    fail if a committed expected file differs from this file

The oracle is fixed before any native generation or run. It reads no native
output: the inputs are the transactions and genesis accounts written below,
the fork rules in `corpus_evm.py`, and the fee rules of each family as written in
ORACLE.md with their native source lines. Generated hashes, timestamps and
state roots are input bindings, never expected values.
"""

import copy
import json
import sys
from pathlib import Path

import corpus_evm as evm_ref
from corpus_evm import Account, ISTANBUL, LONDON, assemble

HERE = Path(__file__).resolve().parent
EXPECTED = HERE / "expected"

GWEI = 10 ** 9
ETHER = 10 ** 18

SENDER = "7e5f4552091a69125d5dfcb7b8c2659029395bdf"  # public test key 1; never funded on a public network
RECIPIENT = "4000000000000000000000000000000000000001"
PARENT = "3000000000000000000000000000000000000001"
CHILD = "3000000000000000000000000000000000000002"
RECEIVER = "3000000000000000000000000000000000000003"

CHILD_TOPIC = 0xC1
PARENT_TOPIC = 0xA1
RECEIVER_TOPIC = 0x5E
CHILD_WORD = 0xEE
CHILD_GAS = 30_000
NESTED_GAS = 200_000
REVERT_GAS = 100_000

# The child writes slot 0, logs, and reverts with one 32-byte word.
CHILD_CODE = assemble([
    ("PUSH1", 1), ("PUSH1", 0), "SSTORE",
    ("PUSH1", CHILD_TOPIC), ("PUSH1", 0), ("PUSH1", 0), "LOG1",
    ("PUSH1", CHILD_WORD), ("PUSH1", 0), "MSTORE",
    ("PUSH1", 32), ("PUSH1", 0), "REVERT",
])

# The parent writes slot 0, calls the child with its call value and a fixed
# CHILD_GAS (forwarding all gas would leave the parent only 1/64 after the
# call, EIP-150), then records !success (slot 1), the return data size (slot 2) and the
# returned word (slot 3), and logs its call value.
PARENT_CODE = assemble([
    ("PUSH1", 1), ("PUSH1", 0), "SSTORE",
    ("PUSH1", 32), ("PUSH1", 0), ("PUSH1", 0), ("PUSH1", 0), "CALLVALUE", ("PUSH20", int(CHILD, 16)), ("PUSH2", CHILD_GAS), "CALL",
    "ISZERO", ("PUSH1", 1), "SSTORE",
    "RETURNDATASIZE", ("PUSH1", 2), "SSTORE",
    ("PUSH1", 0), "MLOAD", ("PUSH1", 3), "SSTORE",
    "CALLVALUE", ("PUSH1", 0), "MSTORE",
    ("PUSH1", PARENT_TOPIC), ("PUSH1", 32), ("PUSH1", 0), "LOG1",
    "STOP",
])

# The Bor StateSync receiver. `onStateReceive(uint256 id, bytes data)` is
# ABI-encoded as selector | id | 0x40 | len(data) | data. The receiver keeps
# id (slot 0), its caller (slot 1), len(data) (slot 2) and the first data word
# (slot 3), and logs the id.
RECEIVER_CODE = assemble([
    ("PUSH1", 0x04), "CALLDATALOAD", ("PUSH1", 0), "SSTORE",
    "CALLER", ("PUSH1", 1), "SSTORE",
    ("PUSH1", 0x44), "CALLDATALOAD", ("PUSH1", 2), "SSTORE",
    ("PUSH1", 0x64), "CALLDATALOAD", ("PUSH1", 3), "SSTORE",
    ("PUSH1", 0x04), "CALLDATALOAD", ("PUSH1", 0), "MSTORE",
    ("PUSH1", RECEIVER_TOPIC), ("PUSH1", 32), ("PUSH1", 0), "LOG1",
    "STOP",
])

SLOTS = (0, 1, 2, 3)

# Bor
BOR_FEE_ADDRESS = "0000000000000000000000000000000000001010"
BOR_TRANSFER_SIG = 0xE6497E3EE548A3372136AF2FCB0696DB31FC6CF20260707645068BD3FE97F3C4
BOR_FEE_SIG = 0x4DFE1BBBCF077DDC3E01291EEA2D5C70C2B422B415D95645B9ADCFD678CB1D63
BOR_SIGNER = "71562b71999873db5b286df957af199ec94617f7"
BOR_BURN = "0000000000000000000000000000000000000000"
BOR_STATE_RECEIVER = "0000000000000000000000000000000000001001"
BOR_ON_STATE_RECEIVE = "26c53bea"
STATE_SYNC_DATA = b"extractor-corpus-state-sync-1"
STATE_SYNC_GAS = 5_000_000

# BSC
BSC_SYSTEM = "fffffffffffffffffffffffffffffffffffffffe"
BSC_VALIDATOR = "03735c2ed70a56cd221e0024eb4bf90243c9d6e9"
BSC_INIT_ORDER = ("0000000000000000000000000000000000001000", "0000000000000000000000000000000000001001",
                  "0000000000000000000000000000000000001003", "0000000000000000000000000000000000001006",
                  "0000000000000000000000000000000000001004", "0000000000000000000000000000000000001005",
                  "0000000000000000000000000000000000002000")
BSC_SYSTEM_REWARD = "0000000000000000000000000000000000001002"
BSC_VALIDATOR_CONTRACT = "0000000000000000000000000000000000001000"
BSC_SYSTEM_GAS = 9223372036854775807

# Ethereum
ETH_COINBASE = "2000000000000000000000000000000000000000"
ETH_REWARD = 2 * ETHER
ETH_REWARD_SOURCE = "test/harness/fixtures/ORACLE.md (E01-01.harness)"

CASE_NAMES = ("transfer", "nested-revert", "fee", "system-action", "empty-block")


def word(value):
    return f"{value:064x}"


def legacy(case, nonce, to, value, gas, gas_price):
    return {"case": case, "type": 0, "from": SENDER, "nonce": nonce, "to": to, "value": value, "gas": gas,
            "gas_price": gas_price, "data": ""}


def dynamic(case, nonce, to, value, gas, max_fee, max_priority):
    return {"case": case, "type": 2, "from": SENDER, "nonce": nonce, "to": to, "value": value, "gas": gas,
            "max_fee_per_gas": max_fee, "max_priority_fee_per_gas": max_priority, "data": ""}


def effective_price(tx, base_fee):
    if tx["type"] == 0:
        return tx["gas_price"]
    return min(tx["max_fee_per_gas"], base_fee + tx["max_priority_fee_per_gas"])


def base_fee_next(parent_base_fee, parent_gas_limit, parent_gas_used, denominator):
    """EIP-1559 with elasticity 2 (Ethereum, and Bor before Dandeli)."""
    target = parent_gas_limit // 2
    if parent_gas_used == target:
        return parent_base_fee
    if parent_gas_used > target:
        delta = parent_base_fee * (parent_gas_used - target) // target // denominator
        return parent_base_fee + max(delta, 1)
    delta = parent_base_fee * (target - parent_gas_used) // target // denominator
    return max(parent_base_fee - delta, 0)


def snapshot(state, addresses):
    out = {}
    for address in addresses:
        account = state.get(address, Account())
        row = {"balance": str(account.balance), "nonce": account.nonce, "code": account.code.hex()}
        if account.code:
            row["storage"] = {str(slot): word(account.storage.get(slot, 0)) for slot in SLOTS}
        out[address] = row
    return out


def bor_log(signature, sender, recipient, values):
    data = b"".join(value.to_bytes(32, "big") for value in values)
    return {"address": BOR_FEE_ADDRESS,
            "topics": [word(t) for t in (signature, int(BOR_FEE_ADDRESS, 16), int(sender, 16), int(recipient, 16))],
            "data": data.hex()}


def bor_transfer_hook(tx, sender, recipient, value):
    """Bor `core.Transfer`: a transfer log with the balances around the move (core/evm.go)."""
    if value <= 0:
        return
    after_sender, after_recipient = tx.state[sender].balance, tx.state[recipient].balance
    tx.logs.append(bor_log(BOR_TRANSFER_SIG, sender, recipient,
                           (value, after_sender + value, after_recipient - value, after_sender, after_recipient)))


def apply_user_tx(family, rules, state, tx, base_fee, coinbase, burn):
    """Apply one user transaction and pay its fee the way `family` does."""
    sender = state[tx["from"]]
    if sender.nonce != tx["nonce"]:
        raise ValueError(f"nonce mismatch in {tx['case']}")
    price = effective_price(tx, base_fee)
    initial_sender = sender.balance
    initial_coinbase = state.get(coinbase, Account()).balance
    sender.nonce += 1
    sender.balance -= tx["gas"] * price
    hook = bor_transfer_hook if family == "poly" else None
    result = evm_ref.execute(rules, state, tx["from"], tx["to"], tx["value"], bytes.fromhex(tx["data"]), tx["gas"],
                             hook)
    used = result["gas_used"]
    state[tx["from"]].balance += (tx["gas"] - used) * price
    tip = used * (price - base_fee if rules.london else price)
    burnt = used * base_fee if rules.london else 0
    logs = list(result["logs"])
    state.setdefault(coinbase, Account()).balance += tip
    if family == "poly":
        state.setdefault(burn, Account()).balance += burnt
        if tip > 0:
            logs.append(bor_log(BOR_FEE_SIG, tx["from"], coinbase, (tip, initial_sender, initial_coinbase,
                                                                     initial_sender - tip, initial_coinbase + tip)))
    return {"status": result["status"], "gas_used": used, "logs": logs, "frame": stringify(result["frame"]),
            "effective_gas_price": str(price),
            "fee": {"total": str(used * price), "tip": str(tip), "burnt": str(burnt), "tip_recipient": coinbase,
                    "burn_recipient": burn}}


def stringify(frame):
    out = dict(frame)
    out["value"] = str(frame["value"])
    out["calls"] = [stringify(call) for call in frame["calls"]]
    return out


def stringify_tx(tx):
    out = dict(tx)
    for key in ("value", "gas_price", "max_fee_per_gas", "max_priority_fee_per_gas"):
        if key in out:
            out[key] = str(out[key])
    return out


def run_block(family, rules, state, number, base_fee, gas_limit, txs, coinbase, burn, tracked):
    rows, cumulative = [], 0
    for index, tx in enumerate(txs):
        touched = tracked(tx)
        before = snapshot(state, touched)
        result = apply_user_tx(family, rules, state, tx, base_fee, coinbase, burn)
        cumulative += result["gas_used"]
        rows.append({"index": index, "case": tx["case"], "transaction": stringify_tx(tx),
                     "receipt": {"status": result["status"], "gas_used": result["gas_used"],
                                 "cumulative_gas_used": cumulative, "logs": result["logs"]},
                     "effective_gas_price": result["effective_gas_price"], "fee": result["fee"],
                     "frame": result["frame"], "state_before": before, "state_after": snapshot(state, touched)})
    return {"number": number, "base_fee": None if base_fee is None else str(base_fee), "gas_limit": gas_limit,
            "transactions": rows}, cumulative


def empty_block(number, base_fee, gas_limit, state, accounts, **extra):
    block = {"number": number, "base_fee": None if base_fee is None else str(base_fee), "gas_limit": gas_limit,
             "transactions": [], "tx_count": 0, "gas_used": 0, "system": [], "outside_transaction": [],
             "state_after_block": snapshot(state, accounts)}
    block.update(extra)
    return block


def genesis_accounts(extra):
    state = {SENDER: Account(balance=10 * ETHER), PARENT: Account(code=PARENT_CODE), CHILD: Account(code=CHILD_CODE)}
    state.update(extra)
    return state


def genesis_rows(with_receiver):
    rows = [{"address": SENDER, "balance": str(10 * ETHER), "code": ""},
            {"address": PARENT, "balance": "0", "code": PARENT_CODE.hex()},
            {"address": CHILD, "balance": "0", "code": CHILD_CODE.hex()}]
    if with_receiver:
        rows.append({"address": RECEIVER, "balance": "0", "code": RECEIVER_CODE.hex()})
    return rows


def case_rows(entries):
    return [{"case": name, **entries[name]} for name in CASE_NAMES]


# ---- Ethereum (Geth) ----

def ethereum():
    rules = LONDON
    state = genesis_accounts({})
    gas_limit = 10_000_000
    base1 = base_fee_next(GWEI, gas_limit, 0, 8)  # the genesis block is empty
    txs = [
        legacy("transfer", 0, RECIPIENT, ETHER, 21000, 2 * GWEI),
        dynamic("fee", 1, RECIPIENT, GWEI, 21000, 2_200_000_000, 1_500_000_000),  # the fee cap limits the tip
        legacy("nested-revert", 2, PARENT, 5, NESTED_GAS, 2 * GWEI),
        legacy("nested-revert", 3, CHILD, 7, REVERT_GAS, 2 * GWEI),
    ]
    accounts = sorted({SENDER, RECIPIENT, ETH_COINBASE, PARENT, CHILD})
    block1, used1 = run_block("eth", rules, state, 1, base1, gas_limit, txs, ETH_COINBASE, None,
                              lambda tx: accounts)
    reward = [{"kind": "block-reward", "recipient": ETH_COINBASE, "amount": str(ETH_REWARD),
               "asserted_by": ETH_REWARD_SOURCE}]
    state[ETH_COINBASE].balance += ETH_REWARD
    block1.update(tx_count=len(txs), gas_used=used1, system=[], outside_transaction=reward,
                  state_after_block=snapshot(state, accounts))
    base2 = base_fee_next(base1, gas_limit, used1, 8)
    state[ETH_COINBASE].balance += ETH_REWARD
    block2 = empty_block(2, base2, gas_limit, state, accounts, outside_transaction=reward)
    cases = case_rows({
        "transfer": {"applicable": True, "where": [[1, 0]]},
        "nested-revert": {"applicable": True, "where": [[1, 2], [1, 3]]},
        "fee": {"applicable": True, "where": [[1, 1]]},
        "system-action": {"applicable": False,
                          "reason": "DEVELOPMENT_PLAN E01-03 (line 89) asks for native system transactions; a block "
                                    "body of this London ethash fixture has none. Post-merge protocol system calls "
                                    "(EIP-4788, EIP-2935) are not transactions, and withdrawals and block rewards are "
                                    "credits outside a transaction; ORACLE.md names their owners. The block-end state "
                                    "still binds the block reward."},
        "empty-block": {"applicable": True, "where": [[2, None]]},
    })
    return {"family": "eth", "client": "geth", "chain_id": 31337,
            "fixture": {"genesis": "test/harness/fixtures/genesis.json",
                        "native_source": "go-ethereum 1df30b18d9ed35ee9d28ffd4648442c2204554ad",
                        "native_build": "the accepted patched build of this source; the observation source names it"}, "rules": rules.name, "coinbase": ETH_COINBASE,
            "genesis_base_fee": str(GWEI), "genesis_additions": genesis_rows(False), "cases": cases,
            "blocks": [block1, block2],
            "unasserted": ["state roots, hashes and timestamps (input bindings)",
                           "header fields other than number, gas limit, gas used, base fee and transaction count",
                           "accounts not listed"]}


# ---- BSC (Parlia) ----

def bsc():
    rules = ISTANBUL  # Berlin and London start at block 8 in the private fixture
    state = genesis_accounts({BSC_SYSTEM: Account()})
    txs = [
        legacy("transfer", 0, RECIPIENT, ETHER, 21000, 5 * GWEI),
        legacy("nested-revert", 1, PARENT, 5, NESTED_GAS, 5 * GWEI),
        legacy("nested-revert", 2, CHILD, 7, REVERT_GAS, 5 * GWEI),
    ]
    accounts = sorted({SENDER, RECIPIENT, PARENT, CHILD, BSC_SYSTEM})
    block1, _ = run_block("bsc", rules, state, 1, None, None, txs, BSC_SYSTEM, None, lambda tx: accounts)
    fees = state[BSC_SYSTEM].balance
    reward = fees >> 4  # systemRewardPercent = 4 (consensus/parlia/parlia.go distributeIncoming)
    system = []
    for nonce, address in enumerate(BSC_INIT_ORDER):
        system.append({"case": "system-action", "kind": "init", "from": BSC_VALIDATOR, "nonce": nonce, "to": address,
                       "value": "0", "data": "e1c7392a", "gas_price": "0", "gas": BSC_SYSTEM_GAS, "status": 1})
    nonce = len(BSC_INIT_ORDER)
    system.append({"case": "fee", "kind": "system-reward", "from": BSC_VALIDATOR, "nonce": nonce,
                   "to": BSC_SYSTEM_REWARD, "value": str(reward), "data": "", "gas_price": "0",
                   "gas": BSC_SYSTEM_GAS, "status": 1})
    system.append({"case": "fee", "kind": "validator-deposit", "from": BSC_VALIDATOR, "nonce": nonce + 1,
                   "to": BSC_VALIDATOR_CONTRACT, "value": str(fees - reward),
                   "data": "f340fa01" + "00" * 12 + BSC_VALIDATOR, "gas_price": "0", "gas": BSC_SYSTEM_GAS,
                   "status": 1})
    state[BSC_SYSTEM].balance = 0
    nonces = {BSC_VALIDATOR: nonce + 2}
    block1.update(tx_count=len(txs) + len(system), gas_used=None,
                  gas_used_reason="system transactions execute native EVM gas that this oracle does not model; "
                                  "only the user receipts' gas is asserted",
                  system=system, system_nonce_after=nonces, outside_transaction=[],
                  state_after_block=snapshot(state, accounts))
    block2 = empty_block(2, None, None, state, accounts, system_nonce_after=nonces, state_root_unchanged=True)
    cases = case_rows({
        "transfer": {"applicable": True, "where": [[1, 0]]},
        "nested-revert": {"applicable": True, "where": [[1, 1], [1, 2]]},
        "fee": {"applicable": True, "where": [[1, 0], [1, 1], [1, 2], [1, "system-reward"],
                                              [1, "validator-deposit"]]},
        "system-action": {"applicable": True, "where": [[1, "init"]]},
        "empty-block": {"applicable": True, "where": [[2, None]]},
    })
    return {"family": "bsc", "client": "bsc", "chain_id": 714,
            "fixture": {"genesis": "test/harness/fixtures/bsc_admission/genesis.json",
                        "native_source": "bsc 54afaff4da31ac9e49ba646a4f65104edac5a2f3",
                        "native_build": "the accepted patched build of this source; the observation source names it"}, "rules": rules.name, "coinbase": BSC_VALIDATOR,
            "fee_accumulator": BSC_SYSTEM, "genesis_additions": genesis_rows(False), "cases": cases,
            "blocks": [block1, block2],
            "unasserted": ["state roots, hashes and timestamps (input bindings)",
                           "system-contract storage, logs and gas, and the validator and system-contract balances",
                           "accounts not listed"]}


# ---- Polygon PoS (Bor) ----

def polygon():
    rules = LONDON  # London at block 1; Shanghai, Cancun and Prague at block 3
    state = genesis_accounts({RECEIVER: Account(code=RECEIVER_CODE), BOR_SIGNER: Account(balance=1000 * ETHER)})
    gas_limit = 20_000_000  # the first London block doubles the genesis limit of 10,000,000
    base1 = GWEI  # params.InitialBaseFee for the first London block
    txs = [
        legacy("transfer", 0, RECIPIENT, ETHER, 21000, 5 * GWEI),
        dynamic("fee", 1, RECIPIENT, GWEI, 21000, 5 * GWEI, 2 * GWEI),
        legacy("nested-revert", 2, PARENT, 5, NESTED_GAS, 5 * GWEI),
        legacy("nested-revert", 3, CHILD, 7, REVERT_GAS, 5 * GWEI),
    ]
    accounts = sorted({SENDER, RECIPIENT, BOR_SIGNER, BOR_BURN, PARENT, CHILD, RECEIVER})
    block1, used1 = run_block("poly", rules, state, 1, base1, gas_limit, txs, BOR_SIGNER, BOR_BURN,
                              lambda tx: accounts)
    block1.update(tx_count=len(txs), gas_used=used1, system=[], outside_transaction=[],
                  state_after_block=snapshot(state, accounts))
    base2 = base_fee_next(base1, gas_limit, used1, 8)
    block2 = empty_block(2, base2, gas_limit, state, accounts, state_root_unchanged=True)
    base3 = base_fee_next(base2, gas_limit, 0, 8)  # parent 2 is before Delhi
    block3 = empty_block(3, base3, gas_limit, state, accounts,
                         note="Shanghai, Cancun, Prague, Delhi, Indore, Ahmedabad and Bhilai activate here; system "
                              "actions outside a transaction at this block are not asserted")
    base4 = base_fee_next(base3, gas_limit, 0, 64)  # parent 3 is Bhilai
    # StateSync: the system address calls 0x1001 commitState, and 0x1001 calls
    # the receiver's onStateReceive(id, data) with 5,000,000 gas.
    calldata = (bytes.fromhex(BOR_ON_STATE_RECEIVE) + (1).to_bytes(32, "big") + (0x40).to_bytes(32, "big")
                + len(STATE_SYNC_DATA).to_bytes(32, "big") + STATE_SYNC_DATA.ljust(32, b"\0"))
    sync_state = copy.deepcopy(state)
    sync_state[BOR_STATE_RECEIVER] = Account(code=b"\x00")  # the caller only; its own code is not modeled here
    receiver_call = evm_ref.Transaction(rules, sync_state, BOR_STATE_RECEIVER)
    ok, _, _, frame = receiver_call.call(BOR_STATE_RECEIVER, RECEIVER, 0, calldata, STATE_SYNC_GAS)
    if not ok:
        raise ValueError("the receiver must accept the state sync")
    state[RECEIVER] = sync_state[RECEIVER]
    system = [{"case": "system-action", "kind": "state-sync",
               "event": {"id": 1, "contract": RECEIVER, "data": STATE_SYNC_DATA.hex()},
               "receiver_call": stringify(frame), "logs": receiver_call.logs, "state_receiver_last_state_id": 1}]
    block4 = empty_block(4, base4, gas_limit, state, accounts, system=system,
                         note="first sprint start: state sync calls are outside the block gas; the span commit to "
                              "0x1000 is not asserted")
    cases = case_rows({
        "transfer": {"applicable": True, "where": [[1, 0]]},
        "nested-revert": {"applicable": True, "where": [[1, 2], [1, 3]]},
        "fee": {"applicable": True, "where": [[1, 1]]},
        "system-action": {"applicable": True, "where": [[4, "state-sync"]]},
        "empty-block": {"applicable": True, "where": [[2, None]]},
    })
    return {"family": "poly", "client": "bor", "chain_id": 15001,
            "fixture": {"genesis": "tests/bor/testdata/genesis.json of the native source",
                        "native_source": "bor 3a018e38467594508ca31ebeaf804837fb92834a",
                        "native_build": "the accepted patched build of this source; the observation source names it",
                        "heimdall": "in-process stub (D98 item 6) serving only the state sync event below"}, "rules": rules.name, "coinbase": BOR_SIGNER,
            "header_coinbase": BOR_BURN, "burn_recipient": BOR_BURN, "genesis_additions": genesis_rows(True),
            "cases": cases, "blocks": [block1, block2, block3, block4],
            "unasserted": ["state roots, hashes and timestamps (input bindings)",
                           "the span commit at block 4, the StateReceiver storage other than lastStateId(), and "
                           "system actions outside a transaction at block 3", "accounts not listed"]}


FAMILIES = {"eth": ethereum, "bsc": bsc, "poly": polygon}


def render(family):
    data = {"schema_version": 1, "kind": "native-corpus-expected",
            "oracle": "Independent: corpus_oracle.py and corpus_evm.py with the rules in ORACLE.md; never derived from a "
                      "native output", **FAMILIES[family]()}
    return json.dumps(data, indent=1, sort_keys=True) + "\n"


def main(argv):
    if argv not in (["write"], ["check"]):
        print(__doc__, file=sys.stderr)
        return 2
    failures = []
    for family in FAMILIES:
        path = EXPECTED / f"{family}.json"
        text = render(family)
        if argv == ["write"]:
            EXPECTED.mkdir(exist_ok=True)
            path.write_text(text)
        elif not path.exists() or path.read_text() != text:
            failures.append(path.name)
    if failures:
        print("expected files differ from corpus_oracle.py: " + ", ".join(failures), file=sys.stderr)
        return 1
    print("expected files match corpus_oracle.py" if argv == ["check"] else "expected files written")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
