"""Check complete native inventories, stable frontiers, and exact exported bytes."""

from urllib.parse import quote
import json
import re

from artifacts import closed, decode_json, require
from nats_fence import validate_frontier
from nats_image_checks import member_id, number, raw, validate_census, validate_observations

ACCOUNTS = ("APP", "SYS", "$G")
JSZ = "/jsz?accounts=true&streams=true&consumers=true&direct-consumers=true&config=true&raft=true&offset=0&limit=16"
CONFIG_FIELDS = set("name description subjects retention max_consumers max_msgs max_bytes max_age max_msgs_per_subject max_msg_size discard storage num_replicas no_ack duplicate_window placement mirror sources compression first_seq subject_transform republish allow_direct mirror_direct discard_new_per_subject sealed deny_delete deny_purge allow_rollup_hdrs consumer_limits allow_msg_ttl subject_delete_marker_ttl allow_msg_counter allow_atomic allow_msg_schedules persist_mode allow_batched metadata".split())


def monitor(observation, node, path, orders, *, absent=False):
    closed(observation, {"node", "path", "status", "body", "order"}, "Native monitoring observation")
    require(observation["node"] == node and observation["path"] == path, "Native monitoring identity or query changed")
    order = number(observation["order"], "monitoring event", 1)
    require(order not in orders, "Duplicate monitoring event")
    orders.add(order)
    status = number(observation["status"], "HTTP status", 100, 599)
    data = raw(observation["body"], 1024 * 1024, "monitoring body")
    if absent and status == 404:
        # Pinned HandleRaftz emits these exact bytes when standalone has never
        # registered a Raft node. A filter miss or arbitrary 404 is not proof.
        require(path.startswith("/raftz?acc=") and data == b"No Raft nodes registered", "Unexpected absent-Raft response")
        return {}
    require(status == 200, "Native monitoring request failed")
    value = decode_json(data)
    require(type(value) is dict, "Native monitoring object missing")
    return value


def stream_config(plan, topology, config, sealed):
    require(type(config) is dict, "Missing native stream configuration")
    require(set(config) <= CONFIG_FIELDS, "Unknown native stream configuration field")
    require(config.get("compression", "none") == "none" and config.get("persist_mode", "default") == "default" and
            config.get("placement") is None, "Unsupported storage or placement configuration")
    expected = {"name": "FENCE", "subjects": ["fence.input"], "retention": "limits", "storage": "file",
                "num_replicas": 1 if topology == "standalone" else 3, "max_msgs": plan["messages"] + 17,
                "max_bytes": 8388608, "max_age": 0, "discard": "new", "deny_delete": True,
                "deny_purge": True, "allow_rollup_hdrs": False, "sealed": sealed}
    for key, value in expected.items():
        require(type(config.get(key)) is type(value) and config[key] == value, "Unsafe stream setting: " + key)
    for key in ("no_ack", "allow_direct", "mirror_direct", "allow_msg_ttl", "allow_msg_counter", "allow_atomic",
                "allow_batched", "allow_msg_schedules", "discard_new_per_subject"):
        require(config.get(key, False) is False, "Unsupported stream write path: " + key)
    for key in ("mirror", "sources", "subject_transform", "republish"):
        require(config.get(key) in (None, []), "Unsupported stream source or transform")
    for key in ("subject_delete_marker_ttl", "inactive_threshold"):
        require(number(config.get(key, 0), key) == 0, "Unsupported expiration or deletion")
    require(number(config.get("first_seq", 0), "configured first sequence") == 0, "Non-default initial sequence")


def state(value, messages):
    require(type(value) is dict, "Missing native stream state")
    for key, expected in (("messages", messages), ("last_seq", messages), ("consumer_count", 0), ("num_deleted", 0)):
        require(number(value.get(key, 0) if key == "num_deleted" else value.get(key), key) == expected,
                "Native stream interval or consumer count changed")
    require(number(value.get("first_seq"), "first sequence") == (1 if messages else 0), "Native interval has a gap")
    require(value.get("deleted") in (None, []) and value.get("lost") is None, "Native messages were lost or deleted")
    byte_count = number(value.get("bytes"), "stored byte count", 0, 8388608)
    require((messages == 0 and byte_count == 0) or (messages > 0 and byte_count > 0), "Invalid stored byte count")
    for key in ("first_ts", "last_ts"):
        require(type(value.get(key)) is str and value[key], "Missing native message timestamp")
    return {key: value[key] for key in ("messages", "bytes", "first_seq", "last_seq", "first_ts", "last_ts", "consumer_count")}


