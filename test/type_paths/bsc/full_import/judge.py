"""Independent selected-state and durable-record oracle for private BSC imports."""
import hashlib
import importlib.util
import json
from pathlib import Path
import re

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
spec = importlib.util.spec_from_file_location("bsc_full_import_encodings", ROOT / "test/type_paths/eth/eth_type_judge.py")
encoding = importlib.util.module_from_spec(spec)
spec.loader.exec_module(encoding)
q = encoding.quantity
SENDER = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
AUTHORITY = "0x2b5ad5c4795c026514f8317c7a215e218dccd6cf"
VALIDATOR = "0x6813eb9362372eef6200f3b1dbc3f819671cba69"
SUCCESS = "0x3000000000000000000000000000000000000001"
REVERT = "0x3000000000000000000000000000000000000002"
VALIDATOR_CONTRACT = "0x" + "1000".zfill(40)
STAKE_HUB = "0x" + "2002".zfill(40)
REWARD = "0x" + "1002".zfill(40)
BURN = "0x" + "dead".zfill(40)
SYSTEM = "0x" + "f" * 39 + "e"
GENESIS_TIME = 1794959990
INIT = "e1c7392a"
INITIALIZE = "8129fc1c"
DEPOSIT = "f340fa01"
ELECTION = "1e4c1524"
LEGACY_INIT = {"0x" + n.zfill(40) for n in ("1000", "1001", "1003", "1006", "1004", "1005", "2000")}
FEYNMAN_INIT = {"0x" + n.zfill(40) for n in ("2002", "2004", "2005", "2006", "3000")}
CURRENT_TIMES = ("feynmanTime", "feynmanFixTime", "cancunTime", "haberTime", "haberFixTime", "bohrTime",
                 "pascalTime", "pragueTime", "lorentzTime", "maxwellTime", "fermiTime", "osakaTime", "mendelTime", "pasteurTime")


def require(ok, message):
    if not ok:
        raise ValueError(message)


def normalized(value):
    require(isinstance(value, str) and re.fullmatch(r"(?:0x)?[0-9a-fA-F]{64}", value) is not None, "invalid hash")
    return value.lower().removeprefix("0x")


def provenance():
    return json.loads((HERE.parent / "modern_init/provenance.json").read_text())["contracts"]


def check_genesis(genesis, config):
    require(genesis["config"] == config and config["chainId"] == 714, "wrong private configuration")
    require(q(genesis["timestamp"]) == GENESIS_TIME, "wrong genesis time")
    require(all(config[key] == GENESIS_TIME + 6 for key in CURRENT_TIMES), "incoherent current fork schedule")
    require(config["jennerTime"] == GENESIS_TIME + 864000 and config["keplerTime"] == config["shanghaiTime"] == 0,
            "wrong warmup or future fork")
    for key, value in config.items():
        if key.endswith("Block") and value is not None:
            require(value == 0, "delayed block fork")
    for row in provenance():
        account = genesis["alloc"][row["address"].removeprefix("0x")]
        actual = hashlib.sha256(bytes.fromhex(account["code"].removeprefix("0x"))).hexdigest()
        require(actual == row["bytecode_sha256"], "modern contract bytecode differs")
        require(q(account["balance"]) == 0 and not account.get("storage"), "unexpected preinitialized system state")


