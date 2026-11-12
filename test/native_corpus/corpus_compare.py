#!/usr/bin/env python3
"""Compare a native corpus observation with the independent expected results.

Usage: corpus_compare.py EXPECTED.json OBSERVATION.json [--report REPORT.json]

The observation is written by a client block from native output (see the
"Observation format" section of README.md). The comparator never changes an
expected value from an observation. It joins blocks by number and
transactions by block and index, and reports every difference with its place.
Each case gets PASS or FAIL from the differences at its places, or
NOT_APPLICABLE with the oracle's reason. Exit 0 means every applicable case
passed; exit 1 means a difference, a format error or an unreadable
observation (the report then names the error); exit 2 means a usage error.
"""

import json
import sys

HEX = set("0123456789abcdef")
TX_FIELDS = ("type", "from", "nonce", "to", "value", "gas", "gas_price", "max_fee_per_gas",
             "max_priority_fee_per_gas", "data")
SYSTEM_FIELDS = ("from", "nonce", "to", "value", "data", "gas", "gas_price", "status")
FRAME_FIELDS = ("type", "from", "to", "value", "input", "output", "error")


class FormatError(Exception):
    """The observation does not follow the observation format."""


def require(condition, message):
    if not condition:
        raise FormatError(message)


def is_hex(value, size=None):
    return isinstance(value, str) and set(value) <= HEX and len(value) % 2 == 0 and (
        size is None or len(value) == size)


def is_int(value):
    """An integer that is not a boolean (JSON true and false are not counters)."""
    return isinstance(value, int) and not isinstance(value, bool)


def is_digits(value):
    return isinstance(value, str) and value.isascii() and value.isdigit()


def is_decimal(value):
    return is_digits(value) and (value == "0" or not value.startswith("0"))


def check_account(row, where):
    require(isinstance(row, dict), f"{where}: account is not an object")
    require(is_decimal(row.get("balance")), f"{where}: balance must be a decimal string")
    require(is_int(row.get("nonce")) and row["nonce"] >= 0, f"{where}: nonce must be an integer")
    require(is_hex(row.get("code")), f"{where}: code must be lowercase hex without 0x")
    storage = row.get("storage", {})
    require(isinstance(storage, dict), f"{where}: storage must be an object")
    for slot, value in storage.items():
        require(is_digits(slot) and is_hex(value, 64), f"{where}: storage {slot} must be a 64-digit hex word")


def check_log(log, where):
    require(isinstance(log, dict) and set(log) == {"address", "topics", "data"}, f"{where}: bad log fields")
    require(is_hex(log["address"], 40), f"{where}: log address")
    require(isinstance(log["topics"], list) and all(is_hex(t, 64) for t in log["topics"]), f"{where}: log topics")
    require(is_hex(log["data"]), f"{where}: log data")


def check_frame(frame, where):
    require(isinstance(frame, dict), f"{where}: frame is not an object")
    for key in FRAME_FIELDS:
        require(key in frame, f"{where}: frame lacks {key}")
    require(frame["type"] in ("CALL", "STATICCALL", "DELEGATECALL", "CALLCODE", "CREATE", "CREATE2"),
            f"{where}: frame type")
    require(is_hex(frame["from"], 40) and is_hex(frame["to"], 40), f"{where}: frame address")
    require(is_decimal(frame["value"]), f"{where}: frame value")
    require(is_hex(frame["input"]) and is_hex(frame["output"]), f"{where}: frame input or output")
    require(frame["error"] is None or isinstance(frame["error"], str), f"{where}: frame error")
    require(isinstance(frame.get("calls"), list), f"{where}: frame calls")
    for number, call in enumerate(frame["calls"]):
        check_frame(call, f"{where}.calls[{number}]")


