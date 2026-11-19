"""Strict private type 1/2/4 crossing proof; no public-profile gate credit."""

import argparse
import copy
import importlib.util
import json
from pathlib import Path

_spec = importlib.util.spec_from_file_location("crossing_expected_helpers", Path(__file__).with_name("crossing_helpers.py"))
h = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(h)
prior, codec = h.prior, h.codec
check, octets, integer, quantity = h.check, h.octets, h.integer, h.quantity
CASES = (("Berlin", "berlinBlock", 1, 0, 0), ("London", "londonBlock", 2, 1, 0), ("Prague", "pragueBlock", 4, 4, 4))
SIGNER = "71562b71999873db5b286df957af199ec94617f7"
INITIAL_CODE_HASHES = {
    prior.SENDER: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
    prior.AUTHORITY: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
    prior.SUCCESS: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
    prior.REVERT: "5cf04cb8c25808b54e86ef1f67aefd4648843b8233bf76efccfcc2e415f749f1",
    prior.TARGET: "2baa572e83d64a532a4358ed7cc1743dfea2794b392bc3dc35eb500e66a4850a",
    prior.RECEIVER: "710f0472f8b6b03ec96db2536b77758501cd97da610bc1bb2a522eccb3a7aced",
}


def legacy_state(state, sender_after):
    expected = {address: {"nonce": 0, "balance": 0, "storage": (0, 0, 0, 0), "code_hash": digest}
                for address, digest in INITIAL_CODE_HASHES.items()}
    expected[prior.SENDER].update(nonce=1, balance=sender_after)
    expected[prior.SUCCESS]["balance"] = 7
    actual = {address: {"nonce": row["nonce"], "balance": row["balance"], "storage": row["storage"],
                        "code_hash": codec.keccak256(row["code"]).hex()} for address, row in state.items()}
    check(actual == expected, "crossing legacy state differs from source genesis")