def check_bridge(bridge, blocks, genesis_header):
    n = len(blocks)
    require(bridge["mode"] == "LIVE" and bridge["head_number"] == n and
            bridge["head_hash"] == normalized(blocks[-1]["hash"]), "wrong captured head")
    require(bridge["counters"] == [n, n] and bridge["open_observations"] == [], "wrong observation frontier")
    require(bridge["admission_records"] == {"counter": {"next": n}, "lowwater": {"sequence": n}, "purged": {"sequence": n}},
            "wrong admission frontier")
    records = bridge["decision_records"]
    expected_keys = {"counter"}
    expected_obligations = []
    previous = {"number": 0, "hash": normalized(genesis_header["hash"])}
    for index, block in enumerate(blocks):
        key = f"{index:016x}"
        expected_keys.update((key + "/intent", key + "/decided"))
        intent, decided = records[key + "/intent"], records[key + "/decided"]
        require(set(intent) == {"native_store_id", "producer", "sequence", "kind", "old_head", "new_head", "members", "evidence_digest", "events", "digest"}, "wrong intent schema")
        require(intent["native_store_id"] == "0123456789abcdef0123456789abcdef" and intent["producer"] == "bsc-probe.run-1" and
                type(intent["sequence"]) is int and intent["sequence"] == index and intent["kind"] == "IMPORT" and intent["events"] is True,
                "wrong intent identity")
        head = {"number": index + 1, "hash": normalized(block["hash"])}
        require(intent["old_head"] == previous and intent["new_head"] == head, "wrong decision heads")
        previous = head
        require(len(intent["members"]) == 1, "wrong member count")
        member = intent["members"][0]
        require(set(member) == {"observation", "block", "exec_ready_digest", "child_root"} and
                member["observation"] == f"bsc-probe.run-1.{index:016x}" and member["block"] == head["hash"] and
                member["child_root"] == normalized(block["root"]), "wrong capture member")
        require(intent["digest"] == encoding.intent_digest(intent), "wrong intent digest")
        require(decided == {"sequence": index, "intent_digest": intent["digest"], "delivery": "BATCH",
                            "digest": encoding.decided_digest(intent["digest"], "BATCH")}, "wrong decided record")
        expected_obligations.append({"sequence": index, "intent_digest": intent["digest"], "decided_digest": decided["digest"]})
    require(set(records) == expected_keys and records["counter"] == {"next": n}, "wrong decision frontier")
    require(bridge["obligations"] == expected_obligations, "wrong delivery obligations")


def balance(state, address):
    return q(state[address]["balance"])


def slot(state, address, key):
    return q(state[address]["storage"][key])


def check_system(row):
    before, after = row["before"], row["after"]
    to, selector, value = row["to"].lower(), row["data"][:8], q(row["value"])
    require(row["type"] == 0 and row["from"].lower() == VALIDATOR and q(row["gas_price"]) == 0, "wrong native system transaction")
    require(after[VALIDATOR]["nonce"] == before[VALIDATOR]["nonce"] + 1 and row["nonce"] == before[VALIDATOR]["nonce"], "system nonce transition")
    require(balance(after, VALIDATOR) == balance(before, VALIDATOR) - value, "system sender balance")
    require(q(row["receipt"]["status"]) == 1 and row["error"] is None, "failed system transaction")
    if selector in (INIT, INITIALIZE):
        expected = LEGACY_INIT if selector == INIT else FEYNMAN_INIT
        require(to in expected and value == 0, "wrong initializer")
        require(slot(before, to, "0") == 0 and slot(after, to, "0") == 1, "initializer state flag")
        for address in before:
            require(balance(before, address) == balance(after, address), "initializer moved funds")
        if to == VALIDATOR_CONTRACT:
            require(slot(before, to, "1") == 0 and slot(after, to, "1") == 21, "validator initialization array")
    elif selector == DEPOSIT:
        require(to == VALIDATOR_CONTRACT and value > 0 and row["data"][8:] == VALIDATOR[2:].zfill(64), "wrong deposit input")
        require(slot(before, to, "0") == 1 and slot(before, to, "23") == slot(before, to, "24") == 0, "deposit preconditions")
        require(slot(after, to, "6") == 1000 and slot(after, to, "15") == 625 and slot(after, to, "20") == 1, "deposit ratios")
        reward, burn = value * 625 // 10000, value * 1000 // 10000
        remaining = value - reward - burn
        require(balance(after, to) - balance(before, to) == remaining and balance(after, REWARD) - balance(before, REWARD) == reward and
                balance(after, BURN) - balance(before, BURN) == burn, "deposit distribution")
        index = slot(before, to, "validator_index")
        require(index in (0, 1) and slot(after, to, "validator_index") == index, "deposit validator mapping")
        delta = remaining if index == 1 else 0
        require(slot(after, to, "3") - slot(before, to, "3") == delta and
                slot(after, to, "first_validator_incoming") - slot(before, to, "first_validator_incoming") == delta,
                "deposit active income")
    elif selector == ELECTION:
        require(to == VALIDATOR_CONTRACT and value == 0, "wrong election input")
        require(slot(before, to, "validator_index") == 0 and slot(after, to, "validator_index") == 1 and
                slot(after, to, "1") == 1 and slot(after, to, "3") == slot(after, to, "first_validator_incoming") == 0,
                "election did not install owned validator")
        require(balance(after, to) == 0 and balance(after, REWARD) - balance(before, REWARD) == balance(before, to), "election dusk transfer")
    else:
        raise ValueError("unmodelled system transaction")