def check_observation(observation):
    """Check the observation format. Raise FormatError on the first problem."""
    require(isinstance(observation, dict), "observation is not an object")
    require(observation.get("schema_version") == 1 and observation.get("kind") == "native-corpus-observation",
            "not a version 1 native corpus observation")
    require(observation.get("family") in ("eth", "bsc", "poly"), "unknown family")
    require(is_int(observation.get("chain_id")), "chain_id must be an integer")
    source = observation.get("source")
    require(isinstance(source, dict) and source, "source must name the native build and tool")
    require(isinstance(observation.get("blocks"), list) and observation["blocks"], "blocks must be a non-empty list")
    for block in observation["blocks"]:
        where = f"block {block.get('number') if isinstance(block, dict) else '?'}"
        require(isinstance(block, dict) and is_int(block.get("number")), f"{where}: number")
        require(is_hex(block.get("hash"), 64) and is_hex(block.get("state_root"), 64), f"{where}: hash or root")
        require(block.get("base_fee") is None or is_decimal(block["base_fee"]), f"{where}: base_fee")
        require(is_int(block.get("gas_limit")) and is_int(block.get("gas_used")),
                f"{where}: gas_limit and gas_used must be integers")
        require(is_int(block.get("body_tx_count")), f"{where}: body_tx_count")
        for key in ("transactions", "system_transactions", "state_sync"):
            require(isinstance(block.get(key), list), f"{where}: {key} must be a list")
        for tx in block["transactions"]:
            tx_where = f"{where} tx {tx.get('index') if isinstance(tx, dict) else '?'}"
            require(isinstance(tx, dict) and is_int(tx.get("index")), f"{tx_where}: index")
            require(is_hex(tx.get("hash"), 64), f"{tx_where}: hash")
            receipt = tx.get("receipt")
            require(isinstance(receipt, dict), f"{tx_where}: receipt")
            for key in ("status", "gas_used", "cumulative_gas_used"):
                require(is_int(receipt.get(key)), f"{tx_where}: receipt {key}")
            require(is_decimal(receipt.get("effective_gas_price")), f"{tx_where}: effective_gas_price")
            require(isinstance(receipt.get("logs"), list), f"{tx_where}: logs")
            for number, log in enumerate(receipt["logs"]):
                check_log(log, f"{tx_where} log {number}")
            check_frame(tx.get("frame"), f"{tx_where} frame")
            for key in ("state_before", "state_after"):
                require(isinstance(tx.get(key), dict), f"{tx_where}: {key}")
                for address, row in tx[key].items():
                    require(is_hex(address, 40), f"{tx_where}: {key} address")
                    check_account(row, f"{tx_where} {key} {address}")
        for tx in block["system_transactions"]:
            require(isinstance(tx, dict) and all(key in tx for key in SYSTEM_FIELDS + ("index",)),
                    f"{where}: system transaction fields")
        for event in block["state_sync"]:
            require(isinstance(event, dict) and is_int(event.get("id")), f"{where}: state sync id")
            for number, log in enumerate(event.get("logs", [])):
                check_log(log, f"{where} state sync log {number}")
        require(isinstance(block.get("state_after_block"), dict), f"{where}: state_after_block")
        for address, row in block["state_after_block"].items():
            require(is_hex(address, 40), f"{where}: state_after_block address")
            check_account(row, f"{where} state_after_block {address}")
    decisions = observation.get("decisions")
    require(isinstance(decisions, list), "decisions must be a list")
    for row in decisions:
        require(isinstance(row, dict) and is_int(row.get("block")) and isinstance(row.get("kind"), str),
                "each decision must name an integer block and a kind")
    reimport = observation.get("reimport")
    require(isinstance(reimport, dict) and is_int(reimport.get("new_observations")) and
            is_int(reimport.get("new_decisions")), "reimport must count new observations and decisions")


class Diff:
    def __init__(self):
        self.rows = []

    def add(self, place, field, expected, observed):
        self.rows.append({"place": place, "field": field, "expected": expected, "observed": observed})

    def equal(self, place, field, expected, observed):
        if expected != observed:
            self.add(place, field, expected, observed)


def compare_accounts(diff, place, field, expected, observed):
    for address, row in sorted(expected.items()):
        got = observed.get(address)
        if got is None:
            diff.add(place, f"{field}.{address}", row, None)
            continue
        for key in ("balance", "nonce", "code"):
            diff.equal(place, f"{field}.{address}.{key}", row[key], got.get(key))
        for slot, value in sorted(row.get("storage", {}).items()):
            diff.equal(place, f"{field}.{address}.storage.{slot}", value, got.get("storage", {}).get(slot))


def frame_view(frame):
    return {**{key: frame[key] for key in FRAME_FIELDS}, "calls": [frame_view(call) for call in frame["calls"]]}


def compare_transaction(diff, place, expected, observed):
    transaction = expected["transaction"]
    for key in TX_FIELDS:
        if key in transaction:
            diff.equal(place, f"transaction.{key}", transaction[key], observed.get(key))
    for key in ("status", "gas_used", "cumulative_gas_used", "logs"):
        diff.equal(place, f"receipt.{key}", expected["receipt"][key], observed["receipt"][key])
    diff.equal(place, "receipt.effective_gas_price", expected["effective_gas_price"],
               observed["receipt"]["effective_gas_price"])
    diff.equal(place, "frame", frame_view(expected["frame"]), frame_view(observed["frame"]))
    compare_accounts(diff, place, "state_before", expected["state_before"], observed["state_before"])
    compare_accounts(diff, place, "state_after", expected["state_after"], observed["state_after"])