def stream_identity(basic, initial, sealed):
    for info in (initial, sealed):
        require(type(info) is dict and "error" not in info and type(info.get("created")) is str and
                re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[^\s]+Z", info["created"]), "Missing native stream identity")
    stream_config(basic["plan"], basic["topology"], initial["config"], False)
    stream_config(basic["plan"], basic["topology"], sealed["config"], True)
    state(initial["state"], 0)
    state(sealed["state"], basic["plan"]["messages"] + 1)
    require(initial["created"] == sealed["created"], "Stream was recreated while sealing")
    allowed = {"sealed", "max_age", "discard", "deny_delete", "deny_purge", "allow_rollup_hdrs"}
    require({key: value for key, value in initial["config"].items() if key not in allowed} ==
            {key: value for key, value in sealed["config"].items() if key not in allowed}, "Unplanned seal configuration change")
    return {"created": initial["created"], "config": sealed["config"]}


def collection(basic, snapshot, identity, expected_group):
    closed(snapshot, {"started_order", "finished_order", "nodes"}, "Native collection")
    start = number(snapshot["started_order"], "collection start", 1)
    end = number(snapshot["finished_order"], "collection end", start + 1)
    count = 1 if basic["topology"] == "standalone" else 3
    recovered = basic["processes"][count:]
    nodes = snapshot["nodes"]
    require(type(nodes) is dict and set(nodes) == {p["name"] for p in recovered}, "Incomplete native server collection")
    expected_groups = {} if count == 1 else {"SYS/_meta_": ("SYS", "_meta_"), "APP/" + expected_group: ("APP", expected_group)}
    brackets = {key: {} for key in expected_groups}
    inventories_by_group = {key: {} for key in expected_groups}
    orders, common_state = set(), None
    for process in recovered:
        name = process["name"]
        observations = nodes[name]
        closed(observations, {"health", "jsz", "raft_inventory", "groups"}, "Native node collection")
        health = monitor(observations["health"], name, "/healthz", orders)
        require(health.get("status") == "ok" and not health.get("error") and not health.get("errors"), "Native recovery is unhealthy")
        info = monitor(observations["jsz"], name, JSZ, orders)
        require(info.get("server_id") == process["info_id"] and info.get("disabled", False) is False and
                number(info.get("total"), "total JetStream accounts") == 1 and
                number(info.get("streams"), "local streams") == 1 and number(info.get("consumers"), "local consumers") == 0,
                "Incomplete, stale or unexpected JetStream inventory")
        accounts = info.get("account_details")
        require(type(accounts) is list and len(accounts) == 1 and accounts[0].get("name") == "APP" and accounts[0].get("id") == "APP",
                "JetStream account inventory changed or is truncated")
        streams = accounts[0].get("stream_detail")
        require(type(streams) is list and len(streams) == 1, "Incomplete stream inventory")
        stream = streams[0]
        require(stream.get("name") == "FENCE" and stream.get("created") == identity["created"] and
                json.dumps(stream.get("config"), sort_keys=True) == json.dumps(identity["config"], sort_keys=True),
                "Recovered stream identity or full configuration changed")
        for key in ("consumer_detail", "direct_consumer_detail", "consumer_raft_groups", "sources"):
            require(stream.get(key) in (None, []), "Unexpected consumer or source group")
        require(stream.get("mirror") is None, "Unexpected mirrored stream")
        current_state = state(stream.get("state"), basic["plan"]["messages"] + 1)
        if common_state is None: common_state = current_state
        require(current_state == common_state, "Replica stream intervals differ")
        require(stream.get("stream_raft_group") == expected_group if count == 3 else not stream.get("stream_raft_group"),
                "Native stream group identity changed")
        if count == 3:
            meta = info.get("meta_cluster")
            require(type(meta) is dict and meta.get("name") == "IMAGE-FENCE" and
                    number(meta.get("cluster_size"), "metadata size") == 3 and number(meta.get("quorum_needed"), "metadata quorum") == 2 and
                    meta.get("rescue", False) is False and all(number(meta.get(k), k) == 0 for k in ("pending", "pending_requests", "pending_infos")),
                    "Metadata membership or pending work changed")
        else:
            require(info.get("meta_cluster") is None, "Unexpected standalone metadata group")
        inventories = observations["raft_inventory"]
        closed(inventories, set(ACCOUNTS), "Complete native account enumeration")
        for account in ACCOUNTS:
            inventory = monitor(inventories[account], name, "/raftz?acc=" + quote(account, safe=""), orders, absent=count == 1)
            groups = {g for a, g in expected_groups.values() if a == account}
            if groups:
                closed(inventory, {account}, "Native account group inventory")
                closed(inventory[account], groups, "Complete native group inventory")
                for group in groups:
                    inventories_by_group[account + "/" + group][name] = {account: {group: inventory[account][group]}}
            else:
                require(inventory == {}, "Unexpected native group outside the declared topology")
        closed(observations["groups"], set(expected_groups), "Explicit group collection")
        for key, (account, group) in expected_groups.items():
            path = "/raftz?acc=" + quote(account, safe="") + "&group=" + quote(group, safe="")
            brackets[key][name] = monitor(observations["groups"][key], name, path, orders)
    require(orders and min(orders) > start and max(orders) < end, "Monitoring requests escape their collection bracket")
    summaries = {}
    for key, (account, group) in expected_groups.items():
        binding = {"account": account, "group": group, "traffic_account": "SYS", "system_account": True,
                   "voters": {p["name"]: member_id(p["name"]) for p in recovered}}
        summaries[key] = {"binding": binding, "rows": brackets[key]}
        validate_frontier(binding, inventories_by_group[key], brackets[key])
        validate_frontier(binding, brackets[key], brackets[key])
    return summaries, common_state, orders


def validate_collections(basic, before, after, initial, sealed, stream_group, exports):
    validate_observations(basic)
    if basic["topology"] == "R3":
        require(type(stream_group) is str and re.fullmatch(r"S-R3F-[A-Za-z0-9_-]{1,64}", stream_group), "Invalid frozen native stream group")
    else:
        require(stream_group is None, "Standalone fixture has a Raft stream group")
    identity = stream_identity(basic, initial, sealed)
    left, left_state, left_orders = collection(basic, before, identity, stream_group)
    right, right_state, right_orders = collection(basic, after, identity, stream_group)
    require(left_state == right_state and not left_orders.intersection(right_orders), "Collection interval changed or was reused")
    for key in left:
        validate_frontier(left[key]["binding"], left[key]["rows"], right[key]["rows"])
    boundaries = basic["boundaries"]
    require(max(p["started_order"] for p in basic["processes"] if p["generation"] == 1) < before["started_order"] <
            before["finished_order"] < boundaries["fence_checked"] < boundaries["export_complete"] < after["started_order"] <
            after["finished_order"] < min(p["exited_order"] for p in basic["processes"] if p["generation"] == 1), "Fence/export bracket order changed")
    expected = validate_census(basic["plan"], basic["census"])
    require(type(exports) is list and len(exports) == len(expected), "Incomplete or extra native sequence export")
    last_order = boundaries["fence_checked"]
    for row, message in zip(exports, expected):
        closed(row, {"sequence", "order", "response"}, "Native export")
        require(number(row["sequence"], "export sequence", 1) == message["sequence"], "Native export sequence order changed")
        order = number(row["order"], "export response order", last_order + 1, boundaries["export_complete"] - 1)
        last_order = order
        response = decode_json(raw(row["response"], 16384, "native message-get response"))
        closed(response, {"type", "message"}, "Native message-get response")
        require(response["type"] == "io.nats.jetstream.api.v1.stream_msg_get_response", "Wrong native export API")
        stored = response["message"]
        require(type(stored) is dict and set(stored) in ({"subject", "seq", "data", "time"}, {"subject", "seq", "data", "time", "hdrs"}),
                "Invalid native stored-message fields")
        require(stored["subject"] == message["subject"] and number(stored["seq"], "stored sequence", 1) == message["sequence"] and
                raw(stored["data"], 4096, "stored payload") == message["data"] and raw(stored.get("hdrs", ""), 1, "stored headers") == b"",
                "Native export differs from the exhaustive publication census")
        require(type(stored["time"]) is str and stored["time"], "Missing stored-message time")
        if message["sequence"] == 1: require(stored["time"] == left_state["first_ts"], "First message time changed")
        if message["sequence"] == len(expected): require(stored["time"] == left_state["last_ts"], "Last message time changed")
    return {"lower_sequence": 0, "upper_sequence": len(expected), "groups": sorted(left), "stored_messages": len(expected)}