def check_user(row, phase, index):
    before, after, receipt = row["before"], row["after"], row["receipt"]
    kind, fields = row["type"], row["fields"]
    require(row["from"].lower() == SENDER and row["nonce"] == index and before[SENDER]["nonce"] == index and after[SENDER]["nonce"] == index + 1,
            "user sender/nonce transition")
    price = 2000000000 if kind <= 1 else 1000000000
    require(q(row["gas_price"]) == price, "wrong effective fee")
    status = 0 if phase == "revert" else 1
    require(q(receipt["status"]) == status and row["error"] is None, "wrong user status")
    fee = q(receipt["gasUsed"]) * price
    if kind == 3:
        require(q(receipt["blobGasUsed"]) == 131072 and q(receipt["blobGasPrice"]) == 1, "wrong blob fees")
        fee += 131072
    value = 2001 * 10**18 if phase == "registration" else 1
    require(q(row["value"]) == value and balance(before, SENDER) - balance(after, SENDER) == fee + status * value, "user fee/value accounting")
    require(balance(after, SYSTEM) - balance(before, SYSTEM) == fee, "system fee accumulation")
    if phase == "registration":
        require(kind == 0 and row["to"].lower() == STAKE_HUB and value == 2001 * 10**18, "wrong registration")
        return
    require(phase in ("success", "revert") and kind == (index - 1) % 5 and row["time"] >= GENESIS_TIME + 6, "wrong current type row")
    target = SUCCESS if phase == "success" else REVERT
    recipient = AUTHORITY if kind == 4 else target
    require(row["to"].lower() == recipient and q(fields["chainId"]) == 714 and q(fields["gas"]) == 150000 and fields["input"] == "0x", "wrong source fields")
    if kind <= 1:
        require(q(fields["gasPrice"]) == 2000000000, "wrong gasPrice")
    else:
        require(q(fields["maxFeePerGas"]) == 2000000000 and q(fields["maxPriorityFeePerGas"]) == 1000000000, "wrong fee caps")
    if kind:
        require(fields["accessList"] == [{"address": target, "storageKeys": ["0x" + "0" * 64] * 2}, {"address": target, "storageKeys": []}], "access list order differs")
    if kind == 3:
        expected_hash = "0x01" + hashlib.sha256(bytes([192]) + bytes(47)).hexdigest()[2:]
        require(fields["blobVersionedHashes"] == [expected_hash] and q(fields["maxFeePerBlobGas"]) == 1000000000, "wrong blob input")
    require(balance(after, recipient) - balance(before, recipient) == status, "user target value")
    require(slot(after, REVERT, "0") == 0, "reverted storage survived")
    if phase == "success":
        require(slot(after, recipient, "0") == 1, "successful storage missing")
    if kind == 4:
        auth = fields["authorizationList"]
        nonce = 0 if phase == "success" else 1
        require(len(auth) == 1 and q(auth[0]["chainId"]) == 714 and q(auth[0]["nonce"]) == nonce and auth[0]["address"].lower() == target,
                "wrong authorization")
        require(before[AUTHORITY]["nonce"] == nonce and after[AUTHORITY]["nonce"] == nonce + 1 and
                normalized(after[AUTHORITY]["code_hash"]) == encoding.keccak256(bytes.fromhex("ef0100" + target[2:])).hex(), "authorization did not persist")
        require(slot(after, AUTHORITY, "0") == 1, "authority execution state differs")


