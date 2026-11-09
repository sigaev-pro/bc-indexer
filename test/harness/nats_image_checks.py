#!/usr/bin/env python3
"""Validate supplied image-fence observations; this does not prove native execution."""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import re
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import closed, decode_json, pinned_json, require

TOPOLOGIES = ["standalone", "R3"]
READ_SUBJECTS = ["$JS.API.STREAM.INFO.FENCE", "$JS.API.STREAM.MSG.GET.FENCE"]
MISSING = ["authenticated native execution", "accepted image/tool/source-conformance bindings",
           "native configuration and readiness confirmation", "complete native account/group census",
           "recovered frontier brackets", "exact stored interval export", "owned resource cleanup"]


def number(value, label, minimum=0, maximum=2**63 - 1):
    require(type(value) is int and minimum <= value <= maximum, "Invalid " + label)
    return value


def validate_plan(plan):
    closed(plan, {"schema_version", "messages", "payload_bytes", "measured_seconds", "topologies"}, "Image-fence plan")
    require(type(plan["schema_version"]) is int and plan["schema_version"] == 1, "Unsupported plan version")
    number(plan["messages"], "message count", 2, 256)
    number(plan["payload_bytes"], "payload size", 16, 4096)
    number(plan["measured_seconds"], "shared measured budget", 60, 1800)
    require(plan["topologies"] == TOPOLOGIES, "Both declared topologies are required")
    return plan


def payload(logical_message, size):
    number(logical_message, "logical message", 0, 255)
    number(size, "payload size", 16, 4096)
    # These literals precede native execution and deliberately are not UTF-8 text.
    prefix = b"\x00\r\n\xff\x80" + logical_message.to_bytes(4, "big")
    return prefix + bytes((index * 37 + logical_message * 17) % 256 for index in range(size - len(prefix)))


