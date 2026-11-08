"""Compare private Bor server observations with independently fixed values."""

import re

from artifacts import closed, require

ZERO = "0x" + "0" * 40
HASH = r"0x[0-9a-f]{64}"
ADDRESS = r"0x[0-9a-f]{40}"
TRANSACTION = {"from", "to", "nonce", "gas", "gas_price", "value", "input"}


def pattern(value, expression, name):
    require(isinstance(value, str) and re.fullmatch(expression, value), f"Invalid {name}")


def integer(value, name):
    require(type(value) is int and value >= 0, f"Invalid {name}")


def decimal(value, name):
    pattern(value, r"0|[1-9][0-9]*", name)


def transaction(value, observed=False):
    closed(value, TRANSACTION | ({"hash", "index", "type"} if observed else set()), "transaction")
    for name in ("from", "to"):
        pattern(value[name], ADDRESS, name)
    integer(value["nonce"], "nonce")
    for name in ("gas", "gas_price", "value"):
        decimal(value[name], name)
    pattern(value["input"], r"0x(?:[0-9a-f]{2})*", "transaction input")
    if observed:
        pattern(value["hash"], HASH, "transaction hash")
        integer(value["index"], "transaction index")
        require(type(value["type"]) is int and value["type"] == 0, "Expected legacy transaction")


def log_payload(value):
    pattern(value["address"], ADDRESS, "log address")
    require(isinstance(value["topics"], list) and len(value["topics"]) == 4, "Invalid Bor log topics")
    for topic in value["topics"]:
        pattern(topic, HASH, "log topic")
    pattern(value["data"], r"0x[0-9a-f]{320}", "Bor log data")


def expectations(value):
    closed(value, {"schema_version", "kind", "chain_id", "validator", "user_transaction", "blocks", "accounts", "logs"}, "oracle")
    require(type(value["schema_version"]) is int and value["schema_version"] == 1 and
            value["kind"] == "bor-private-transfer", "Unsupported Bor oracle")
    require(type(value["chain_id"]) is int and value["chain_id"] == 15001, "Expected private chain 15001")
    pattern(value["validator"], ADDRESS, "validator")
    transaction(value["user_transaction"])
    require(isinstance(value["blocks"], list) and len(value["blocks"]) == 2, "Expected two oracle blocks")
    for n, block in enumerate(value["blocks"], 1):
        closed(block, {"number", "gas_limit", "gas_used", "base_fee", "transactions"}, "oracle block")
        for name in ("number", "gas_limit", "gas_used", "transactions"):
            integer(block[name], name)
        require(block["number"] == n and block["transactions"] == (1 if n == 1 else 0), "Unexpected fixture block")
        decimal(block["base_fee"], "base fee")
    require(isinstance(value["accounts"], dict) and len(value["accounts"]) == 4, "Expected four oracle accounts")
    for address, account in value["accounts"].items():
        pattern(address, ADDRESS, "oracle address")
        closed(account, {"balance", "nonce"}, "oracle account")
        decimal(account["balance"], "balance"); integer(account["nonce"], "nonce")
    require(isinstance(value["logs"], list) and len(value["logs"]) == 2, "Expected transfer and fee logs")
    for row in value["logs"]:
        closed(row, {"address", "topics", "data"}, "oracle log"); log_payload(row)


def account_state(state, expected, state_root):
    require(isinstance(state, dict) and isinstance(state.get("accounts"), dict), "Missing complete state accounts")
    root = state.get("root")
    require(isinstance(root, str) and "0x" + root.removeprefix("0x").lower() == state_root, "State root mismatch")
    accounts = {}
    for key, value in state["accounts"].items():
        require(isinstance(key, str), "Invalid state address")
        address = "0x" + key.lower().removeprefix("0x")
        pattern(address, ADDRESS, "state address")
        require(address not in accounts, "Duplicate state address")
        accounts[address] = value
    for address, wanted in expected.items():
        require(address in accounts, "Expected account omitted")
        actual = accounts[address]
        require(isinstance(actual, dict), "Invalid state account")
        decimal(actual.get("balance"), "observed balance"); integer(actual.get("nonce"), "observed nonce")
        require(actual["balance"] == wanted["balance"] and actual["nonce"] == wanted["nonce"], "Account mismatch")