def compare_block(diff, expected, observed, parent):
    number = expected["number"]
    place = [number, None]
    diff.equal(place, "body_tx_count", expected["tx_count"], observed["body_tx_count"])
    diff.equal(place, "base_fee", expected["base_fee"], observed["base_fee"])
    if expected["gas_limit"] is not None:
        diff.equal(place, "gas_limit", expected["gas_limit"], observed["gas_limit"])
    if expected["gas_used"] is not None:
        diff.equal(place, "gas_used", expected["gas_used"], observed["gas_used"])
    diff.equal(place, "transaction indexes", [tx["index"] for tx in expected["transactions"]],
               [tx["index"] for tx in observed["transactions"]])
    by_index = {tx["index"]: tx for tx in observed["transactions"]}
    for tx in expected["transactions"]:
        if tx["index"] in by_index:
            compare_transaction(diff, [number, tx["index"]], tx, by_index[tx["index"]])
    first_system = len(expected["transactions"])
    kinds = [row for row in expected["system"] if "from" in row]
    diff.equal(place, "system transaction count", len(kinds), len(observed["system_transactions"]))
    for offset, (row, got) in enumerate(zip(kinds, observed["system_transactions"])):
        system_place = [number, row["kind"]]
        diff.equal(system_place, "index", first_system + offset, got["index"])
        for key in SYSTEM_FIELDS:
            diff.equal(system_place, key, row[key], got[key])
    syncs = [row for row in expected["system"] if row["kind"] == "state-sync"]
    diff.equal(place, "state sync count", len(syncs), len(observed["state_sync"]))
    for row, got in zip(syncs, observed["state_sync"]):
        sync_place = [number, "state-sync"]
        diff.equal(sync_place, "id", row["event"]["id"], got["id"])
        diff.equal(sync_place, "logs", row["logs"], got.get("logs"))
        diff.equal(sync_place, "state_receiver_last_state_id", row["state_receiver_last_state_id"],
                   got.get("state_receiver_last_state_id"))
    for address, nonce in expected.get("system_nonce_after", {}).items():
        diff.equal(place, f"system nonce {address}", nonce, observed.get("system_nonce_after", {}).get(address))
    compare_accounts(diff, place, "state_after_block", expected["state_after_block"], observed["state_after_block"])
    if expected.get("state_root_unchanged"):
        diff.equal(place, "state_root equals the parent root", parent["state_root"] if parent else None,
                   observed["state_root"])


def compare(expected, observation):
    """Return the comparison report; raise FormatError for a bad observation."""
    check_observation(observation)
    diff = Diff()
    whole = [None, None]
    diff.equal(whole, "family", expected["family"], observation["family"])
    diff.equal(whole, "chain_id", expected["chain_id"], observation["chain_id"])
    diff.equal(whole, "block numbers", [b["number"] for b in expected["blocks"]],
               [b["number"] for b in observation["blocks"]])
    observed = {block["number"]: block for block in observation["blocks"]}
    for block in expected["blocks"]:
        if block["number"] in observed:
            parent = observed.get(block["number"] - 1)
            compare_block(diff, block, observed[block["number"]], parent)
    decided = {}
    for row in observation["decisions"]:
        decided.setdefault(row.get("block"), []).append(row.get("kind"))
    for block in expected["blocks"]:
        diff.equal([block["number"], None], "decisions", ["IMPORT"], decided.get(block["number"]))
    # A decision of a block the corpus does not have (for example the genesis) is a difference too.
    diff.equal(whole, "decisions of other blocks", [],
               sorted(set(decided) - {block["number"] for block in expected["blocks"]}))
    diff.equal(whole, "reimport.new_observations", 0, observation["reimport"]["new_observations"])
    diff.equal(whole, "reimport.new_decisions", 0, observation["reimport"]["new_decisions"])
    cases = []
    for case in expected["cases"]:
        if not case["applicable"]:
            cases.append({"case": case["case"], "verdict": "NOT_APPLICABLE", "reason": case["reason"]})
            continue
        places = [list(place) for place in case["where"]]
        blocks = {place[0] for place in places}
        # A whole-observation or block-level difference fails every case in that block.
        rows = [row for row in diff.rows if row["place"] == whole or row["place"] in places or
                (row["place"][1] is None and row["place"][0] in blocks)]
        cases.append({"case": case["case"], "verdict": "FAIL" if rows else "PASS", "differences": len(rows)})
    result = "PASS" if not diff.rows and all(c["verdict"] != "FAIL" for c in cases) else "FAIL"
    return {"schema_version": 1, "kind": "native-corpus-comparison", "family": expected["family"],
            "result": result, "cases": cases, "differences": diff.rows}


def main(argv):
    args = list(argv)
    report_path = None
    if "--report" in args:
        at = args.index("--report")
        if at + 1 >= len(args):
            print(__doc__, file=sys.stderr)
            return 2
        report_path = args[at + 1]
        del args[at:at + 2]
    if len(args) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    with open(args[0]) as handle:
        expected = json.load(handle)
    try:
        with open(args[1]) as handle:
            observation = json.load(handle)
        report = compare(expected, observation)
    except (FormatError, ValueError, OSError) as error:
        report = {"schema_version": 1, "kind": "native-corpus-comparison", "family": expected.get("family"),
                  "result": "FAIL", "format_error": str(error), "cases": [], "differences": []}
    text = json.dumps(report, indent=1, sort_keys=True) + "\n"
    if report_path:
        with open(report_path, "x") as handle:
            handle.write(text)
    sys.stdout.write(text)
    return 0 if report["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
