"""Compare private Parlia observations against independently fixed expectations."""

import re

from artifacts import closed, require

SYSTEM_ADDRESS = "0xfffffffffffffffffffffffffffffffffffffffe"
HASH = r"0x[0-9a-f]{64}"
ADDRESS = r"0x[0-9a-f]{40}"
TRANSACTION_FIELDS = {"from", "to", "nonce", "gas", "gas_price", "value", "input"}


def integer(value, name):
    require(type(value) is int and value >= 0, f"Invalid {name}")
    return value


def decimal(value, name):
    require(isinstance(value, str) and re.fullmatch(r"0|[1-9][0-9]*", value), f"Invalid {name}")
    return int(value)


def transaction(value, observed=False):
    closed(value, TRANSACTION_FIELDS | ({"hash", "index", "type"} if observed else set()), "transaction")
    for name in ("from", "to"):
        require(isinstance(value[name], str) and re.fullmatch(ADDRESS, value[name]), f"Invalid transaction {name}")
    integer(value["nonce"], "transaction nonce")
    for name in ("gas", "gas_price", "value"):
        decimal(value[name], "transaction " + name)
    require(isinstance(value["input"], str) and re.fullmatch(r"0x(?:[0-9a-f]{2})*", value["input"]), "Invalid input")
    if observed:
        require(isinstance(value["hash"], str) and re.fullmatch(HASH, value["hash"]), "Invalid transaction hash")
        integer(value["index"], "transaction index")
        require(type(value["type"]) is int and value["type"] == 0, "Expected a legacy fixture transaction")


def validate_oracle(expected):
    closed(expected, {"schema_version", "chain_id", "validator", "user_transaction", "system_transactions",
                      "accounts", "user_receipt", "unchecked"}, "BSC oracle")
    require(type(expected["schema_version"]) is int and expected["schema_version"] == 1 and
            type(expected["chain_id"]) is int and expected["chain_id"] == 714, "Wrong private BSC oracle")
    require(isinstance(expected["validator"], str) and re.fullmatch(ADDRESS, expected["validator"]), "Invalid validator")
    transaction(expected["user_transaction"])
    require(isinstance(expected["system_transactions"], list) and len(expected["system_transactions"]) == 9,
            "Nine native system transactions are required")
    for row in expected["system_transactions"]:
        transaction(row)
    require(isinstance(expected["accounts"], dict) and len(expected["accounts"]) == 4 and
            SYSTEM_ADDRESS in expected["accounts"], "Incomplete account oracle")
    for address, values in expected["accounts"].items():
        require(re.fullmatch(ADDRESS, address) and isinstance(values, dict) and
                set(values) in ({"nonce"}, {"balance", "nonce"}), "Invalid account oracle")
        integer(values["nonce"], "expected nonce")
        if "balance" in values:
            decimal(values["balance"], "expected balance")
    closed(expected["user_receipt"], {"status", "gas_used"}, "user receipt oracle")
    for name, value in expected["user_receipt"].items():
        integer(value, name)
    require(isinstance(expected["unchecked"], list) and expected["unchecked"] and
            all(isinstance(item, str) for item in expected["unchecked"]), "Unchecked scope must remain explicit")