def compare_native(expected, observed, states, identities, prepared, config_sha256):
    expectations(expected)
    require(isinstance(identities, list) and len(identities) == 3 and len(set(identities)) == 3, "Missing unique native identities")
    for identity in identities:
        pattern(identity, HASH, "native identity")
    pattern(config_sha256, r"[0-9a-f]{64}", "config digest")
    closed(observed, {"schema_version", "chain_id", "genesis_hash", "config_sha256", "head_number", "head_hash", "blocks"}, "observation")
    require(type(observed["schema_version"]) is int and observed["schema_version"] == 1, "Unsupported observation")
    require(type(observed["chain_id"]) is int and observed["chain_id"] == expected["chain_id"] and
            observed["genesis_hash"] == identities[0] and observed["config_sha256"] == config_sha256,
            "Native genesis/config mismatch")
    require(type(observed["head_number"]) is int and observed["head_number"] == 2 and
            observed["head_hash"] == identities[2], "Terminal native head mismatch")
    require(isinstance(observed["blocks"], list) and len(observed["blocks"]) == 2 and
            isinstance(states, list) and len(states) == 2 and isinstance(prepared, list) and len(prepared) == 2,
            "Incomplete native chain")
    previous_time = None
    for n, (block, wanted, state, bound) in enumerate(zip(observed["blocks"], expected["blocks"], states, prepared), 1):
        closed(block, {"number", "hash", "parent_hash", "state_root", "timestamp", "coinbase", "author",
                       "gas_limit", "gas_used", "base_fee", "withdrawals", "transactions", "receipts"}, "block")
        for field in ("number", "timestamp", "gas_limit", "gas_used", "withdrawals"):
            integer(block[field], field)
        pattern(block["state_root"], HASH, "state root")
        require(block["number"] == n and block["hash"] == identities[n] and block["parent_hash"] == identities[n - 1], "Native header chain mismatch")
        require(isinstance(bound, dict) and type(bound.get("number")) is int and bound["number"] == n and
                bound.get("hash") == block["hash"] and type(bound.get("timestamp")) is int and
                bound["timestamp"] == block["timestamp"], "Prepared block mismatch")
        require(previous_time is None or block["timestamp"] > previous_time, "Block timestamps must increase")
        previous_time = block["timestamp"]
        require(block["coinbase"] == ZERO and block["author"] == expected["validator"], "Native author/coinbase mismatch")
        require(all(block[k] == wanted[k] for k in ("gas_limit", "gas_used", "base_fee")) and
                block["withdrawals"] == 0, "Native gas/base-fee/withdrawals mismatch")
        require(isinstance(block["transactions"], list) and isinstance(block["receipts"], list) and
                len(block["transactions"]) == len(block["receipts"]) == wanted["transactions"], "Body/receipt count mismatch")
        if n == 1:
            tx, receipt = block["transactions"][0], block["receipts"][0]
            transaction(tx, True)
            require(tx["index"] == 0 and {k: tx[k] for k in TRANSACTION} == expected["user_transaction"], "User transaction mismatch")
            closed(receipt, {"transaction_hash", "transaction_index", "block_hash", "block_number", "status", "type",
                             "gas_used", "cumulative_gas_used", "effective_gas_price", "contract_address", "logs"}, "receipt")
            for field in ("transaction_index", "block_number", "status", "type", "gas_used", "cumulative_gas_used"):
                integer(receipt[field], field)
            require(receipt["transaction_hash"] == tx["hash"] and receipt["transaction_index"] == 0 and
                    receipt["block_hash"] == block["hash"] and receipt["block_number"] == n and receipt["status"] == 1 and
                    receipt["type"] == 0 and receipt["gas_used"] == receipt["cumulative_gas_used"] == wanted["gas_used"] and
                    receipt["effective_gas_price"] == expected["user_transaction"]["gas_price"] and
                    receipt["contract_address"] is None, "Receipt identity/result mismatch")
            require(isinstance(receipt["logs"], list) and len(receipt["logs"]) == 2, "Expected two native Bor logs")
            for index, (row, log) in enumerate(zip(receipt["logs"], expected["logs"])):
                closed(row, {"address", "topics", "data", "transaction_hash", "transaction_index", "block_hash",
                             "block_number", "log_index", "removed"}, "log")
                log_payload(row)
                for field in ("transaction_index", "block_number", "log_index"):
                    integer(row[field], field)
                require({k: row[k] for k in ("address", "topics", "data")} == log and row["transaction_hash"] == tx["hash"] and
                        row["transaction_index"] == 0 and row["block_hash"] == block["hash"] and row["block_number"] == n and
                        row["log_index"] == index and row["removed"] is False, "Native Bor log mismatch")
        account_state(state, expected["accounts"], block["state_root"])
    require(observed["blocks"][0]["state_root"] == observed["blocks"][1]["state_root"], "Empty block changed state")
