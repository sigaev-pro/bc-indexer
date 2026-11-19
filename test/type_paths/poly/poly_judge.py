"""Strict private Polygon proof judge. No public-profile or whole-task verdict."""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re

_spec = importlib.util.spec_from_file_location("poly_proof_codec", Path(__file__).with_name("poly_codec.py"))
codec = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(codec)

NATIVE = "3a018e38467594508ca31ebeaf804837fb92834a"
SENDER = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
AUTHORITY = "0x2b5ad5c4795c026514f8317c7a215e218dccd6cf"
SUCCESS = "0x4000000000000000000000000000000000000001"
REVERT = "0x3000000000000000000000000000000000000002"
TARGET = "0x3000000000000000000000000000000000000003"
RECEIVER = "0x0000000000000000000000000000000000001001"
SYSTEM = "0xfffffffffffffffffffffffffffffffffffffffe"
WATCHED = {SENDER, AUTHORITY, SUCCESS, REVERT, TARGET, RECEIVER}
EVENT_DATA = b"extractor-corpus-state-sync-1"


def check(ok, message):
    if not ok:
        raise ValueError(message)


def octets(value, width=None):
    check(isinstance(value, str), "hex value is not text")
    raw = bytes.fromhex(value.removeprefix("0x"))
    check(width is None or len(raw) == width, "wrong hex width")
    return raw


def integer(raw):
    check(isinstance(raw, bytes) and not raw.startswith(b"\x00"), "noncanonical unsigned integer")
    return int.from_bytes(raw, "big")


def quantity(value):
    if type(value) is int and value >= 0:
        return value
    if isinstance(value, str) and value:
        number = int(value, 16 if value.startswith("0x") else 10)
        if number >= 0:
            return number
    raise ValueError("Expected a nonnegative quantity")


def native_json(value):
    def unique(pairs):
        result = {}
        for key, item in pairs:
            if key in result:
                raise ValueError("Duplicate native record field")
            result[key] = item
        return result
    return json.loads(value, object_pairs_hook=unique)


def uint64(value):
    if type(value) is not int or not 0 <= value < 2**64:
        raise ValueError("Invalid native uint64")
    return value.to_bytes(8, "big")


def digest_bytes(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value):
        raise ValueError("Invalid native digest")
    return bytes.fromhex(value)


def native_text(value):
    data = value.encode("utf-8")
    return uint64(len(data)) + data


def intent_digest(record):
    """Pinned adapters/bor/decision/decision.go (same accepted digest encoding) IntentDigest encoding."""
    data = b"extractor-decision-intent-v1\0"
    data += native_text(record["native_store_id"]) + native_text(record["producer"])
    data += uint64(record["sequence"]) + native_text(record["kind"])
    for head in (record["old_head"], record["new_head"]):
        data += uint64(head["number"]) + digest_bytes(head["hash"])
    data += uint64(len(record["members"]))
    for member in record["members"]:
        data += native_text(member["observation"])
        for field in ("block", "exec_ready_digest", "child_root"):
            data += digest_bytes(member[field])
    data += digest_bytes(record["evidence_digest"]) + bytes([record["events"]])
    return hashlib.sha256(data).hexdigest()


def decided_digest(intent, delivery):
    return hashlib.sha256(b"extractor-decision-decided-v1\0" + digest_bytes(intent) + native_text(delivery)).hexdigest()