def raw(value, maximum, label):
    require(type(value) is str and len(value) <= ((maximum + 2) // 3) * 4, "Oversized " + label)
    data = base64.b64decode(value, validate=True)
    require(len(data) <= maximum and base64.b64encode(data).decode() == value, "Invalid " + label)
    return data


def member_id(name):
    alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
    return "".join(alphabet[byte % 62] for byte in hashlib.sha256(name.encode()).digest()[:8])


def inbox(role, connection):
    require(role in ("publisher", "manager", "reader"), "Unsupported inbox role")
    return f"_INBOX.IMAGE.{role}.C{number(connection, 'connection position', 0, 63):03d}"


def validate_census(plan, census):
    validate_plan(plan)
    closed(census, {"connection", "inbox", "closed_order", "attempts"}, "Publication census")
    require(census["connection"] == "publisher-1", "Undeclared publisher connection")
    require(census["inbox"] in (inbox("publisher", 2), inbox("publisher", 4)), "Undeclared publisher inbox")
    attempts = census["attempts"]
    logical_messages = [0, 1, 1, *range(2, plan["messages"])]
    require(isinstance(attempts, list) and len(attempts) == len(logical_messages), "Incomplete or extra publication attempts")
    fields = {"ordinal", "logical_message", "connection", "frame_ordinal", "request", "response", "forwarded_order",
              "response_order", "application_order", "application_result", "server_outcome", "retry_of"}
    stored, last_order = [], 0
    for ordinal, (attempt, logical) in enumerate(zip(attempts, logical_messages), 1):
        closed(attempt, fields, "Publication attempt")
        require(number(attempt["ordinal"], "attempt ordinal", 1) == ordinal and
                number(attempt["logical_message"], "logical message") == logical and
                attempt["connection"] == census["connection"] and
                number(attempt["frame_ordinal"], "connection frame ordinal", 1) == ordinal + 3,
                "Publication identity or frame order changed")
        forwarded = number(attempt["forwarded_order"], "forwarded event", 1)
        response_order = number(attempt["response_order"], "server response event", 1)
        application_order = number(attempt["application_order"], "application event", 1)
        require(last_order < forwarded < response_order < application_order, "Response was not observed before application outcome")
        last_order = application_order
        require(attempt["application_result"] == ("TIMEOUT" if ordinal == 2 else "ACK") and
                attempt["server_outcome"] == "ACKNOWLEDGED", "Unresolved or incorrect acknowledgment outcome")
        if ordinal == 3:
            require(type(attempt["retry_of"]) is int and attempt["retry_of"] == 2, "Exact retry lacks its original attempt")
        else:
            require(attempt["retry_of"] is None, "Unexpected retry")
        data = payload(logical, plan["payload_bytes"])
        reply = f"{census['inbox']}.P{ordinal}".encode()
        request = b"PUB fence.input " + reply + b" " + str(len(data)).encode() + b"\r\n" + data + b"\r\n"
        require(raw(attempt["request"], 8192, "publication frame") == request, "Publication bytes or reply correlation changed")
        response = raw(attempt["response"], 8192, "acknowledgment frame")
        require(b"\r\n" in response, "Missing MSG response header")
        header, body = response.split(b"\r\n", 1)
        header_fields = header.split(b" ")
        require(len(header_fields) == 4 and header_fields[:3] == [b"MSG", reply, b"1"] and
                re.fullmatch(rb"(?:0|[1-9][0-9]{0,3})", header_fields[3]) and
                len(body) == int(header_fields[3]) + 2 and body.endswith(b"\r\n"), "Uncorrelated or malformed native response")
        ack = decode_json(body[:-2])
        closed(ack, {"stream", "seq"}, "Native publication acknowledgment")
        require(ack["stream"] == "FENCE" and number(ack["seq"], "stored sequence", 1) == ordinal,
                "Native acknowledgment belongs to another publication")
        stored.append({"sequence": ordinal, "subject": "fence.input", "headers": b"", "data": data})
    require(number(census["closed_order"], "publisher close event", 1) > last_order, "Publisher closed before its census completed")
    return stored


def expected_config(topology, generation, index):
    require(topology in TOPOLOGIES and type(generation) is int and generation in (0, 1), "Unsupported process generation")
    count = 1 if topology == "standalone" else 3
    number(index, "server position", 0, count - 1)
    def user(name, subjects):
        return {"user": name, "password": "fixture-only", "permissions": {
            "publish": {"allow": list(subjects)}, "subscribe": {"allow": [f"_INBOX.IMAGE.{name}.>"]}, "allow_responses": False}}
    reader = user("reader", READ_SUBJECTS)
    app_users = [reader] if generation == 1 else [user("publisher", ["fence.input"]),
        user("manager", ["$JS.API.STREAM.CREATE.FENCE", "$JS.API.STREAM.UPDATE.FENCE", *READ_SUBJECTS]), reader]
    name = f"image-S-{index + 1}"
    result = {"server_name": name, "listen": f"127.0.0.1:{19001 + index}", "http": f"127.0.0.1:{19101 + index}",
              "max_payload": 8192, "max_connections": 16, "system_account": "SYS",
              "jetstream": {"store_dir": f"/proof/stores/{name}", "max_memory_store": 16777216, "max_file_store": 67108864},
              "accounts": {"SYS": {"users": [{"user": "system", "password": "fixture-only", "permissions": {
                  "publish": {"deny": [">"]}, "subscribe": {"deny": [">"]}, "allow_responses": False}}]},
                  "APP": {"jetstream": "enabled", "users": app_users}}}
    if topology == "R3":
        result["cluster"] = {"name": "IMAGE-FENCE", "listen": f"127.0.0.1:{19201 + index}",
                             "routes": [f"nats-route://127.0.0.1:{19201 + peer}" for peer in range(3) if peer != index]}
    return result


def validate_processes(topology, processes, boundaries, census):
    require(topology in TOPOLOGIES, "Unsupported topology")
    phases = ("publishers_closed", "seal_ack", "restart_started", "fence_checked", "export_complete", "finish")
    closed(boundaries, set(phases), "Process phase boundaries")
    orders = [number(boundaries[key], key, 1) for key in phases]
    require(all(left < right for left, right in zip(orders, orders[1:])) and
            boundaries["publishers_closed"] == census["closed_order"], "Invalid publication/closure/fence ordering")
    count = 1 if topology == "standalone" else 3
    require(isinstance(processes, list) and len(processes) == count * 2, "Incomplete native process generations")
    fields = {"name", "generation", "pid", "start_ticks", "info_id", "member_id", "argv", "config_bytes", "store",
              "store_device", "store_inode", "started_order", "exited_order", "exit_code", "wait_error", "forced_kill"}
    identities, info_ids, stores, event_orders = set(), set(), {}, set()
    generation_pids = [set(), set()]
    old_start_ticks = []
    for position, process in enumerate(processes):
        closed(process, fields, "Native process")
        generation, index = divmod(position, count)
        name = f"image-S-{index + 1}"
        require(process["name"] == name and type(process["generation"]) is int and process["generation"] == generation and
                process["member_id"] == member_id(name), "Native process/name/member binding changed")
        identity = (number(process["pid"], "native PID", 1), number(process["start_ticks"], "native start time", 1))
        require(identity not in identities, "Repeated native process instance")
        require(identity[0] not in generation_pids[generation], "Overlapping native processes share one PID")
        generation_pids[generation].add(identity[0])
        identities.add(identity)
        if generation == 0:
            old_start_ticks.append(identity[1])
        else:
            require(identity[1] >= max(old_start_ticks), "Recovered process predates an old generation")
        info_id = process["info_id"]
        require(type(info_id) is str and re.fullmatch(r"N[A-Z2-7]{55}", info_id) and info_id not in info_ids,
                "Missing, stale or duplicate native INFO identity")
        info_ids.add(info_id)
        require(process["argv"] == ["/usr/local/bin/nats-server", "--config", f"/proof/config/{generation}-{name}.conf"],
                "Native executable or configuration argv changed")
        config = decode_json(raw(process["config_bytes"], 16384, "native configuration"))
        require(json.dumps(config, sort_keys=True) == json.dumps(expected_config(topology, generation, index), sort_keys=True),
                "Native configuration or restart permissions changed")
        require(process["store"] == f"/proof/stores/{name}", "Native store path changed")
        store = (number(process["store_device"], "store device", 0), number(process["store_inode"], "store inode", 1))
        if generation == 0:
            require(store not in stores.values(), "Distinct native stores share one directory")
            stores[name] = store
        else:
            require(store == stores[name], "Restart substituted a different store directory")
        started = number(process["started_order"], "process start event", 1)
        exited = number(process["exited_order"], "process wait event", 1)
        require(started < exited and started not in event_orders and exited not in event_orders,
                "Missing or duplicate native process event")
        event_orders.update((started, exited))
        require(type(process["exit_code"]) is int and process["exit_code"] == 0 and
                process["wait_error"] is None and process["forced_kill"] is False, "Native child did not exit cleanly")
        if generation == 0:
            require(started < census["attempts"][0]["forwarded_order"] and
                    boundaries["seal_ack"] < exited < boundaries["restart_started"], "Old native process survived restart boundary")
        else:
            require(boundaries["restart_started"] < started < boundaries["fence_checked"] and
                    boundaries["export_complete"] < exited < boundaries["finish"], "Recovered process lifetime is incomplete")


def validate_observations(value):
    closed(value, {"schema_version", "kind", "plan", "topology", "census", "boundaries", "processes"}, "Image-fence observations")
    require(type(value["schema_version"]) is int and value["schema_version"] == 1 and
            value["kind"] == "nats-image-supplied-observations", "Unsupported observation document")
    stored = validate_census(value["plan"], value["census"])
    count = 1 if value["topology"] == "standalone" else 3
    require(value["census"]["inbox"] == inbox("publisher", count + 1), "Publisher inbox does not match its connection")
    validate_processes(value["topology"], value["processes"], value["boundaries"], value["census"])
    return {"schema_version": 1, "kind": "nats-image-supplied-observation-check", "observation_valid": True,
            "topology": value["topology"], "expected_stored_messages": len(stored),
            "runtime_admitted": False, "missing_evidence": MISSING}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("--sha256", required=True)
    args = parser.parse_args()
    try:
        require(args.input.is_absolute(), "Absolute observation input required")
        reference, value = pinned_json(args.input, args.sha256, 8 * 1024 * 1024, time.monotonic() + 30)
        result = validate_observations(value)
        result["input"] = reference
        print(json.dumps(result, sort_keys=True))
        return 0
    except (ValueError, OSError, KeyError, TypeError) as error:
        print(json.dumps({"observation_valid": False, "runtime_admitted": False, "error": str(error)}, sort_keys=True))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
