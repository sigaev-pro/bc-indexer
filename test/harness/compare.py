"""Compare observations with literal expectations, never with derived balances."""

import re
import json

from artifacts import closed, digest, load, require, read_bytes, check_deadline


def validate_oracle(expected, prepared):
    closed(expected, {"schema_version", "oracle", "blocks"}, "oracle")
    require(type(expected["schema_version"]) is int and expected["schema_version"] == 1 and
            type(expected["oracle"]) is str and expected["oracle"], "Invalid oracle identity")
    require(type(expected["blocks"]) is list and len(expected["blocks"]) == 2,
            "Oracle requires two blocks")
    for number, (block, source) in enumerate(zip(expected["blocks"], prepared), 1):
        closed(block, {"number", "gas_used", "transactions", "base_fee", "accounts"}, "oracle block")
        closed(source, {"number", "gas_used", "transactions", "base_fee", "hash", "state_root"}, "prepared block")
        for key in ("number", "gas_used", "transactions"):
            require(type(block[key]) is int and type(source[key]) is int and block[key] == source[key],
                    "Prepared block differs from oracle")
        require(block["number"] == number and block["gas_used"] == (21000 if number == 1 else 0) and
                block["transactions"] == (1 if number == 1 else 0), "Invalid slice block shape")
        require(type(block["base_fee"]) is str and re.fullmatch(r"[0-9]+", block["base_fee"]) and
                source["base_fee"] == block["base_fee"], "Base fee differs from oracle")
        for key in ("hash", "state_root"):
            require(type(source[key]) is str and re.fullmatch(r"0x[0-9a-fA-F]{64}", source[key]),
                    "Invalid prepared block identity")
        require(type(block["accounts"]) is dict and len(block["accounts"]) == 3, "Oracle requires three accounts")
        for address, account in block["accounts"].items():
            require(re.fullmatch(r"[0-9a-f]{40}", address), "Invalid oracle account")
            closed(account, {"balance", "nonce"}, "oracle account")
            require(type(account["balance"]) is str and re.fullmatch(r"[0-9]+", account["balance"]) and
                    int(account["balance"]) < 2 ** 256 and type(account["nonce"]) is int and
                    0 <= account["nonce"] < 2 ** 64, "Invalid oracle amount or nonce")


def compare_state(observed, expected, state_root):
    require(type(observed) is dict and type(observed.get("root")) is str and observed["root"].removeprefix("0x").lower() ==
            state_root.removeprefix("0x").lower(), "Native state root differs from prepared input")
    accounts = observed.get("accounts")
    require(type(accounts) is dict, "Native dump has no accounts")
    normalized = {}
    for key, value in accounts.items():
        address = key.removeprefix("0x").lower()
        require(re.fullmatch(r"[0-9a-f]{40}", address) and address not in normalized,
                "Native dump has an incomplete or duplicate address")
        normalized[address] = value
    require(set(normalized) == set(expected["accounts"]), "Native account set is incomplete or unexpected")
    for address, expected_account in expected["accounts"].items():
        account = normalized[address]
        require(type(account.get("nonce")) is int and account["nonce"] == expected_account["nonce"] and
                account.get("balance") == expected_account["balance"], "Account state mismatch: " + address)


def compare_headers(admitted):
    logs = admitted.get("header_observations")
    require(type(logs) is list and len(logs) == 3, "Native header identity observations are missing")
    expected_hashes = [admitted["fixture"]["identity"]["genesis_hash"]] + [
        block["hash"] for block in admitted["fixture"]["prepared_blocks"]]
    deadline = admitted.get("deadline")

    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "Native header identity log repeats a JSON key")
            result[key] = value
        return result

    for number, (path, expected_hash) in enumerate(zip(logs, expected_hashes)):
        events = []
        for line in read_bytes(path, 8 * 1024 * 1024, deadline).decode("utf-8").splitlines():
            check_deadline(deadline)
            if not line.strip():
                continue
            try:
                event = json.loads(line, object_pairs_hook=pairs,
                                   parse_constant=lambda _: (_ for _ in ()).throw(ValueError("Nonfinite log")))
            except ValueError as error:
                raise ValueError("Native header identity log is malformed") from error
            require(type(event) is dict, "Native header identity log is not an object")
            if event.get("msg") == "State dump configured":
                events.append(event)
        require(len(events) == 1, "Native header identity event is missing or ambiguous")
        event = events[0]
        # The pinned native JSON handler renders *big.Int as a decimal string.
        require(type(event.get("block")) is str and event["block"] == str(number) and
                type(event.get("hash")) is str and re.fullmatch(r"0x[0-9a-f]{64}", event["hash"]) and
                event["hash"] == expected_hash, "Native header identity differs from prepared input")
    check_deadline(deadline)


def compare(admitted, dumps, exported):
    fixture = admitted["fixture"]
    expected = admitted["expected"]
    validate_oracle(expected, fixture["prepared_blocks"])
    deadline = admitted.get("deadline")
    require(digest(exported, 16 * 1024 * 1024, deadline=deadline) ==
            (fixture["full_chain"]["sha256"], fixture["full_chain"]["size_bytes"]),
            "Complete native chain differs from prepared input")
    compare_headers(admitted)
    require(len(dumps) == 2, "Both native block states are required")
    for dump, block, prepared in zip(dumps, expected["blocks"], fixture["prepared_blocks"]):
        compare_state(load(dump, 8 * 1024 * 1024, deadline), block, prepared["state_root"])
    return [{"scenario": "value-transfer", "result": "PASS", "block": fixture["prepared_blocks"][0]},
            {"scenario": "empty-block", "result": "PASS", "block": fixture["prepared_blocks"][1]}]
