"""Compare observations with literal expectations, never with derived balances."""

import re

from artifacts import closed, digest, load, require


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


def compare(admitted, dumps, exported):
    fixture = admitted["fixture"]
    expected = admitted["expected"]
    validate_oracle(expected, fixture["prepared_blocks"])
    require(digest(exported) == (fixture["full_chain"]["sha256"], fixture["full_chain"]["size_bytes"]),
            "Complete native chain differs from prepared input")
    require(len(dumps) == 2, "Both native block states are required")
    for dump, block, prepared in zip(dumps, expected["blocks"], fixture["prepared_blocks"]):
        compare_state(load(dump, 8 * 1024 * 1024), block, prepared["state_root"])
    return [{"scenario": "value-transfer", "result": "PASS", "block": fixture["prepared_blocks"][0]},
            {"scenario": "empty-block", "result": "PASS", "block": fixture["prepared_blocks"][1]}]
