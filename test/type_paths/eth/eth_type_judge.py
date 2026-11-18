#!/usr/bin/env python3
"""Judge private native ETH type-path facts against the independent oracle."""

import argparse
import functools
from collections import Counter
import json
import hashlib
import re
from pathlib import Path

NATIVE = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
SENDER = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
AUTHORITY = "0x2b5ad5c4795c026514f8317c7a215e218dccd6cf"
SUCCESS = "0x4000000000000000000000000000000000000001"
REVERT = "0x3000000000000000000000000000000000000002"
DELEGATE = "0x3000000000000000000000000000000000000004"
SYSTEM = "0xfffffffffffffffffffffffffffffffffffffffe"
ZERO = "0x" + "00" * 32
SYSTEM_TARGETS = {
    "0x000f3df6d732807ef1319fb7b8bb8522d0beac02", "0x0000f90827f1c53a10cb7a02335b175320002935",
    "0x00000961ef480eb55e80d19ad83579a64c007002", "0x0000bbddc7ce488642fb579f8b00f3a590007251",
}


# Exact accepted independent codec from test/harness/source_inputs_geth.py.
# Portable source identity and behavioral vectors guard this retained copy.
KECCAK_ROUNDS = (
    0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000, 0x000000000000808B,
    0x0000000080000001, 0x8000000080008081, 0x8000000000008009, 0x000000000000008A, 0x0000000000000088,
    0x0000000080008009, 0x000000008000000A, 0x000000008000808B, 0x800000000000008B, 0x8000000000008089,
    0x8000000000008003, 0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
    0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008)
KECCAK_ROTATIONS = ((0, 36, 3, 41, 18), (1, 44, 10, 45, 2), (62, 6, 43, 15, 61), (28, 55, 25, 21, 56),
                    (27, 20, 39, 8, 14))
MASK = (1 << 64) - 1


