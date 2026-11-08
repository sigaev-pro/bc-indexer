"""Join retained native API bytes, process identities, census, and collections."""

import json
import os
from pathlib import PurePosixPath
import re
import stat

from artifacts import artifact, check_deadline, closed, decode_json, read_bytes, require
from nats_image_checks import raw, number
from nats_image_collection import JSZ, monitor, state, stream_config, validate_collections


def regular_files(directory, maximum, deadline=None):
    """List a bounded owned evidence tree without following links."""
    check_deadline(deadline)
    require(stat.S_ISDIR(os.lstat(directory).st_mode), "Evidence directory is not a regular directory")
    pending, files, entries = [directory], {}, 0
    while pending:
        parent = pending.pop()
        with os.scandir(parent) as children:
            for child in children:
                check_deadline(deadline)
                entries += 1
                require(entries <= maximum * 2 and not child.is_symlink(), "Evidence tree bound or symlink violation")
                path = parent / child.name
                if child.is_dir(follow_symlinks=False):
                    pending.append(path)
                else:
                    require(child.is_file(follow_symlinks=False) and len(files) < maximum, "Special or excessive evidence files")
                    files[path.relative_to(directory).as_posix()] = path
    return files


def snapshots(value, directory, deadline):
    basic = value["basic"]
    count = 1 if basic["topology"] == "standalone" else 3
    rows = value["store_snapshots"]
    require(type(rows) is list and len(rows) == count, "Pre-restart snapshot inventory missing")
    prior = max(process["exited_order"] for process in basic["processes"][:count])
    for process, row in zip(basic["processes"][:count], rows):
        closed(row, {"name", "started_order", "finished_order", "files"}, "Native store snapshot")
        require(row["name"] == process["name"], "Snapshot server identity changed")
        start = number(row["started_order"], "snapshot start", prior + 1)
        prior = number(row["finished_order"], "snapshot finish", start + 1)
        require(prior < basic["boundaries"]["restart_started"], "Store snapshot was not captured before restart")
        files = row["files"]
        require(type(files) is list and 0 < len(files) <= 8192, "Empty or oversized snapshot file inventory")
        actual = regular_files(directory / "before-stores" / row["name"], 8192, deadline)
        expected, total = set(), 0
        for entry in files:
            closed(entry, {"path", "sha256", "size_bytes"}, "Snapshot file")
            path = entry["path"]
            require(type(path) is str and 0 < len(path) <= 1024 and "\\" not in path, "Invalid snapshot path")
            relative = PurePosixPath(path)
            require(not relative.is_absolute() and ".." not in relative.parts and relative.as_posix() == path and path != "." and
                    path not in expected and path in actual, "Missing, duplicate or unsafe snapshot path")
            expected.add(path)
            total += number(entry["size_bytes"], "snapshot file size", 0, 64 * 1024 * 1024)
            require(total <= 128 * 1024 * 1024, "Snapshot total byte limit exceeded")
            artifact({**entry, "path": str(actual[path])}, 64 * 1024 * 1024, deadline=deadline)
        require(set(actual) == expected, "Extra or missing snapshot files")


def config_join(left, right, plan, topology, sealed):
    stream_config(plan, topology, left, sealed)
    stream_config(plan, topology, right, sealed)
    require(json.dumps(left, sort_keys=True) == json.dumps(right, sort_keys=True), "Native configuration join changed bytes or types")


def handshake(initial, connected):
    require(type(initial) is dict and type(connected) is dict and "connect_info" not in initial and
            connected.get("connect_info") is True and connected.get("remote_account") == "APP" and
            connected.get("acc_is_sys", False) is False, "Native CONNECT account binding changed")
    for key in ("server_id", "server_name", "version", "git_commit", "go", "host", "client_ip", "xkey"):
        require(type(initial.get(key)) is str and initial[key] and initial[key] == connected.get(key), "Native INFO string identity changed")
    for key in ("proto", "port", "max_payload", "client_id", "api_lvl"):
        require(number(initial.get(key), "initial INFO " + key, 1) == number(connected.get(key), "CONNECT INFO " + key, 1),
                "Native INFO numeric identity changed")
    for key in ("headers", "auth_required", "jetstream"):
        require(initial.get(key) is True and connected.get(key) is True, "Native INFO capability changed")