def native_block(row):
    outer = codec.rlp_list(octets(row["raw_block"]))
    check(len(outer) == 3, "unexpected Bor block envelope")
    header = codec.header_fields(outer[0][1])
    check(len(header) == 16, "unexpected private Bor header fields")
    check(all(len(header[index]) == 32 for index in (0, 1, 3, 4, 5)), "invalid header hash width")
    number = integer(header[8])
    digest = "0x" + codec.keccak256(outer[0][1]).hex()
    check(type(row["number"]) is int and row["number"] == number and row["hash"] == digest, "reported block identity differs from raw header")
    check(row["root"] == "0x" + header[3].hex() and row["parent"] == "0x" + header[0].hex(), "reported block roots differ")
    check(isinstance(outer[1][0], list) and outer[2][0] == [], "invalid Bor body/uncles")
    txs = [raw if isinstance(value, list) else value for value, raw in outer[1][0]]
    check(codec.ordered_root(txs) == header[4], "complete body transaction root differs")
    proofs = row["transaction_proofs"]
    check(len(proofs) == len(txs), "transaction proof count differs")
    if not txs:
        check(header[4] == codec.EMPTY_ROOT, "nonempty transaction root for empty body")
    for index, (tx, proof) in enumerate(zip(txs, proofs)):
        key = codec.rlp(index.to_bytes((index.bit_length() + 7) // 8, "big"))
        check(type(proof["index"]) is int and proof["index"] == index and octets(proof["key"]) == key, "wrong transaction proof key")
        authenticated = codec.proof_value(header[4], key, [octets(x) for x in proof["nodes"]])
        check(authenticated == tx == octets(proof["value"]), "body transaction not authenticated")
    return header, txs


def check_records(decisions, admission, views, frontier=None):
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
        check(member["observation"] == f"bor.run-1.{sequence:016x}" and member["block"] == row["hash"][2:] and
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


def state_value(account, slot, traced=False):
    key = "0x" + slot.to_bytes(32, "big").hex() if traced else str(slot)
    return int(account["storage"][key].removeprefix("0x"), 16)


def account_claims(row, root):
    proofs = row["account_proofs"]
    after = row["state_after"]
    check(set(proofs) == WATCHED and set(after) == {a[2:] for a in WATCHED}, "missing or extra account proofs")
    for address in WATCHED:
        proof, account = proofs[address], after[address[2:]]
        check(proof["address"].lower() == address, "substituted account proof address")
        leaf = codec.proof_value(root, codec.keccak256(octets(address, 20)), [octets(x) for x in proof["accountProof"]])
        if leaf is None:
            nonce = balance = 0
            storage_root = code_hash = bytes(32)
        else:
            fields = [value for value, _ in codec.rlp_list(leaf)]
            check(len(fields) == 4, "invalid account leaf")
            nonce, balance = integer(fields[0]), integer(fields[1])
            storage_root, code_hash = fields[2:]
            check(len(storage_root) == len(code_hash) == 32, "invalid account commitments")
        check(quantity(proof["nonce"]) == account["nonce"] == nonce and quantity(proof["balance"]) == quantity(account["balance"]) == balance, "account amount/nonce not authenticated")
        check(octets(proof["storageHash"], 32) == storage_root and octets(proof["codeHash"], 32) == code_hash, "account subroot mismatch")
        check((codec.keccak256(octets(account["code"])) if leaf is not None else bytes(32)) == code_hash and
              (leaf is not None or account["code"] == ""), "account code not authenticated")
        slots = proof["storageProof"]
        check(len(slots) == 4 and {quantity(item["key"]) for item in slots} == set(range(4)), "missing or duplicate storage proofs")
        for item in slots:
            slot = quantity(item["key"])
            nodes = [octets(x) for x in item["proof"]]
            if leaf is None:
                check(not nodes, "storage proof for absent account")
                value = None
            else:
                value = codec.proof_value(storage_root, codec.keccak256(slot.to_bytes(32, "big")), nodes)
            expected = 0
            if value is not None:
                decoded, _, end = codec.rlp_item(value)
                check(end == len(value), "storage value has trailing data")
                expected = integer(decoded)
            check(quantity(item["value"]) == expected == state_value(account, slot), "storage value not authenticated")


def consensus_logs(logs):
    return [[octets(log["address"], 20), [octets(topic, 32) for topic in log["topics"]], octets(log["data"])] for log in logs]


def plain_rlp(value):
    return [plain_rlp(child) for child, _ in value] if isinstance(value, list) else value


def receipt_claims(row, header, txs):
    receipts, proofs, traces = row["receipts"], row["receipt_proofs"], row["transactions"]
    check(len(receipts) == len(proofs) == len(traces) == len(txs), "receipt/body/trace count differs")
    if not receipts:
        check(header[5] == codec.EMPTY_ROOT, "nonempty receipt root for empty body")
    cumulative = 0
    receipt_wires = []
    for index, (receipt, proof, trace, tx) in enumerate(zip(receipts, proofs, traces, txs)):
        key = codec.rlp(index.to_bytes((index.bit_length() + 7) // 8, "big"))
        check(type(proof["index"]) is int and proof["index"] == index and octets(proof["key"]) == key, "wrong receipt proof key")
        wire = codec.proof_value(header[5], key, [octets(x) for x in proof["nodes"]])
        receipt_wires.append(wire)
        check(wire is not None and wire == octets(proof["value"]), "receipt bytes not authenticated")
        typ = tx[0] if tx[0] < 128 else 0
        payload = wire if typ == 0 else wire[1:]
        check(typ == 0 or wire[0] == typ, "receipt envelope type differs")
        fields = plain_rlp(codec.rlp_list(payload))
        check(len(fields) == 4 and len(fields[2]) == 256, "invalid receipt payload")
        status, total = integer(fields[0]), integer(fields[1])
        check(status in (0, 1) and total >= cumulative, "invalid receipt status/gas")
        tx_hash = "0x" + codec.keccak256(tx).hex()
        check(quantity(receipt.get("type", "0x0")) == typ and quantity(receipt["status"]) == status and
              quantity(receipt["cumulativeGasUsed"]) == total and quantity(receipt["gasUsed"]) == total - cumulative,
              "receipt status/gas not authenticated")
        check(octets(receipt["logsBloom"], 256) == fields[2] and consensus_logs(receipt["logs"]) == fields[3], "receipt logs not authenticated")
        check(receipt["transactionHash"] == tx_hash and receipt["blockHash"] == row["hash"] and
              quantity(receipt["blockNumber"]) == row["number"] and quantity(receipt["transactionIndex"]) == index,
              "receipt metadata identity differs")
        check(trace["hash"] == tx_hash and trace["type"] == typ and octets(trace["raw"]) == tx, "trace/body identity differs")
        observed = trace["receipt"]
        check(quantity(observed["status"]) == status and quantity(observed["gasUsed"]) == total - cumulative and
              quantity(observed["cumulativeGasUsed"]) == total and consensus_logs(observed["logs"]) == fields[3], "live receipt differs from stored proof")
        cumulative = total
    check(cumulative == integer(header[10]), "receipt gas does not match header")
    check(codec.ordered_root(receipt_wires) == header[5], "complete receipt root differs")


def snapshot(value, traced):
    expected = WATCHED if traced else {a[2:] for a in WATCHED}
    check(set(value) == expected, "incomplete state snapshot")
    return {address: {"balance": quantity(value[address if traced else address[2:]]["balance"]),
                      "nonce": quantity(value[address if traced else address[2:]]["nonce"]),
                      "code": octets(value[address if traced else address[2:]]["code"]),
                      "storage": tuple(state_value(value[address if traced else address[2:]], slot, traced) for slot in range(4))}
            for address in WATCHED}


def ordinary_fields(raw, index):
    kind = raw[0] if raw[0] < 128 else 0
    fields = plain_rlp(codec.rlp_list(raw if kind == 0 else raw[1:]))
    check(kind == (0, 0, 1, 1, 2, 2, 4, 4)[index], "wrong ordinary type order")
    check(len(fields) == {0: 9, 1: 11, 2: 12, 4: 13}[kind], "wrong ordinary field count")
    if kind == 0:
        nonce, price, gas, to, value, data, v, r, s = fields
        check(integer(v) in (30037, 30038), "wrong legacy chain/signature domain")
        check(integer(price) == 5_000_000_000, "wrong legacy fee")
    elif kind == 1:
        chain, nonce, price, gas, to, value, data, access, v, r, s = fields
        check(integer(chain) == 15001 and integer(price) == 5_000_000_000, "wrong access-list chain/fee")
        check(access == [[octets(REVERT), [bytes(32)]]], "wrong ordered access list")
    else:
        chain, nonce, tip, cap, gas, to, value, data, access = fields[:9]
        v, r, s = fields[-3:]
        check(integer(chain) == 15001 and integer(tip) == 2_000_000_000 and integer(cap) == 5_000_000_000,
              "wrong dynamic fee fields")
        check(access == [], "unexpected access list")
        if kind == 4:
            auth = fields[9]
            check(len(auth) == 1 and len(auth[0]) == 6, "wrong authorization count/shape")
            chain_id, delegate, auth_nonce, parity, ar, ass = auth[0]
            check(integer(chain_id) == 15001 and delegate == octets(SUCCESS) and integer(auth_nonce) == index - 6,
                  "wrong authorization domain/target/nonce")
            check(integer(parity) in (0, 1) and 0 < integer(ar) < 2**256 and 0 < integer(ass) < 2**256,
                  "invalid authorization signature")
            codec.fixture_signature(codec.keccak256(b"\x05" + codec.rlp(auth[0][:3])), integer(parity), integer(ar), integer(ass), 2)
    check(kind == 0 or integer(v) in (0, 1), "invalid typed signature parity")
    check(0 < integer(r) < 2**256 and 0 < integer(s) < 2**256, "invalid transaction signature")
    check(integer(nonce) == index and integer(gas) == 150000 and integer(value) == 7 and data == b"" and
          to == octets(SUCCESS if index % 2 == 0 else REVERT), "wrong ordinary transaction input")
    if kind == 0:
        digest = codec.keccak256(codec.rlp(fields[:6] + [(15001).to_bytes(2, "big"), b"", b""]))
        parity = integer(v) - 30037
    else:
        digest = codec.keccak256(bytes([kind]) + codec.rlp(fields[:-3]))
        parity = integer(v)
    codec.fixture_signature(digest, parity, integer(r), integer(s), 1)


def ordinary_state(row, header, txs):
    check(len(txs) == 8 and len(row["calls"]) == 8, "ordinary fixture incomplete")
    previous = None
    for index, (trace, raw, receipt, call) in enumerate(zip(row["transactions"], txs, row["receipts"], row["calls"])):
        ordinary_fields(raw, index)
        status = 1 - index % 2
        target = SUCCESS if status else REVERT
        check(trace["from"].lower() == SENDER and quantity(receipt["status"]) == status, "wrong sender/status")
        before, after = snapshot(trace["before"], True), snapshot(trace["after"], True)
        if previous is not None:
            check(before == previous, "ordinary transaction state discontinuity")
        else:
            check(before[SENDER]["balance"] == 10**19 and before[SENDER]["nonce"] == 0 and
                  before[SUCCESS]["balance"] == 0 and before[AUTHORITY]["nonce"] == 0, "wrong initial fixture state")
        check(after[SENDER]["nonce"] == before[SENDER]["nonce"] + 1 == index + 1, "sender nonce boundary differs")
        gas, price = quantity(receipt["gasUsed"]), quantity(receipt["effectiveGasPrice"])
        expected_price = 5_000_000_000 if index < 4 else min(5_000_000_000, integer(header[15]) + 2_000_000_000)
        check(price == expected_price and gas > 0, "wrong effective fee or gas")
        check(before[SENDER]["balance"] - after[SENDER]["balance"] == gas * price + status * 7, "sender balance effect differs")
        check(after[SUCCESS]["balance"] - before[SUCCESS]["balance"] == status * 7, "successful value transfer differs")
        check(after[REVERT] == before[REVERT] and after[TARGET] == before[TARGET] and after[RECEIVER] == before[RECEIVER], "reverted or unrelated state changed")
        if index >= 6:
            check(after[AUTHORITY]["nonce"] == index - 5 and after[AUTHORITY]["code"] == b"\xef\x01\x00" + octets(SUCCESS), "authorization effects missing")
        else:
            check(after[AUTHORITY] == before[AUTHORITY], "unexpected authorization effects")
        check(call["transaction"] == trace["hash"] and call["depth"] == 0 and call["type"] == 241 and
              call["from"].lower() == SENDER and call["to"].lower() == target and call["input"] == "" and
              call["reverted"] is (not bool(status)), "wrong ordinary native call")
        check(call["output"] == ("" if status else "00" * 31 + "ee") and
              (call.get("error") is None if status else call.get("error") == "execution reverted"), "wrong ordinary call result")
        check(not any(log["address"].lower() == REVERT for log in receipt["logs"]), "reverted log survived")
        previous = after
    check(previous == snapshot(row["state_after"], False), "ordinary end state differs from authenticated state")


def abi_bytes(selector, number, data):
    return selector + number.to_bytes(32, "big") + (64).to_bytes(32, "big") + len(data).to_bytes(32, "big") + data + bytes(-len(data) % 32)


def system_state(row, txs, parent_state):
    check(len(txs) == 1 and txs[0][0] == 127 and len(row["calls"]) == 5, "missing body StateSync/actions")
    expected_items = [[bytes([i]), octets(target), EVENT_DATA, i.to_bytes(32, "big")]
                      for i, target in ((1, TARGET), (2, REVERT))]
    check(plain_rlp(codec.rlp_list(txs[0][1:])) == expected_items, "wrong ordered StateSync body items/L1 hashes")
    tx, receipt = row["transactions"][0], row["receipts"][0]
    check(tx["from"].lower() == SYSTEM, "wrong actual StateSync hook sender")
    check(quantity(receipt["status"]) == 1 and quantity(receipt["gasUsed"]) == 0 and
          quantity(receipt["effectiveGasPrice"]) == 0, "StateSync receipt treated as ordinary gas transaction")
    before, after = snapshot(tx["before"], True), snapshot(tx["after"], True)
    check(before == parent_state and after == snapshot(row["state_after"], False), "system state boundary not authenticated")
    check(before[RECEIVER]["storage"][0] == 0 and after[RECEIVER]["storage"][0] == 2, "wrong receiver progress")
    for address in (SENDER, AUTHORITY, SUCCESS, REVERT):
        check(before[address] == after[address], "system action charged or changed unrelated account")
    check(after[TARGET]["storage"] == (1, int(RECEIVER, 16), len(EVENT_DATA), int.from_bytes(EVENT_DATA.ljust(32, b"\0"), "big")), "successful system target effects differ")
    check(consensus_logs(receipt["logs"]) == [[octets(TARGET), [(94).to_bytes(32, "big")], (1).to_bytes(32, "big")]], "wrong StateSync logs or reverted logs survived")
    root, first, target, second, rejected = row["calls"]
    check([call["depth"] for call in row["calls"]] == [0, 1, 2, 1, 2], "wrong native StateSync call order")
    for call in row["calls"]:
        check(call["transaction"] == tx["hash"] and call["type"] == 241, "uncorrelated system call")
    check(root["from"].lower() == SYSTEM and root["to"].lower() == RECEIVER and root["input"] == "" and
          root["reverted"] is False and root.get("error") is None, "invalid synthetic StateSync root")
    check(snapshot(root["before"], True) == before and snapshot(root["after"], True) == after, "synthetic root state differs")
    previous = before
    for index, outer, inner, target_address in ((1, first, target, TARGET), (2, second, rejected, REVERT)):
        outer_before, outer_after = snapshot(outer["before"], True), snapshot(outer["after"], True)
        inner_before, inner_after = snapshot(inner["before"], True), snapshot(inner["after"], True)
        # Native contract/client.go encodes clerk.EventRecord (six fields),
        # whereas the body StateSyncData encodes only its first four fields.
        event = codec.rlp([bytes([index]), octets(target_address), EVENT_DATA,
                           index.to_bytes(32, "big"), bytes([index]), b"15001"])
        check(outer["from"].lower() == SYSTEM and outer["to"].lower() == RECEIVER and
              octets(outer["input"]) == abi_bytes(bytes.fromhex("19494a17"), 1558348306, event), "wrong receiver system-call identity/data")
        check(outer_before == previous and outer_before[RECEIVER]["storage"][0] == index - 1 and
              outer_after[RECEIVER]["storage"][0] == index, "receiver action state discontinuity")
        check(inner["from"].lower() == RECEIVER and inner["to"].lower() == target_address and
              octets(inner["input"]) == abi_bytes(bytes.fromhex("26c53bea"), index, EVENT_DATA), "wrong target system-call identity/data")
        check(inner_before[RECEIVER]["storage"][0] == index and inner_after[RECEIVER]["storage"][0] == index,
              "receiver progress not visible during action")
        check(outer_after == inner_after and outer["reverted"] is False and outer.get("error") is None and
              outer["output"] == (1 if index == 1 else 0).to_bytes(32, "big").hex(), "receiver action outcome differs")
        check(inner["reverted"] is (index == 2) and inner["output"] == ("" if index == 1 else "00" * 31 + "ee") and
              (inner.get("error") is None if index == 1 else inner.get("error") == "execution reverted"), "nested action outcome differs")
        if index == 2:
            check(inner_before == inner_after, "failed nested action retained state")
        else:
            check(inner_before[TARGET]["storage"] == (0, 0, 0, 0) and inner_after[TARGET] == after[TARGET], "successful action before/after state differs")
        previous = outer_after
    check(previous == after, "last action state differs from authenticated end state")


def component_claims(components, head):
    check(components["head_before"] == components["head_after"] == head, "component control moved head")
    raw = octets(components["blob_raw"])
    check(raw[0] == 3 and components["blob_hash"] == "0x" + codec.keccak256(raw).hex(), "blob decoder identity differs")
    fields = plain_rlp(codec.rlp_list(raw[1:]))
    check(len(fields) == 14 and integer(fields[0]) == 15001 and integer(fields[1]) == 8 and
          integer(fields[2]) == 2_000_000_000 and integer(fields[3]) == 5_000_000_000 and
          integer(fields[4]) == 21000 and fields[5] == octets(SUCCESS) and integer(fields[6]) == 0 and
          fields[7] == b"" and fields[8] == [] and integer(fields[9]) == 1 and
          fields[10] == [b"\x01" + bytes(31)], "wrong blob component input")
    codec.fixture_signature(codec.keccak256(b"\x03" + codec.rlp(fields[:-3])), integer(fields[-3]), integer(fields[-2]), integer(fields[-1]), 1)
    check(components["blob_pool_error"] == "transaction type not supported: received type 3", "wrong native blob pool refusal")
    check(components["blob_body_import"] == "NOT_RUN", "pool refusal cannot grant blob body-import credit")
    rows = components["unknown_block_decode"]
    check([row["type"] for row in rows] == list(range(5, 127)), "unknown block-decoder coverage incomplete")
    check(all(isinstance(row["error"], str) and "transaction type not supported" in row["error"] for row in rows), "wrong unknown block-decoder refusal")


def refusal_claims(rows, blocks, blob_raw):
    names = ["missing", "non-final", "mismatched-l1-hash", "reordered-items", "duplicate-body", "blob-native-header"]
    check([row["case"] for row in rows] == names, "native import refusal cases incomplete")
    expected_items = [[bytes([index]), octets(address), EVENT_DATA, index.to_bytes(32, "big")]
                      for index, address in ((1, TARGET), (2, REVERT))]
    original = b"\x7f" + codec.rlp(expected_items)
    for row in rows:
        case = row["case"]
        expected_error = "data blobs present in block body" if case == "blob-native-header" else "state-sync mismatch"
        check("control_error" not in row and isinstance(row.get("error"), str) and expected_error in row["error"].lower(), "missing expected native import refusal")
        check(row["head_before"] == row["head_after"] == blocks[2]["hash"], "refused import changed canonical head")
        header, txs = native_block(row)
        check(integer(header[8]) == 4 and header[0] == octets(blocks[2]["hash"]), "refusal does not target expected parent")
        if case == "missing":
            check(txs == [], "missing-StateSync control has transactions")
        elif case == "blob-native-header":
            check(row["error"] == expected_error and txs == [octets(blob_raw), original] and len(header) == 16,
                  "blob refusal does not bind native header profile and otherwise valid fixture input")
        elif case == "non-final":
            check(len(txs) == 2 and txs[0] == original and txs[1][0] >= 192, "StateSync is not followed by ordinary tx")
            normal = plain_rlp(codec.rlp_list(txs[1]))
            check(len(normal) == 9 and [integer(x) for x in normal[:3]] == [8, 5_000_000_000, 21000] and
                  normal[3] == octets(SUCCESS) and normal[4:6] == [b"\x01", b""] and
                  integer(normal[6]) in (30037, 30038), "non-final control ordinary input differs")
            digest = codec.keccak256(codec.rlp(normal[:6] + [(15001).to_bytes(2, "big"), b"", b""]))
            codec.fixture_signature(digest, integer(normal[6]) - 30037, integer(normal[7]), integer(normal[8]), 1)
        elif case == "duplicate-body":
            check(txs == [original, original], "StateSync duplicate control differs")
        else:
            check(len(txs) == 1 and txs[0][0] == 127, "wrong mismatched StateSync envelope")
            items = plain_rlp(codec.rlp_list(txs[0][1:]))
            if case == "mismatched-l1-hash":
                changed = [list(item) for item in expected_items]
                changed[0][3] = (153).to_bytes(32, "big")
                check(items == changed, "L1 hash mismatch control absent")
            else:
                check(items == list(reversed(expected_items)), "item-order mismatch control absent")
        # ValidateBody precedes admission; StateSync mismatch is discovered
        # during execution after admission. Native records must reflect this.
        check_records(row["decisions"], row["admission"], blocks[:3], frontier=3 if case == "blob-native-header" else 4)


def judge(report):
    try:
        check(report.get("native_source") == NATIVE and report.get("public_profile_credit") is False and "error" not in report,
              "wrong native source/scope or failed execution")
        blocks = report["blocks"]
        check([block["number"] for block in blocks] == [1, 2, 3, 4], "incomplete private chain")
        previous_hash, previous_state = None, None
        for row in blocks:
            header, txs = native_block(row)
            check(integer(header[11]) == 1558348305 + row["number"], "wrong private block time")
            if previous_hash is not None:
                check(row["parent"] == previous_hash, "canonical chain discontinuity")
            account_claims(row, header[3])
            receipt_claims(row, header, txs)
            if row["number"] == 1:
                ordinary_state(row, header, txs)
            elif row["number"] == 4:
                system_state(row, txs, previous_state)
            else:
                check(not txs and not row["calls"] and snapshot(row["state_after"], False) == previous_state, "empty block changed watched state")
            previous_hash, previous_state = row["hash"], snapshot(row["state_after"], False)
        check(report["head_hash"] == previous_hash and report["observed"] == [1, 2, 3, 4] and report["observer_errors"] == [],
              "native final head/adapter observations differ")
        check(report["keys"] == {str(i + 1): [f"bor.run-1.{i:016x}"] for i in range(4)}, "adapter observation keys differ")
        check_records(report["decisions"], report["admission"], blocks)
        component_claims(report["components"], previous_hash)
        refusal_claims(report["import_refusals"], blocks, report["components"]["blob_raw"])
    except (KeyError, TypeError, ValueError, IndexError, OverflowError) as error:
        return {"result": "FAIL", "error": f"{type(error).__name__}: {error}", "public_profile_credit": False}
    return {"result": "PASS", "scope": "private-current-fork-import-and-system-state",
            "ordinary_types": [0, 1, 2, 4], "body_state_sync": True,
            "blob_body_import": "native-header-body-refusal-only", "activation_boundaries": "NOT_RUN", "public_profile_credit": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    args = parser.parse_args()
    result = judge(native_json(args.report.read_text()))
    print(json.dumps(result))
    return int(result["result"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