@functools.lru_cache(maxsize=4096)
def keccak256(data):
    """Keccak-256 as Ethereum uses it (original padding, not SHA3-256), written from the specification.
    It is slow in pure Python, so results are cached; data must be bytes."""
    rate = 136
    padded = bytearray(data) + b"\x01" + bytes(-(len(data) + 1) % rate)
    padded[-1] |= 0x80
    lanes = [[0] * 5 for _ in range(5)]
    for offset in range(0, len(padded), rate):
        for index in range(rate // 8):
            lanes[index % 5][index // 5] ^= int.from_bytes(padded[offset + 8 * index:offset + 8 * index + 8], "little")
        for constant in KECCAK_ROUNDS:
            parity = [lanes[x][0] ^ lanes[x][1] ^ lanes[x][2] ^ lanes[x][3] ^ lanes[x][4] for x in range(5)]
            for x in range(5):
                mix = parity[(x - 1) % 5] ^ (((parity[(x + 1) % 5] << 1) | (parity[(x + 1) % 5] >> 63)) & MASK)
                for y in range(5):
                    lanes[x][y] ^= mix
            moved = [[0] * 5 for _ in range(5)]
            for x in range(5):
                for y in range(5):
                    shift = KECCAK_ROTATIONS[x][y]
                    moved[y][(2 * x + 3 * y) % 5] = ((lanes[x][y] << shift) | (lanes[x][y] >> (64 - shift))) & MASK
            for x in range(5):
                for y in range(5):
                    lanes[x][y] = moved[x][y] ^ (~moved[(x + 1) % 5][y] & moved[(x + 2) % 5][y])
            lanes[0][0] ^= constant
    return b"".join(lanes[index % 5][index // 5].to_bytes(8, "little") for index in range(4))


def rlp_item(data, offset=0):
    """Decode one RLP item at offset. Return (value, raw encoding, next offset); a list value is a list of
    (value, raw) pairs. Noncanonical lengths are refused."""
    first = data[offset]
    if first < 0x80:
        return data[offset:offset + 1], data[offset:offset + 1], offset + 1
    short, base = (first < 0xB8, 0x80) if first < 0xC0 else (first < 0xF8, 0xC0)
    if short:
        start, length = offset + 1, first - base
    else:
        size = first - base - 55
        length = int.from_bytes(data[offset + 1:offset + 1 + size], "big")
        if data[offset + 1] == 0 or length < 56:
            raise ValueError("noncanonical RLP length")
        start = offset + 1 + size
    end = start + length
    if end > len(data):
        raise ValueError("truncated RLP item")
    if base == 0x80:
        if length == 1 and data[start] < 0x80:
            raise ValueError("noncanonical RLP byte")
        return data[start:end], data[offset:end], end
    values, cursor = [], start
    while cursor < end:
        value, raw, cursor = rlp_item(data, cursor)
        values.append((value, raw))
    if cursor != end:
        raise ValueError("RLP list overruns its length")
    return values, data[offset:end], end


def rlp_list(data):
    """Decode bytes that must hold exactly one RLP list."""
    value, _, end = rlp_item(data)
    if end != len(data) or not isinstance(value, list):
        raise ValueError("not exactly one RLP list")
    return value


# Positions in the RLP list of a Geth header.
PARENT_HASH, STATE_ROOT, NUMBER, EXTRA = 0, 3, 8, 12


def header_fields(data):
    fields = [value for value, _ in rlp_list(data)]
    if len(fields) < 15 or any(isinstance(field, list) for field in fields[:15]):
        raise ValueError("not a header")
    return fields


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
    """Pinned adapters/geth/decision/decision.go IntentDigest encoding."""
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


def parent_hash(raw):
    return native_header(raw)["parent"]


def native_header(raw):
    """Bind reported identities to the exact encoded native header."""
    block = rlp_list(bytes.fromhex(raw))
    if len(block) not in (3, 4):
        raise ValueError("Unexpected native block field count")
    encoded = block[0][1]
    fields = header_fields(encoded)
    if len(fields) not in (15, 16, 17, 20, 21):
        raise ValueError("Unexpected fixture header field count")
    if len(fields[PARENT_HASH]) != 32 or len(fields[STATE_ROOT]) != 32:
        raise ValueError("Invalid native hash/root width")
    for position in (NUMBER, 11):
        if len(fields[position]) > 8 or fields[position].startswith(b"\0"):
            raise ValueError("Noncanonical native header integer")
    return {"hash": "0x" + keccak256(encoded).hex(), "parent": fields[PARENT_HASH].hex(),
            "root": "0x" + fields[STATE_ROOT].hex(), "number": int.from_bytes(fields[NUMBER], "big"),
            "time": int.from_bytes(fields[11], "big")}


def check_records(case, views, check):
    """Check native durable records after admission's terminal purge."""
    records = {k: native_json(v) for k, v in case["decision_records"].items()}
    intents = sorted(k for k in records if k.endswith("/intent"))
    check(len(intents) == len(views), "missing or duplicate capture decisions")
    if not intents:
        return
    producer = records[intents[0]]["producer"]
    check(re.fullmatch(r"type-paths-eth\.r[0-9a-f]{16}", producer) is not None, "wrong capture producer")
    prefix = "extractor-decision-v1/" + producer + "/"
    expected = {prefix + "counter"}
    for sequence, block in enumerate(views):
        key = prefix + f"{sequence:016x}"
        expected.update((key + "/intent", key + "/decided"))
        check(key + "/decided" in records, "unresolved capture intent")
        intent, decided = records[key + "/intent"], records[key + "/decided"]
        check(set(intent) == {"native_store_id", "producer", "sequence", "kind", "old_head", "new_head", "members", "evidence_digest", "events", "digest"}, "wrong intent schema")
        check(intent["native_store_id"] == "e0105e71000000000000000000000001", "wrong native store")
        check(intent["producer"] == producer and type(intent["sequence"]) is int and intent["sequence"] == sequence, "wrong capture identity")
        check(intent["kind"] == "IMPORT" and intent["events"] is True, "wrong capture kind/events")
        check(intent["old_head"] == {"number": block["number"] - 1, "hash": parent_hash(block["raw_block"])}, "wrong previous native head")
        check(intent["new_head"] == {"number": block["number"], "hash": block["hash"][2:]}, "wrong imported native head")
        members = intent["members"]
        check(len(members) == 1, "wrong capture members")
        member = members[0]
        check(set(member) == {"observation", "block", "exec_ready_digest", "child_root"}, "wrong member schema")
        check(member["observation"] == producer + f".{sequence:016x}", "wrong observation identity")
        check(member["block"] == block["hash"][2:] and member["child_root"] == block["root"][2:] == block["header_root"][2:], "wrong member block/root")
        check(intent["digest"] == intent_digest(intent), "wrong intent digest")
        check(set(decided) == {"sequence", "intent_digest", "delivery", "digest"}, "wrong decided schema")
        check(type(decided["sequence"]) is int and decided["sequence"] == sequence and decided["intent_digest"] == intent["digest"], "wrong decided reference")
        check(decided["delivery"] == "BATCH" and decided["digest"] == decided_digest(intent["digest"], decided["delivery"]), "wrong decided digest/delivery")
    check(set(records) == expected and records[prefix + "counter"] == {"next": len(views)}, "wrong decision frontier")
    admission = {k: native_json(v) for k, v in case["admission_records"].items()}
    prefix = "extractor-admission-v1/" + producer + "/"
    check(admission == {prefix + "counter": {"next": len(views)}, prefix + "lowwater": {"sequence": len(views)}, prefix + "purged": {"sequence": len(views)}}, "no admission records or wrong admission frontier")


def check_import(case, blocks, errors, label):
    def check(ok, reason):
        if not ok:
            errors.append(f"{label}: {reason}")
    check(case.get("attach_error") is None and "attach_error" in case, "attachment failed or absent")
    check(case.get("import_error") is None and "import_error" in case, "native import failed or absent")
    check(case.get("head") == blocks, "wrong imported head")
    check(case.get("hooks_closed") is True, "unclosed tracing hooks")
    check(case.get("replay_transactions_equal") is True, "re-execution transaction observations differ")
    check(case.get("replay_system_calls_equal") is True, "re-execution system observations differ")
    views = case.get("blocks", [])
    check([b.get("number") for b in views] == list(range(1, blocks + 1)), "missing or duplicate block observations")
    previous = None
    for block in views:
        header = native_header(block["raw_block"])
        check(block["hash"] == header["hash"], "reported block hash differs from raw native header")
        check(block["number"] == header["number"], "reported block number differs from raw native header")
        check(block["root"] == header["root"] and block["header_root"] == header["root"], "reported state root differs from raw native header")
        check(header["time"] == block["number"] * 10, "native header timestamp differs from fixture")
        if previous is not None:
            check(header["parent"] == previous[2:], "native canonical chain is discontinuous")
        previous = header["hash"]
        for field in ("body_equal", "root_equal", "gas_equal"):
            check(block.get(field) is True, f"block {block.get('number')} {field}")
        check("replay_error" in block and block["replay_error"] is None, "re-execution failed or absent")
        check(isinstance(block.get("raw_block"), str) and bool(block["raw_block"]), "missing raw block")
        rows = block.get("receipts", [])
        check(block.get("receipt_count") == len(rows), "receipt count mismatch")
        for i, row in enumerate(rows):
            check(row.get("index") == i and row.get("receipt_equal") is True, "missing or differing receipt")
            check(isinstance(row.get("wire"), str) and bool(row["wire"]), "missing raw receipt")
    check(case.get("head_hash") == previous, "native final head differs from raw header")
    check_records(case, views, check)
    return views


def judge(observation):
    errors = []
    def check(ok, reason):
        if not ok:
            errors.append(reason)
    try:
        check(observation.get("kind") == "native-type-paths-eth" and observation.get("schema_version") == 1,
              "wrong observation kind/version")
        check(observation.get("native_commit") == NATIVE, "wrong native commit")
        check(observation.get("chain_id") == 31337 and observation.get("private_profile") == "private-type-paths-osaka-v1",
              "wrong private profile")
        main = observation["main"]
        blocks = check_import(main, 2, errors, "main")
        txs = main.get("transactions", [])
        check(len(txs) == 10, "expected ten transaction observations")
        check(all(len(b.get("receipts", [])) == 5 for b in blocks), "expected five receipts in each main block")
        receipts = [r for b in blocks for r in b.get("receipts", [])]
        check(len(receipts) == 10, "expected ten native receipts")
        for index, tx in enumerate(txs):
            kind, block = index % 5, index // 5 + 1
            expected_status = 1 if block == 1 else 0
            target = SUCCESS if block == 1 else REVERT
            prefix = f"transaction {index}: "
            check(tx.get("block") == block and tx.get("type") == kind and tx.get("nonce") == index,
                  prefix + "wrong block/type/nonce")
            check(tx.get("from") == SENDER and tx.get("status") == expected_status, prefix + "wrong sender/status")
            check("execution_error" in tx and tx["execution_error"] is None, prefix + "invalid included transaction")
            fields = tx["fields"]
            check(quantity(fields["type"]) == kind and quantity(fields["nonce"]) == index and
                  quantity(fields["gas"]) == 150000 and quantity(fields["value"]) == 1 and
                  fields["input"] == "0x" and fields["to"].lower() == target, prefix + "source fields differ")
            check(quantity(fields["chainId"]) == 31337, prefix + "source chain ID differs")
            if kind <= 1:
                check(quantity(fields["gasPrice"]) == 2_000_000_000, prefix + "gas price differs")
            else:
                check(quantity(fields["maxFeePerGas"]) == 2_000_000_000 and
                      quantity(fields["maxPriorityFeePerGas"]) == 1_000_000_000, prefix + "fee caps differ")
            if kind:
                access = fields["accessList"]
                check(access == [{"address": target, "storageKeys": [ZERO, ZERO]},
                                 {"address": target, "storageKeys": []}], prefix + "ordered access list differs")
            if kind == 3:
                check(fields["blobVersionedHashes"] == ["0x01" + "00" * 31] and
                      quantity(fields["maxFeePerBlobGas"]) == 1_000_000_000, prefix + "blob source fields differ")
            before, after = tx["before"], tx["after"]
            check(before[SENDER]["nonce"] == index and after[SENDER]["nonce"] == index + 1,
                  prefix + "sender nonce boundary differs")
            receipt = receipts[index]
            check(receipt["type"] == kind and receipt["status"] == expected_status and
                  receipt["gas_used"] == tx["gas_used"], prefix + "stored receipt differs from observed execution")
            check(tx["wire"] == receipt["transaction_wire"] and bool(tx["wire"]), prefix + "source bytes differ")
            if kind:
                check(tx["wire"].startswith(f"{kind:02x}"), prefix + "typed envelope prefix differs")
            gas_fee = quantity(receipt["gas_used"]) * quantity(receipt["effective_gas_price"])
            blob_fee = quantity(receipt["blob_gas_used"]) * quantity(receipt.get("blob_gas_price", 0))
            check(quantity(before[SENDER]["balance"]) - quantity(after[SENDER]["balance"]) ==
                  gas_fee + blob_fee + expected_status, prefix + "sender fee/value accounting differs")
            check(quantity(after[target]["balance"]) - quantity(before[target]["balance"]) == expected_status,
                  prefix + "target value boundary differs")
            check(before[REVERT]["slot0"] == ZERO and after[REVERT]["slot0"] == ZERO, prefix + "reverted storage survived")
            if kind == 4:
                auth = fields["authorizationList"]
                check(len(auth) == 1 and quantity(auth[0]["chainId"]) == 31337 and
                      quantity(auth[0]["nonce"]) == block - 1 and auth[0]["address"].lower() == DELEGATE,
                      prefix + "authorization source fields differ")
                check(before[AUTHORITY]["nonce"] == block - 1 and after[AUTHORITY]["nonce"] == block and
                      after[AUTHORITY]["code"] == "ef0100" + DELEGATE[2:], prefix + "authorization state differs")
        calls = main.get("system_calls", [])
        check(Counter((row.get("block"), row.get("from"), row.get("to")) for row in calls) ==
              Counter((block, SYSTEM, target) for block in (1, 2) for target in SYSTEM_TARGETS),
              "protocol system-call presence differs")
        forks = observation.get("forks", [])
        check([row.get("type") for row in forks] == [1, 2, 3, 4], "missing or duplicate fork controls")
        for fork in forks:
            kind = fork["type"]
            check(fork.get("before_number") == 1 and fork.get("at_number") == 2 and
                  fork.get("before_time") == 10 and fork.get("at_time") == 20, f"fork {kind}: wrong boundary")
            check(fork.get("signer_unsupported") is True and bool(fork.get("signer_error")), f"fork {kind}: signer accepted early")
            check_import(fork["accepted"], 2, errors, f"fork {kind} accepted")
            accepted = fork["accepted"].get("transactions", [])
            check(len(accepted) == 1 and accepted[0].get("type") == kind and accepted[0].get("block") == 2 and
                  accepted[0].get("status") == 1, f"fork {kind}: missing successful native transaction")
            bad = fork["rejected"]
            expected_error = "data blobs present in block body" if kind == 3 else "transaction type not supported"
            check(bad.get("attach_error") is None and "attach_error" in bad and bad.get("head") == 0 and
                  expected_error in (bad.get("import_error") or ""), f"fork {kind}: wrong import rejection/head")
        unknown = observation["unknown"]
        check(unknown.get("wire") == "7ec0" and unknown.get("unsupported") is True and bool(unknown.get("error")),
              "unknown native envelope was not rejected")
    except (KeyError, TypeError, ValueError, IndexError, AttributeError) as error:
        errors.append(f"Malformed native observation: {type(error).__name__}: {error}")
    return {"result": "FAIL" if errors else "PASS", "errors": errors, "public_profile_credit": False,
            "scope": "private ETH type-path fixture; no outside-transaction semantic or public-profile verdict"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("observation", type=Path)
    args = parser.parse_args()
    report = judge(json.loads(args.observation.read_text()))
    print(json.dumps(report, sort_keys=True))
    return int(report["result"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
