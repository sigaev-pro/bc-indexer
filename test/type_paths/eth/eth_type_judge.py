#!/usr/bin/env python3
"""Judge private native ETH type-path facts against the independent oracle."""

import argparse
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
    """Read only the parent hash from the native block/header RLP lists."""
    data = bytes.fromhex(raw)
    def list_start(offset):
        prefix = data[offset]
        if prefix < 0xc0:
            raise ValueError("Expected native RLP list")
        width = 0 if prefix <= 0xf7 else prefix - 0xf7
        return offset + 1 + width
    offset = list_start(list_start(0))
    if data[offset] != 0xa0 or len(data[offset + 1:offset + 33]) != 32:
        raise ValueError("Missing native parent hash")
    return data[offset + 1:offset + 33].hex()


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
    for block in views:
        for field in ("body_equal", "root_equal", "gas_equal"):
            check(block.get(field) is True, f"block {block.get('number')} {field}")
        check("replay_error" in block and block["replay_error"] is None, "re-execution failed or absent")
        check(isinstance(block.get("raw_block"), str) and bool(block["raw_block"]), "missing raw block")
        rows = block.get("receipts", [])
        check(block.get("receipt_count") == len(rows), "receipt count mismatch")
        for i, row in enumerate(rows):
            check(row.get("index") == i and row.get("receipt_equal") is True, "missing or differing receipt")
            check(isinstance(row.get("wire"), str) and bool(row["wire"]), "missing raw receipt")
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
