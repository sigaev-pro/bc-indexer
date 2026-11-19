"""Narrow source-derived helpers; see crossing_helpers.origin.json."""

import importlib.util
from pathlib import Path

_spec = importlib.util.spec_from_file_location("crossing_helper_support", Path(__file__).with_name("crossing_support.py"))
support = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(support)
prior = support.module("poly_judge")
codec = support.module("poly_codec")
profile = support.module("poly_profile")
check, octets, integer, quantity = prior.check, prior.octets, prior.integer, prior.quantity
native_json, intent_digest, decided_digest = prior.native_json, prior.intent_digest, prior.decided_digest


def historical_config(config, eth_prefix, bor_prefix):
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


def check_records(decisions, admission, views, frontier=None, observation_offset=0):
    records = {key: native_json(value) for key, value in decisions.items()}
    prefix = "extractor-decision-v1/bor.run-1/"
    expected = {prefix + "counter"}
    for sequence, row in enumerate(views):
        key = prefix + f"{sequence:016x}"
        expected.update((key + "/intent", key + "/decided"))
        intent, decided = records[key + "/intent"], records[key + "/decided"]
        check(set(intent) == {"native_store_id", "producer", "sequence", "kind", "old_head", "new_head", "members", "evidence_digest", "events", "digest"}, "invalid intent schema")
        check(intent["native_store_id"] == "0123456789abcdef0123456789abcdef" and intent["producer"] == "bor.run-1", "wrong native identity")
        check(type(intent["sequence"]) is int and intent["sequence"] == sequence and intent["kind"] == "IMPORT" and intent["events"] is True, "wrong decision kind/sequence")
        check(intent["old_head"] == {"number": row["number"] - 1, "hash": row["parent"][2:]} and
              intent["new_head"] == {"number": row["number"], "hash": row["hash"][2:]}, "decision/native header mismatch")
        check(len(intent["members"]) == 1, "wrong decision member count")
        member = intent["members"][0]
        check(set(member) == {"observation", "block", "exec_ready_digest", "child_root"}, "invalid decision member schema")
        check(member["observation"] == f"bor.run-1.{sequence + observation_offset:016x}" and member["block"] == row["hash"][2:] and
              member["child_root"] == row["root"][2:], "decision member/root mismatch")
        check(intent["digest"] == intent_digest(intent), "invalid intent digest")
        check(set(decided) == {"sequence", "intent_digest", "delivery", "digest"}, "invalid decided schema")
        check(type(decided["sequence"]) is int and decided["sequence"] == sequence and
              decided["intent_digest"] == intent["digest"] and decided["delivery"] == "BATCH" and
              decided["digest"] == decided_digest(intent["digest"], "BATCH"), "invalid decided binding")
    check(set(records) == expected and records[prefix + "counter"] == {"next": len(views)}, "wrong durable decision frontier")
    frontier = len(views) if frontier is None else frontier
    actual = {key: native_json(value) for key, value in admission.items()}
    prefix = "extractor-admission-v1/bor.run-1/"
    check(actual == {prefix + "counter": {"next": frontier}, prefix + "lowwater": {"sequence": frontier},
                     prefix + "purged": {"sequence": frontier}}, "wrong admission frontier")
