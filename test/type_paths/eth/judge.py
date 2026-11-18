#!/usr/bin/env python3
"""Judge private native ETH type-path facts against the independent oracle."""

import argparse
from collections import Counter
import json
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
    records = case.get("decision_records", {})
    intents = [key for key in records if key.endswith("/intent")]
    check(len(intents) == blocks, "missing or duplicate capture decisions")
    for key in intents:
        check(key[:-7] + "/decided" in records, "unresolved capture intent")
    admission = case.get("admission_records", {})
    check(bool(admission), "no admission records")
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