def compare_native(expected, observation, states, identities, prepared):
    """Compare retained DB observations; this alone makes no runtime or suite claim."""
    validate_oracle(expected)
    require(len(identities) == 3 and len(set(identities)) == 3 and
            all(isinstance(item, str) and re.fullmatch(HASH, item) for item in identities), "Invalid CLI block identities")
    require(len(prepared) == 2 and all(row["number"] == n and row["hash"] == identities[n]
                                    for n, row in enumerate(prepared, 1)), "Prepared block identity mismatch")
    closed(observation, {"schema_version", "chain_id", "genesis_hash", "head_number", "head_hash", "blocks"},
           "BSC imported observation")
    require(type(observation["schema_version"]) is int and observation["schema_version"] == 1 and
            type(observation["chain_id"]) is int and observation["chain_id"] == 714 and
            observation["genesis_hash"] == identities[0] and
            type(observation["head_number"]) is int and observation["head_number"] == 2 and
            observation["head_hash"] == identities[2], "Imported chain identity mismatch")
    require(isinstance(observation["blocks"], list) and len(observation["blocks"]) == len(states) == 2,
            "Complete two-block observations are required")
    seen = set()
    for number, (block, state) in enumerate(zip(observation["blocks"], states), 1):
        closed(block, {"number", "hash", "timestamp", "parent_hash", "state_root", "coinbase", "difficulty", "gas_used",
                       "base_fee", "withdrawals", "transactions", "receipts"}, "observed block")
        require(type(block["number"]) is int and block["number"] == number and block["hash"] == identities[number]
                and block["parent_hash"] == identities[number - 1], "Observed block order/identity mismatch")
        require(type(block["timestamp"]) is int and block["timestamp"] >= 0, "Invalid native timestamp")
        for key in ("state_root", "timestamp", "gas_used"):
            if key in prepared[number - 1]:
                require(block[key] == prepared[number - 1][key], "Prepared block metadata mismatch")
        require(block["coinbase"] == expected["validator"] and block["difficulty"] == "2" and
                block["base_fee"] is None and type(block["withdrawals"]) is int and block["withdrawals"] == 0,
                "Expected private pre-London in-turn Parlia block")
        require(isinstance(block["state_root"], str) and re.fullmatch(HASH, block["state_root"]) and
                isinstance(state, dict) and isinstance(state.get("root"), str) and
                state["root"].removeprefix("0x").lower() == block["state_root"][2:], "State root mismatch")
        raw_accounts = state.get("accounts")
        require(isinstance(raw_accounts, dict), "Missing complete account dump")
        accounts = {}
        for address, values in raw_accounts.items():
            normalized = "0x" + address.removeprefix("0x").lower()
            require(re.fullmatch(ADDRESS, normalized) and normalized not in accounts,
                    "Native dump has an incomplete or duplicate address")
            accounts[normalized] = values
        for address, values in expected["accounts"].items():
            actual = accounts.get(address)
            if actual is None and address == SYSTEM_ADDRESS:
                actual = {"balance": "0", "nonce": 0}
            require(isinstance(actual, dict), "Expected account omitted from native dump")
            for key, value in values.items():
                require(type(actual.get(key)) is type(value) and actual[key] == value,
                        f"Account {address} {key} mismatch")
        wanted = [expected["user_transaction"], *expected["system_transactions"]] if number == 1 else []
        txs, receipts = block["transactions"], block["receipts"]
        require(isinstance(txs, list) and isinstance(receipts, list) and len(txs) == len(receipts) == len(wanted),
                "Native transaction/receipt count mismatch")
        if "transactions" in prepared[number - 1]:
            require(len(txs) == prepared[number - 1]["transactions"], "Prepared transaction count mismatch")
        cumulative = 0
        for index, (tx, receipt, wanted_tx) in enumerate(zip(txs, receipts, wanted)):
            transaction(tx, observed=True)
            require(tx["index"] == index and tx["hash"] not in seen and
                    all(tx[key] == wanted_tx[key] for key in TRANSACTION_FIELDS), "Native transaction mismatch")
            seen.add(tx["hash"])
            closed(receipt, {"transaction_hash", "transaction_index", "block_hash", "block_number", "status",
                             "gas_used", "cumulative_gas_used", "logs"}, "observed receipt")
            for key in ("transaction_index", "block_number", "status", "gas_used", "cumulative_gas_used"):
                integer(receipt[key], "receipt " + key)
            require(receipt["transaction_hash"] == tx["hash"] and receipt["transaction_index"] == index and
                    receipt["block_hash"] == block["hash"] and receipt["block_number"] == number and
                    receipt["status"] == 1 and isinstance(receipt["logs"], list), "Receipt identity/status mismatch")
            cumulative += receipt["gas_used"]
            require(receipt["cumulative_gas_used"] == cumulative, "Receipt gas accounting mismatch")
            if index == 0:
                require(all(receipt[key] == value for key, value in expected["user_receipt"].items()),
                        "User receipt oracle mismatch")
        require(integer(block["gas_used"], "block gas") == cumulative, "Header/receipt gas mismatch")
    require(observation["blocks"][0]["state_root"] == observation["blocks"][1]["state_root"],
            "No-user-transaction block changed state")
    return {"checked_scenarios": ["native-transfer", "no-user-transaction-block"], "unchecked": expected["unchecked"]}
