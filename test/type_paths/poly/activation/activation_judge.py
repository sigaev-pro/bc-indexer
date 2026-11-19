"""Private historical refusals; reserved127 observation earns no gate credit."""

import argparse
import importlib.util
import json
from pathlib import Path

_spec = importlib.util.spec_from_file_location("activation_judge_support", Path(__file__).with_name("activation_support.py"))
support = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(support)
prior = support.module("poly_judge")
codec = support.module("poly_codec")
profile = support.module("poly_profile")
check, octets, integer, quantity = prior.check, prior.octets, prior.integer, prior.quantity
CASES = (("preBerlin", 1, 0, 0), ("preLondon", 2, 1, 0), ("prePrague", 4, 4, 4), ("preMadhugiri", 127, 5, 6))


def native_numbers(value, events=False):
    """Reject JSON numeric aliases before exact native schema/value checks.

    Only the native intent's events member is deliberately boolean; the
    accepted schema checker independently requires that member to be true.
    Bounds and exact values remain the responsibility of each native schema.
    """
    if isinstance(value, dict):
        for key, child in value.items():
            if events and key == "events":
                continue
            native_numbers(child, events)
    elif isinstance(value, list):
        for child in value:
            native_numbers(child, events)
    elif isinstance(value, (bool, float)):
        raise ValueError("native numeric metadata requires JSON integers")


def historical_config(config, eth_prefix, bor_prefix):
    native_numbers(config)
    eth = {name: 0 if i < eth_prefix else 8 for i, name in enumerate(profile.ETHEREUM_SCHEDULE)}
    bor = {name: 0 if i < bor_prefix else 8 for i, name in enumerate(profile.BOR_SCHEDULE)}
    bor.update({"hampiBlock": None, "sprint": {"0": 4}, "period": {"0": 1},
                "producerDelay": {"0": 6}, "backupMultiplier": {"0": 1},
                "stateSyncConfirmationDelay": {"0": 0},
                "coinbase": {"0": "0x" + "00" * 20}, "burntContract": {"0": "0x" + "00" * 20},
                "stateReceiverContract": prior.RECEIVER,
                "validatorContract": "0x0000000000000000000000000000000000001000"})
    eth.update({name: 0 for name in ("homesteadBlock", "eip150Block", "eip155Block", "eip158Block",
                                   "byzantiumBlock", "constantinopleBlock", "petersburgBlock", "istanbulBlock", "muirGlacierBlock")})
    eth.update({"eip150Hash": "0x" + "00" * 32, "chainId": 15001, "bor": bor})
    check(config == eth, "historical native configuration differs")


def raw_block(raw, width):
    outer = codec.rlp_list(octets(raw))
    check(len(outer) == (4 if width == 20 else 3) and isinstance(outer[1][0], list) and outer[2][0] == [], "invalid historical block envelope")
    header = codec.header_fields(outer[0][1])
    check(len(header) == width, "historical header fork shape differs")
    check(all(len(header[i]) == 32 for i in (0, 1, 3, 4, 5)), "invalid historical header hashes")
    if width == 20:
        # Pinned core/genesis.go: Shanghai/Cancun genesis has empty withdrawals
        # and zero blob/beacon fields; ordinary Bor headers still have16fields.
        check(outer[3][0] == [] and header[15:] == [(10**9).to_bytes(4, "big"), codec.EMPTY_ROOT, b"", b"", bytes(32)], "wrong native genesis fork tail")
    txs = [encoded if isinstance(value, list) else value for value, encoded in outer[1][0]]
    check(codec.ordered_root(txs) == header[4], "complete historical body root differs")
    return header, txs, "0x" + codec.keccak256(outer[0][1]).hex()