def receipt_oracle(kind, base_fee, sender_before, recipient_before, signer_before):
    # Pinned core/state_transition.go: 21000 plus 25000 for a newly created
    # authorization authority. The absent authority earns no existing-account
    # refund. Empty calldata adds no EVM execution gas to this plain transfer.
    gas = 46000 if kind == 4 else 21000
    gas_limit = 100000 if kind == 4 else 21000
    price = base_fee + 2_000_000_000 if kind in (2, 4) else 5_000_000_000
    bought, tip = gas_limit * price, gas * (price - base_fee)
    address = octets("0000000000000000000000000000000000001010")
    sender = octets(prior.SENDER).rjust(32, b"\x00")
    signatures = ("e6497e3ee548a3372136af2fcb0696db31fc6cf20260707645068bd3fe97f3c4", "4dfe1bbbcf077ddc3e01291eea2d5c70c2b422b415d95645b9adcfd678cb1d63")
    amounts = ((7, sender_before - bought, recipient_before, sender_before - bought - 7, recipient_before + 7),
               (tip, sender_before, signer_before, sender_before - tip, signer_before + tip))
    logs = [[address, [octets(signature), address.rjust(32, b"\x00"), sender, target],
             b"".join(value.to_bytes(32, "big") for value in values)]
            for signature, target, values in zip(signatures, (octets(prior.SUCCESS).rjust(32, b"\x00"), octets(SIGNER).rjust(32, b"\x00")), amounts)]
    bloom = 0
    for address, topics, _ in logs:
        for item in [address] + topics:
            digest = codec.keccak256(item)
            for offset in (0, 2, 4):
                bloom |= 1 << (int.from_bytes(digest[offset:offset + 2], "big") & 2047)
    bloom = bloom.to_bytes(256, "big")
    wire = codec.rlp([b"\x01", gas.to_bytes((gas.bit_length() + 7) // 8, "big"), bloom, logs])
    if kind:
        wire = bytes([kind]) + wire
    return {"gas": gas, "price": price, "logs": logs, "bloom": bloom, "wire": wire,
            "sender_after": sender_before - gas * price - 7, "signer_after": signer_before + tip}


def stored(row, header, tx, kind, oracle):
    receipts, proofs = row["receipts"], row["receipt_proofs"]
    check(isinstance(receipts, list) and isinstance(proofs, list) and len(receipts) == len(proofs) == 1, "crossing receipt collection differs")
    wires = [octets(proofs[0]["value"])]
    check(wires == [oracle["wire"]] and codec.ordered_root(wires) == header[5], "crossing receipt wire/root differs")
    h.inclusion(proofs, wires, header[5])
    check(integer(header[10]) == oracle["gas"] and header[6] == oracle["bloom"], "crossing header gas/bloom differs")
    receipt = receipts[0]
    check(quantity(receipt.get("type", "0x0")) == kind and quantity(receipt["status"]) == 1, "crossing receipt status/type differs")
    check(quantity(receipt["gasUsed"]) == quantity(receipt["cumulativeGasUsed"]) == oracle["gas"] and
          quantity(receipt["effectiveGasPrice"]) == oracle["price"], "crossing receipt fees differ")
    check(prior.consensus_logs(receipt["logs"]) == oracle["logs"] and octets(receipt["logsBloom"]) == oracle["bloom"], "crossing receipt logs differ")
    tx_hash = "0x" + codec.keccak256(tx).hex()
    check(receipt["transactionHash"] == tx_hash and receipt["blockHash"] == row["hash"] and
          quantity(receipt["blockNumber"]) == row["number"] and quantity(receipt["transactionIndex"]) == 0, "crossing receipt identity differs")
    for index, log in enumerate(receipt["logs"]):
        check(log["transactionHash"] == tx_hash and log["blockHash"] == row["hash"] and
              quantity(log["blockNumber"]) == row["number"] and quantity(log["transactionIndex"]) == 0 and
              quantity(log["logIndex"]) == index and quantity(log["blockTimestamp"]) == integer(header[11]) and
              log["removed"] is False, "crossing log identity differs")
    prior.account_claims(row, header[3])
    return prior.snapshot(row["state_after"], False)


def records(row, views, head, frontier, error):
    check(row["head_hash"] == head, "crossing actual head differs")
    h.check_records(row["decisions"], row["admission"], views, frontier, observation_offset=1)
    observed = [1] if frontier == 1 else [1, 1, 2]
    keys = {"1": ["bor.run-1.0000000000000000"]} if frontier == 1 else {
        "1": ["bor.run-1.0000000000000000", "bor.run-1.0000000000000001"],
        "2": ["bor.run-1.0000000000000002"]}
    check(row["observed"] == observed and all(type(x) is int for x in row["observed"]) and
          row["keys"] == keys and row["observer_errors"] == [error], "crossing observation frontier differs")


def case(row, expected):
    name, gate, kind, eth_prefix, bor_prefix = expected
    check(row["name"] == name and type(row["type"]) is int and row["type"] == kind and
          type(row["activation_block"]) is int and row["activation_block"] == 2, "crossing case identity differs")
    config = copy.deepcopy(row["config"])
    check(type(config[gate]) is int and config[gate] == 2, "crossing activation schedule differs")
    config[gate] = 8
    h.historical_config(config, eth_prefix, bor_prefix)
    width1, width2 = (16, 16) if kind == 4 else ((15, 16) if kind == 2 else (15, 15))
    genesis, txg, genesis_hash = h.raw_block(row["genesis_raw"], 20 if kind == 4 else 15)
    check(integer(genesis[8]) == 0 and genesis[0] == bytes(32) and not txg, "crossing genesis differs")
    positive, negative = row["crossing"], row["before_activation"]
    check(len(positive["blocks"]) == 2, "crossing needs two successful blocks")
    first, second = positive["blocks"]
    h1, tx1 = h.block(first, width1, 1, genesis_hash)
    h2, tx2 = h.block(second, width2, 2, first["hash"])
    check(len(tx1) == len(tx2) == 1, "crossing transaction count differs")
    h.signed_input(tx1[0], 0, 0)
    h.signed_input(tx2[0], kind, 1)
    check(integer(genesis[9]) == integer(h1[9]) == 10_000_000 and
          integer(h2[9]) == (20_000_000 if kind == 2 else 10_000_000), "crossing London gas-limit transition differs")
    check(integer(h1[11]) == integer(genesis[11]) + 1 and integer(h2[11]) == integer(h1[11]) + 1, "crossing timestamp sequence differs")
    base1 = 937_500_000 if kind == 4 else 0
    base2 = base1 - base1 * (5_000_000 - 21000) // 5_000_000 // 16 if kind == 4 else (10**9 if kind == 2 else 0)
    check((integer(h1[15]) if width1 == 16 else 0) == base1 and
          (integer(h2[15]) if width2 == 16 else 0) == base2, "crossing native base-fee transition differs")
    oracle1 = receipt_oracle(0, base1, 10**19, 0, 10**21)
    oracle2 = receipt_oracle(kind, base2, oracle1["sender_after"], 7, oracle1["signer_after"])
    state1 = stored(first, h1, tx1[0], 0, oracle1)
    state2 = stored(second, h2, tx2[0], kind, oracle2)
    legacy_state(state1, oracle1["sender_after"])
    expected_state = copy.deepcopy(state1)
    expected_state[prior.SENDER].update(nonce=2, balance=oracle2["sender_after"])
    expected_state[prior.SUCCESS]["balance"] = 14
    if kind == 4:
        expected_state[prior.AUTHORITY].update(nonce=1, code=b"\xef\x01\x00" + octets(prior.SUCCESS))
    check(state2 == expected_state and h1[3] != h2[3], "crossing final authenticated state differs")
    candidate = negative["block"]
    hn, txn = h.block(candidate, width1, 1, genesis_hash)
    check(len(txn) == 1 and candidate["hash"] != first["hash"] and hn[4] != h1[4], "crossing negative body is not distinct")
    h.signed_input(txn[0], kind, 0)
    check(all(hn[i] == h1[i] for i in range(width1) if i not in (4, 12)), "crossing negative changed control header commitments")
    expected_error = "could not apply tx 0 [0x" + codec.keccak256(txn[0]).hex() + "]: transaction type not supported"
    check(negative["accepted"] is False and negative["error"] == expected_error and negative["head_before"] == genesis_hash, "crossing native refusal differs")
    check(not any(key in candidate for key in ("receipts", "receipt_proofs", "account_proofs", "state_after")), "crossing refused block has durable state claims")
    records(negative, [], genesis_hash, 1, expected_error)
    records(positive, [first, second], second["hash"], 3, expected_error)


def judge(report):
    try:
        check(report["native_source"] == prior.NATIVE and report["scope"] == "private-ordinary-activation-crossing" and
              report["public_profile_credit"] is False and "error" not in report, "crossing native identity/result differs")
        check(len(report["cases"]) == len(CASES), "crossing case matrix differs")
        for row, expected in zip(report["cases"], CASES):
            case(row, expected)
        return {"result": "PASS", "private_activation_crossings": [1, 2, 4], "reserved127": "NOT_EVALUATED", "public_profile_credit": False}
    except (ValueError, TypeError, KeyError, IndexError, OverflowError, AttributeError) as error:
        return {"result": "FAIL", "error": str(error), "public_profile_credit": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path)
    args = parser.parse_args()
    result = judge(prior.native_json(args.report.read_text()))
    print(json.dumps(result, indent=2))
    raise SystemExit(result["result"] != "PASS")