def check(report, generated, genesis):
    try:
        require(type(report.get("schema_version")) is int and report["schema_version"] == 1 and report["scope"] == "private-current-fork-parlia-import", "wrong report")
        require(all(report[key] == "NOT_RUN" for key in ("public_profiles", "activation_import_controls", "unknown_import_control")), "unexecuted profile/control credit")
        check_genesis(genesis, report["config"])
        require(4 <= len(generated) <= 40 and report["blocks"] == len(generated), "wrong block coverage")
        genesis_header = encoding.native_header(report["genesis_wire"])
        previous = genesis_header
        for number, block in enumerate(generated, 1):
            native = encoding.native_header(block["wire"])
            require(native["number"] == block["number"] == number and native["time"] == block["time"] and
                    native["hash"] == block["hash"] and native["root"] == block["root"] and native["parent"] == normalized(previous["hash"]), "block identity/continuity differs")
            previous = native
        require([b["phase"] for b in generated if b["phase"] != "warmup"] == ["registration", "success", "revert"], "missing registration or target blocks")
        targets = [b for b in generated if b["phase"] in ("success", "revert")]
        require(all(b["validator_index"] == "1" and b["time"] >= GENESIS_TIME + 6 for b in targets), "target validator not active")
        check_bridge(report["bridge"], generated, genesis_header)
        require(report["reimport_before"] == report["bridge"] == report["reimport_after"], "reimport changed durable capture")
        rows = report["transactions"]
        require(len(rows) == sum(b["transactions"] for b in generated), "missing transaction snapshots")
        user_index = 0; init_seen = set(); feynman_seen = set(); election_count = 0
        for block in generated:
            block_rows = [r for r in rows if r["block"] == block["number"]]
            require(len(block_rows) == block["transactions"], "wrong per-block row count")
            seen_system = False; target_types = []
            for row in block_rows:
                require(row["time"] == block["time"] and type(row["system"]) is bool, "wrong transaction context")
                require(encoding.keccak256(bytes.fromhex(row["wire"])).hex() == normalized(row["hash"]), "wire identity differs")
                require(normalized(row["receipt"]["transactionHash"]) == normalized(row["hash"]) and q(row["receipt"]["type"]) == row["type"] and
                        row["observed_receipt_wire"] == row["stored_receipt_wire"] and bool(row["stored_receipt_wire"]), "receipt binding differs")
                classified = row["from"].lower() == VALIDATOR and row["to"].lower() in {r["address"] for r in provenance()} and q(row["gas_price"]) == 0
                require(row["system"] == classified, "native classifier boundary differs")
                if row["system"]:
                    seen_system = True; check_system(row)
                    selector = row["data"][:8]
                    if selector == INIT:
                        require(row["to"].lower() not in init_seen and row["block"] == 1, "repeated legacy initializer"); init_seen.add(row["to"].lower())
                    if selector == INITIALIZE:
                        require(row["to"].lower() not in feynman_seen and row["block"] == 2, "repeated Feynman initializer"); feynman_seen.add(row["to"].lower())
                    if selector == ELECTION: election_count += 1
                else:
                    require(not seen_system, "user transaction after system transaction")
                    check_user(row, block["phase"], user_index); user_index += 1
                    if block["phase"] in ("success", "revert"): target_types.append(row["type"])
            if block["phase"] in ("success", "revert"):
                require(target_types == list(range(5)), "missing native type variant")
        require(user_index == 11 and init_seen == LEGACY_INIT and feynman_seen == FEYNMAN_INIT and election_count == 1, "incomplete bootstrap/current execution")
        return {"result": "PASS", "errors": [], "scope": "private current-fork import selected-state fixture", "public_profile_credit": False,
                "activation_import_controls": "NOT_RUN", "unknown_import_control": "NOT_RUN"}
    except (ValueError, KeyError, TypeError, IndexError, AttributeError) as error:
        return {"result": "FAIL", "errors": [str(error)], "public_profile_credit": False}