def inclusion(proofs, wires, root):
    check(len(proofs) == len(wires), "historical proof count differs")
    for index, (proof, wire) in enumerate(zip(proofs, wires)):
        key = codec.rlp(index.to_bytes((index.bit_length() + 7) // 8, "big"))
        check(type(proof["index"]) is int and proof["index"] == index and octets(proof["key"]) == key, "historical proof key differs")
        check(octets(proof["value"]) == wire == codec.proof_value(root, key, [octets(x) for x in proof["nodes"]]), "historical proof bytes differ")


def block(row, width, number, parent):
    header, txs, digest = raw_block(row["raw_block"], width)
    check(type(row["number"]) is int and row["number"] == integer(header[8]) == number, "historical block number differs")
    check(row["hash"] == digest and row["parent"] == "0x" + header[0].hex() == parent, "historical block chain differs")
    check(row["root"] == "0x" + header[3].hex(), "historical state root differs")
    inclusion(row["transaction_proofs"], txs, header[4])
    return header, txs


def signed_input(raw, kind, nonce):
    check((raw[0] if raw[0] < 128 else 0) == kind, "wrong activation transaction type")
    fields = prior.plain_rlp(codec.rlp_list(raw if kind == 0 else raw[1:]))
    check(len(fields) == {0: 9, 1: 11, 2: 12, 4: 13}[kind], "wrong activation transaction fields")
    v, r, s = map(integer, fields[-3:])
    if kind == 0:
        n, price, gas, to, value, data = fields[:6]
        check(integer(price) == 5_000_000_000, "wrong legacy fixture price")
        digest = codec.keccak256(codec.rlp(fields[:6] + [(15001).to_bytes(2, "big"), b"", b""]))
        parity = v - 30037
    else:
        check(integer(fields[0]) == 15001, "wrong typed fixture chain")
        if kind == 1:
            _, n, price, gas, to, value, data, access = fields[:8]
            check(integer(price) == 5_000_000_000, "wrong access-list fixture price")
        else:
            _, n, tip, cap, gas, to, value, data, access = fields[:9]
            check(integer(tip) == 2_000_000_000 and integer(cap) == 5_000_000_000, "wrong typed fixture fee caps")
        check(access == [], "wrong fixture access list")
        if kind == 4:
            auth = fields[9]
            check(len(auth) == 1 and len(auth[0]) == 6, "wrong fixture authorization count")
            chain, target, auth_nonce, av, ar, ass = auth[0]
            check(integer(chain) == 15001 and target == octets(prior.SUCCESS) and integer(auth_nonce) == 0, "wrong fixture authorization domain")
            codec.fixture_signature(codec.keccak256(b"\x05" + codec.rlp(auth[0][:3])), integer(av), integer(ar), integer(ass), 2)
        digest, parity = codec.keccak256(bytes([kind]) + codec.rlp(fields[:-3])), v
    check(integer(n) == nonce and integer(gas) == (100000 if kind == 4 else 21000) and
          to == octets(prior.SUCCESS) and integer(value) == 7 and data == b"", "wrong activation transaction inputs")
    codec.fixture_signature(digest, parity, r, s, 1)


def transfer_receipt(base_fee):
    """Bor core/bor_fee_log.go + state_transition.go; independent fixture amounts.

    Sender starts at10 ETH, span signer at1000 ETH, recipient at zero. Gas
    purchase precedes the value-transfer log. Fee-log balances deliberately
    describe tip movement (the native deprecated event), not final balances.
    """
    address = octets("0000000000000000000000000000000000001010")
    sender = octets(prior.SENDER).rjust(32, b"\x00")
    signer = octets("71562b71999873db5b286df957af199ec94617f7").rjust(32, b"\x00")
    bought = 21000 * 5_000_000_000
    tip = 21000 * (5_000_000_000 - base_fee)
    signatures = ("e6497e3ee548a3372136af2fcb0696db31fc6cf20260707645068bd3fe97f3c4", "4dfe1bbbcf077ddc3e01291eea2d5c70c2b422b415d95645b9adcfd678cb1d63")
    amounts = ((7, 10**19 - bought, 0, 10**19 - bought - 7, 7),
               (tip, 10**19, 10**21, 10**19 - tip, 10**21 + tip))
    logs = [[address, [octets(signature), address.rjust(32, b"\x00"), sender, target],
             b"".join(value.to_bytes(32, "big") for value in values)]
            for signature, target, values in zip(signatures, (octets(prior.SUCCESS).rjust(32, b"\x00"), signer), amounts)]
    # core/types/bloom9.go: three 11-bit positions from Keccak for address/topics.
    bloom = 0
    for address, topics, _ in logs:
        for item in [address] + topics:
            digest = codec.keccak256(item)
            for offset in (0, 2, 4):
                bloom |= 1 << (int.from_bytes(digest[offset:offset + 2], "big") & 2047)
    return codec.rlp([b"\x01", (21000).to_bytes(2, "big"), bloom.to_bytes(256, "big"), logs]), logs, bloom.to_bytes(256, "big")


def stored(row, header, txs, legacy_receipt):
    check(row["receipts"] is None or isinstance(row["receipts"], list), "malformed native receipt collection")
    receipts = row["receipts"] or []
    proofs = row["receipt_proofs"]
    wires = [octets(proof["value"]) for proof in proofs]
    check(len(receipts) == len(proofs) == (1 if legacy_receipt else 0), "historical receipt count differs")
    check(codec.ordered_root(wires) == header[5], "complete historical receipt root differs")
    inclusion(proofs, wires, header[5])
    expected_wire, expected_logs, bloom = transfer_receipt(integer(header[15]) if len(header) == 16 else 0) if legacy_receipt else (None, [], bytes(256))
    check(integer(header[10]) == (21000 if legacy_receipt else 0) and header[6] == bloom, "historical header gas/bloom differs")
    if legacy_receipt:
        check(wires == [expected_wire], "legacy control receipt differs from native transfer oracle")
        receipt = receipts[0]
        check(quantity(receipt.get("type", "0x0")) == 0 and quantity(receipt["status"]) == 1 and
              quantity(receipt["gasUsed"]) == quantity(receipt["cumulativeGasUsed"]) == 21000, "legacy receipt claim differs")
        check(prior.consensus_logs(receipt["logs"]) == expected_logs and octets(receipt["logsBloom"]) == bloom, "legacy receipt logs differ")
        check(receipt["transactionHash"] == "0x" + codec.keccak256(txs[0]).hex() and receipt["blockHash"] == row["hash"] and
              quantity(receipt["blockNumber"]) == 1 and quantity(receipt["transactionIndex"]) == 0, "legacy receipt identity differs")
    prior.account_claims(row, header[3])
    return prior.snapshot(row["state_after"], False)


def records(row, views, head, frontier, rejection=None):
    check(row["head_hash"] == head, "actual native head differs")
    native_numbers({key: prior.native_json(value) for key, value in row["decisions"].items()}, events=True)
    native_numbers({key: prior.native_json(value) for key, value in row["admission"].items()})
    prior.check_records(row["decisions"], row["admission"], views, frontier)
    native_numbers(row["observed"])
    check(row["observer_errors"] == ([rejection] if rejection else []) and row["observed"] == [1, 2] and
          row["keys"] == {"1": ["bor.run-1.0000000000000000"], "2": ["bor.run-1.0000000000000001"]}, "actual capture observation differs")


def case(row, expected):
    name, kind, eth_prefix, bor_prefix = expected
    check(row["name"] == name and type(row["type"]) is int and row["type"] == kind, "activation case identity differs")
    historical_config(row["config"], eth_prefix, bor_prefix)
    width = 15 if eth_prefix < 2 else 16
    genesis, genesis_txs, genesis_hash = raw_block(row["genesis_raw"], 20 if eth_prefix >= 4 else width)
    check(integer(genesis[8]) == 0 and genesis[0] == bytes(32) and not genesis_txs, "invalid historical genesis")
    control, attempt = row["control"], row["attempt"]
    check(len(control["blocks"]) == 2, "missing valid historical controls")
    first, empty = control["blocks"]
    h1, tx1 = block(first, width, 1, genesis_hash)
    h2, tx2 = block(empty, width, 2, first["hash"])
    check(all(integer(header[9]) == 10_000_000 for header in (genesis, h1, h2)), "private genesis/control gas limit differs")
    if width == 16:
        # params.BaseFeeChangeDenominator: Delhi16, Bhilai64; Dandeli's
        # percentage target is still future in both selected profiles.
        denominator = 64 if bor_prefix >= 5 else 16
        base1 = 10**9 - 10**9 // denominator
        base2 = base1 - base1 * (5_000_000 - 21000) // 5_000_000 // denominator
        check(integer(h1[15]) == base1 and integer(h2[15]) == base2, "historical native base fee transition differs")
    check(len(tx1) == 1 and not tx2, "legacy/empty control body differs")
    signed_input(tx1[0], 0, 0)
    state1, state2 = stored(first, h1, tx1, True), stored(empty, h2, tx2, False)
    check(state1[prior.SENDER]["nonce"] == 1 and state1[prior.SENDER]["balance"] == 10**19 - 21000 * 5_000_000_000 - 7 and
          state1[prior.SUCCESS]["balance"] == 7, "legacy control state/funding differs")
    check(h1[3] == h2[3] and state1 == state2, "empty control changed authenticated state")
    records(control, [first, empty], empty["hash"], 2)
    candidate = attempt["block"]
    hc, txc = block(candidate, width, 2, first["hash"])
    check(len(txc) == 1 and candidate["hash"] != empty["hash"] and hc[4] != h2[4], "candidate body is not distinct")
    check(all(hc[i] == h2[i] for i in range(width) if i not in (4, 12)), "candidate changed empty-control execution commitments")
    check(attempt["head_before"] == first["hash"], "wrong seeded native head")
    if kind != 127:
        signed_input(txc[0], kind, 1)
        expected_error = "could not apply tx 0 [0x" + codec.keccak256(txc[0]).hex() + "]: transaction type not supported"
        check(attempt["accepted"] is False and attempt["error"] == expected_error, "missing native preactivation refusal")
        check(not any(key in candidate for key in ("receipts", "receipt_proofs", "account_proofs", "state_after")), "rejected candidate has durable state claims")
        records(attempt, [first], first["hash"], 2, expected_error)
    else:
        check(txc[0][0] == 127, "wrong reserved envelope")
        fields = prior.plain_rlp(codec.rlp_list(txc[0][1:]))
        check(fields == [[b"\x01", octets(prior.TARGET), b"%", bytes(31) + b"\x01"]], "wrong reserved body fixture")
        check(attempt["classification"] == "native-preMadhugiri-observation-no-refusal-credit" and attempt["accepted"] is True and "error" not in attempt, "reserved behavior differs from scoped native observation")
        candidate_state = stored(candidate, hc, txc, False)
        check(candidate_state == state2 and hc[3] == h2[3], "reserved preactivation changed authenticated state")
        records(attempt, [first, candidate], candidate["hash"], 2)


def judge(report):
    try:
        check(report["native_source"] == prior.NATIVE and report["scope"] == "private-preactivation" and
              report["public_profile_credit"] is False and "error" not in report, "wrong native activation identity/result")
        check(len(report["cases"]) == len(CASES), "incomplete activation cases")
        for row, expected in zip(report["cases"], CASES):
            case(row, expected)
        return {"result": "PASS", "ordinary_preactivation_refusals": [1, 2, 4],
                "reserved127": "accepted_without_execution_no_activation_refusal_credit",
                "paired_boundary_crossing": "NOT_RUN", "public_profile_credit": False}
    except (ValueError, TypeError, KeyError, IndexError, OverflowError, AttributeError) as error:
        return {"result": "FAIL", "error": str(error), "public_profile_credit": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path)
    args = parser.parse_args()
    result = judge(prior.native_json(args.report.read_text()))
    print(json.dumps(result, indent=2))
    raise SystemExit(result["result"] != "PASS")