def rpc(row, index, name, subject, request_body):
    closed(row, {"name", "request", "response", "body", "order"}, "Native API exchange")
    require(row["name"] == name, "Native API operation changed")
    request = raw(row["request"], 16384, "API request")
    prefix, separator, rest = request.partition(b"\r\n")
    reply = f"_INBOX.IMAGE.R{index}".encode()
    fields = prefix.split(b" ")
    require(separator and fields[:3] == [b"PUB", subject.encode(), reply] and len(fields) == 4 and
            fields[3] == str(len(rest) - 2).encode() and rest.endswith(b"\r\n"), "Native API request framing changed")
    body = decode_json(rest[:-2])
    require(json.dumps(body, sort_keys=True) == json.dumps(request_body, sort_keys=True), "Native API request payload changed")
    response = raw(row["response"], 32768, "API response")
    payload = raw(row["body"], 16384, "API response body")
    require(response == b"MSG " + reply + b" 1 " + str(len(payload)).encode() + b"\r\n" + payload + b"\r\n",
            "Native API response correlation or body changed")
    value = decode_json(payload)
    require(type(value) is dict and "error" not in value, "Native API rejected the request")
    number(row["order"], "API response order", 1)
    return value


def validate_report(value, directory, deadline=None):
    closed(value, {"schema_version", "kind", "status", "error", "basic", "rpc", "initial_inventory", "initial", "sealed",
                   "stream_group", "before", "after", "exports", "restart_publication_denial", "cleanup_complete", "store_snapshots"}, "Native image report")
    require(type(value["schema_version"]) is int and value["schema_version"] == 1 and
            value["kind"] == "nats-unchanged-image-native-observations" and value["status"] == "PASS" and
            value["error"] == "" and value["cleanup_complete"] is True, "Native image collection failed")
    basic = value["basic"]
    result = validate_collections(**{key: value[key] for key in ("basic", "before", "after", "initial", "sealed", "stream_group", "exports")})
    count = 1 if basic["topology"] == "standalone" else 3
    calls = value["rpc"]
    require(type(calls) is list and len(calls) == 3 + len(value["exports"]), "Missing or extra native API exchange")
    for index, call in enumerate(calls, 1):
        for field in ("request", "response"):
            require(read_bytes(directory / "rpc" / f"{index:03d}.{field}", 32768, deadline) == raw(call[field], 32768, "API " + field),
                    "Retained API bytes disagree with the report")
    selected_http = list(value["initial_inventory"])
    for bracket in (value["before"], value["after"]):
        for node in bracket["nodes"].values():
            selected_http.extend([node["health"], node["jsz"], *node["raft_inventory"].values(), *node["groups"].values()])
    pending = {row["order"]: row for row in selected_http}
    require(len(pending) == len(selected_http), "Reused native HTTP observation")
    files = regular_files(directory / "http", 2000, deadline)
    seen = set()
    for name, file in sorted(files.items()):
        require(re.fullmatch(r"[0-9]{4}\.json", name), "Unexpected native monitoring file")
        row = decode_json(read_bytes(file, 2 * 1024 * 1024, deadline))
        closed(row, {"node", "path", "status", "body", "order", "error"}, "Retained native HTTP record")
        order = number(row["order"], "HTTP record order", 1)
        require(order not in seen, "Duplicate or contradictory native HTTP order")
        seen.add(order)
        if order in pending:
            require(row.get("error") == "" and {key: v for key, v in row.items() if key != "error"} == pending.pop(row["order"]),
                    "Retained monitoring bytes disagree with the selected sample")
    require(not pending, "Selected monitoring sample has no retained request record")
    attempts = regular_files(directory / "attempts", 514, deadline)
    expected_attempts = set()
    for attempt in basic["census"]["attempts"]:
        for field in ("request", "response"):
            name = f"{attempt['ordinal']:03d}.{field}"
            expected_attempts.add(name)
            require(name in attempts and read_bytes(attempts[name], 32768, deadline) == raw(attempt[field], 32768, "attempt " + field),
                    "Retained attempt bytes disagree with the census")
    require(set(attempts) == expected_attempts, "Extra or missing attempt files")
    snapshots(value, directory, deadline)
    for process in basic["processes"]:
        config = directory / "config" / f"{process['generation']}-{process['name']}.conf"
        require(read_bytes(config, 65536, deadline) == raw(process["config_bytes"], 65536, "process config"), "Retained process configuration changed")
        store = directory / "stores" / process["name"]
        identity = os.lstat(store)
        # Native old/new device and inode equality is checked inside the same
        # container namespace. A macOS bind mount need not expose host numbers.
        require(stat.S_ISDIR(identity.st_mode), "Retained original store directory changed")
        snapshot = directory / "before-stores" / process["name"]
        require(stat.S_ISDIR(os.lstat(snapshot).st_mode), "Immutable pre-restart store snapshot missing")
    plan = basic["plan"]
    config = {"name": "FENCE", "subjects": ["fence.input"], "retention": "limits", "storage": "file", "num_replicas": count,
              "max_msgs": plan["messages"] + 17, "max_bytes": 8388608, "max_age": 0, "discard": "new",
              "deny_delete": True, "deny_purge": True, "allow_rollup_hdrs": False, "sealed": False}
    require(rpc(calls[0], 1, "create", "$JS.API.STREAM.CREATE.FENCE", config) == value["initial"], "Initial API response substituted")
    require(rpc(calls[1], 2, "seal", "$JS.API.STREAM.UPDATE.FENCE", {**value["initial"]["config"], "sealed": True}) == value["sealed"],
            "Seal API response substituted")
    info = rpc(calls[2], 3, "fenced-info", "$JS.API.STREAM.INFO.FENCE", {})
    config_join(info.get("config"), value["sealed"]["config"], plan, basic["topology"], True)
    require(info.get("created") == value["sealed"]["created"] and
            state(info.get("state"), plan["messages"] + 1) == state(value["sealed"]["state"], plan["messages"] + 1),
            "Fenced interval or identity differs from the sealed stream")
    require(calls[0]["order"] < basic["census"]["attempts"][0]["forwarded_order"] and
            basic["boundaries"]["publishers_closed"] < calls[1]["order"] < basic["boundaries"]["seal_ack"] and
            basic["boundaries"]["fence_checked"] < calls[2]["order"] < value["exports"][0]["order"], "Native API stage order changed")
    for index, exported in enumerate(value["exports"], 1):
        call = calls[index + 2]
        rpc(call, index + 3, f"export-{index}", "$JS.API.STREAM.MSG.GET.FENCE", {"seq": index})
        require(exported["response"] == call["body"] and exported["order"] == call["order"], "Export body or order substituted")
    require(raw(value["restart_publication_denial"], 1024, "publish denial") ==
            b'-ERR \'Permissions Violation for Publish to "fence.input"\'\r\n', "Restart data publish was not rejected")
    inventories = value["initial_inventory"]
    require(type(inventories) is list and len(inventories) == count, "Pre-restart group identity inventory missing")
    orders = set()
    for process, observation in zip(basic["processes"][:count], inventories):
        jsz = monitor(observation, process["name"], JSZ, orders)
        require(jsz.get("server_id") == process["info_id"] and number(jsz.get("total"), "initial account total") == 1,
                "Initial server identity changed")
        accounts = jsz.get("account_details")
        require(type(accounts) is list and len(accounts) == 1 and accounts[0].get("id") == "APP", "Initial account inventory changed")
        streams = accounts[0].get("stream_detail")
        require(type(streams) is list and len(streams) == 1, "Initial stream inventory changed")
        stream = streams[0]
        config_join(stream.get("config"), value["initial"]["config"], plan, basic["topology"], False)
        require(stream.get("name") == "FENCE" and stream.get("created") == value["initial"]["created"] and
                stream.get("stream_raft_group") == value["stream_group"],
                "Original stream/group/configuration identity changed")
        state(stream.get("state"), 0)
        require(calls[0]["order"] < observation["order"] < basic["census"]["attempts"][0]["forwarded_order"], "Initial inventory occurred after publication")
    # The native socket audit is independent of summary row serialization.
    publishers = sorted((directory / "wire").glob("*-publisher.sent"))
    require(len(publishers) == 1, "Missing or extra publisher connection")
    sent = read_bytes(publishers[0], 2 * 1024 * 1024, deadline)
    expected_connect = json.dumps({"user": "publisher", "pass": "fixture-only", "verbose": False, "pedantic": True, "protocol": 1},
                                  sort_keys=True, separators=(",", ":")).encode()
    expected = b"CONNECT " + expected_connect + b"\r\nSUB _INBOX.IMAGE.> 1\r\nPING\r\n"
    expected += b"".join(raw(row["request"], 16384, "publish request") for row in basic["census"]["attempts"])
    require(sent == expected, "Raw publisher ingress disagrees with its complete census")
    received = read_bytes(publishers[0].with_suffix(".received"), 2 * 1024 * 1024, deadline)
    line, separator, remaining = received.partition(b"\r\n")
    require(separator and line.startswith(b"INFO "), "Raw publisher INFO missing")
    initial = decode_json(line[5:])
    require(initial.get("server_id") == basic["processes"][0]["info_id"], "Raw publisher server identity changed")
    pong, separator, remaining = remaining.partition(b"\r\n")
    require(separator and pong == b"PONG", "Raw native handshake PONG missing")
    connected, separator, remaining = remaining.partition(b"\r\n")
    require(separator and connected.startswith(b"INFO "), "Raw native CONNECT INFO missing")
    handshake(initial, decode_json(connected[5:]))
    expected = b"".join(raw(row["response"], 32768, "publish response") for row in basic["census"]["attempts"])
    require(remaining == expected, "Raw native replies disagree with the ACK census")
    return {**result, "ordinary_path": "PUB/FileStorage/Limits/default-persist/no-batching/no-consumers", "runtime_admitted": False}
