"""Synthetic native JSON controls; these fixtures make no runtime claim."""

import copy
import json
import unittest
from urllib.parse import quote

import nats_image_collection as collection
from nats_image_checks import member_id, validate_census
from test_nats_image_checks import encoded, sample


def document(topology="R3"):
    basic = sample(topology)
    basic["boundaries"].update(fence_checked=400, export_complete=500, finish=900)
    for process in basic["processes"]:
        if process["generation"] == 1:
            process["exited_order"] += 700
    count = 1 if topology == "standalone" else 3
    config = {"name": "FENCE", "subjects": ["fence.input"], "retention": "limits", "storage": "file",
              "num_replicas": count, "max_msgs": 20, "max_bytes": 8388608, "max_age": 0, "discard": "new",
              "deny_delete": True, "deny_purge": True, "allow_rollup_hdrs": False, "sealed": False}
    empty = {"messages": 0, "bytes": 0, "first_seq": 0, "last_seq": 0, "consumer_count": 0,
             "first_ts": "0001-01-01T00:00:00Z", "last_ts": "0001-01-01T00:00:00Z"}
    state = {**empty, "messages": 4, "bytes": 228, "first_seq": 1, "last_seq": 4,
             "first_ts": "2026-11-09T00:00:01Z", "last_ts": "2026-11-09T00:00:04Z"}
    initial = {"created": "2026-11-09T00:00:00Z", "config": config, "state": empty}
    sealed = {**initial, "config": {**config, "sealed": True}, "state": state}
    stream_group = "S-R3F-original" if count == 3 else None
    voters = {p["name"]: member_id(p["name"]) for p in basic["processes"][count:]}

    def snapshot(start):
        order = start
        def observation(name, path, value, status=200):
            nonlocal order
            order += 1
            return {"node": name, "path": path, "status": status, "order": order,
                    "body": encoded(json.dumps(value).encode() if status == 200 else b"No Raft nodes registered")}
        nodes = {}
        for process in basic["processes"][count:]:
            name = process["name"]
            stream = {"name": "FENCE", **sealed}
            if count == 3: stream["stream_raft_group"] = stream_group
            jsz = {"server_id": process["info_id"], "total": 1, "streams": 1, "consumers": 0,
                   "account_details": [{"name": "APP", "id": "APP", "stream_detail": [stream]}]}
            if count == 3:
                jsz["meta_cluster"] = {"name": "IMAGE-FENCE", "cluster_size": 3, "quorum_needed": 2,
                                       "pending": 0, "pending_requests": 0, "pending_infos": 0}
            node = {"health": observation(name, "/healthz", {"status": "ok"}),
                    "jsz": observation(name, collection.JSZ, jsz), "raft_inventory": {}, "groups": {}}
            for account in collection.ACCOUNTS:
                group = "_meta_" if account == "SYS" else stream_group if account == "APP" else None
                value = {}
                if count == 3 and group:
                    row = {"id": voters[name], "state": "LEADER" if name == "image-S-1" else "FOLLOWER",
                           "size": 3, "quorum_needed": 2, "committed": 7, "applied": 7,
                           "leader": voters["image-S-1"], "ever_had_leader": True, "term": 2,
                           "pterm": 2, "pindex": 7, "system_account": True, "traffic_account": "SYS",
                           "ipq_proposal_len": 0, "ipq_entry_len": 0, "ipq_resp_len": 0, "ipq_apply_len": 0,
                           "wal": {"messages": 2, "first_seq": 6, "last_seq": 7},
                           "peers": {v: {"name": n, "known": True} for n, v in voters.items() if n != name}}
                    value = {account: {group: row}}
                path = "/raftz?acc=" + quote(account, safe="")
                node["raft_inventory"][account] = observation(name, path, value, 404 if count == 1 else 200)
                if count == 3 and group:
                    node["groups"][account + "/" + group] = observation(name, path + "&group=" + quote(group, safe=""), value)
            nodes[name] = node
        return {"started_order": start, "finished_order": order + 1, "nodes": nodes}

    exports = []
    for message in validate_census(basic["plan"], basic["census"]):
        seq = message["sequence"]
        response = {"type": "io.nats.jetstream.api.v1.stream_msg_get_response", "message": {
            "subject": message["subject"], "seq": seq, "data": encoded(message["data"]),
            "time": f"2026-11-09T00:00:0{seq}Z"}}
        exports.append({"sequence": seq, "order": 400 + seq, "response": encoded(json.dumps(response).encode())})
    return dict(basic=basic, before=snapshot(100), after=snapshot(600), initial=initial, sealed=sealed,
                stream_group=stream_group, exports=exports)


def change_body(observation, mutate):
    import base64
    data = json.loads(base64.b64decode(observation["body"]))
    mutate(data)
    observation["body"] = encoded(json.dumps(data).encode())


def change_export(observation, mutate):
    wrapper = {"body": observation["response"]}
    change_body(wrapper, mutate)
    observation["response"] = wrapper["body"]


class CollectionTests(unittest.TestCase):
    def test_standalone_absence_uses_exact_pinned_native_response(self):
        bodies = (b"No Raft nodes registered",)
        for body in bodies:
            value = document("standalone")
            for bracket in (value["before"], value["after"]):
                for node in bracket["nodes"].values():
                    for row in node["raft_inventory"].values(): row["body"] = encoded(body)
            self.assertEqual(collection.validate_collections(**value)["upper_sequence"], 4)
        for body in (b"", b"404 page not found\n", bodies[0] + b"\n", b"No Raft nodes",
                     b"No Raft nodes returned, check supplied filters"):
            row = {"node": "image-S-1", "path": "/raftz?acc=APP", "status": 404, "body": encoded(body), "order": 1}
            with self.subTest(body=body), self.assertRaises(ValueError):
                collection.monitor(row, row["node"], row["path"], set(), absent=True)
        for body in bodies:
            row = {"node": "image-S-1", "path": "/raftz?acc=APP", "status": 404, "body": encoded(body), "order": 1}
            with self.assertRaises(ValueError): collection.monitor(row, row["node"], row["path"], set())

    def test_both_topologies_and_every_sequence_are_required(self):
        for topology in ("standalone", "R3"):
            value = document(topology)
            result = collection.validate_collections(**value)
            self.assertEqual(result["upper_sequence"], 4)
            for mutate in (lambda d: d["exports"].pop(),
                           lambda d: d["after"]["nodes"].pop("image-S-1"),
                           lambda d: d.update(stream_group="S-R3F-other"),
                           lambda d: d["exports"][0].update(order=501)):
                bad = copy.deepcopy(value)
                mutate(bad)
                with self.subTest(topology=topology, mutate=mutate), self.assertRaises(ValueError):
                    collection.validate_collections(**bad)

    def test_complete_account_inventory_rejects_omissions_and_extra_groups(self):
        for mutate in (
            lambda n: n["raft_inventory"].pop("$G"),
            lambda n: change_body(n["raft_inventory"]["APP"], lambda d: d["APP"].update({"extra": {}})),
            lambda n: change_body(n["raft_inventory"]["SYS"], lambda d: d.clear()),
            lambda n: change_body(n["jsz"], lambda d: d.update(total=2)),
            lambda n: change_body(n["jsz"], lambda d: d.update(server_id="foreign")),
            lambda n: change_body(n["jsz"], lambda d: d["account_details"][0].update(id="SYS")),
            lambda n: change_body(n["jsz"], lambda d: d["account_details"][0]["stream_detail"][0].update(consumer_detail=[{}])),
        ):
            value = document()
            mutate(value["after"]["nodes"]["image-S-2"])
            with self.subTest(mutate=mutate), self.assertRaises(ValueError): collection.validate_collections(**value)

    def test_raw_bytes_identity_health_and_raft_frontier_are_not_summary_booleans(self):
        for mutate in (
            lambda d: change_body(d["after"]["nodes"]["image-S-3"]["health"], lambda h: h.update(status="unavailable")),
            lambda d: change_body(d["after"]["nodes"]["image-S-3"]["groups"]["APP/S-R3F-original"],
                                 lambda r: r["APP"]["S-R3F-original"].update(applied=6)),
            lambda d: change_body(d["after"]["nodes"]["image-S-3"]["jsz"],
                                 lambda j: j["account_details"][0]["stream_detail"][0]["state"].update(last_seq=5)),
            lambda d: change_export(d["exports"][0], lambda r: r["message"].update(data=encoded(b"wrong"))),
        ):
            value = document()
            mutate(value)
            with self.subTest(mutate=mutate), self.assertRaises(ValueError): collection.validate_collections(**value)

    def test_inventory_rows_cannot_disagree_with_the_explicit_group_samples(self):
        for field, replacement in (("applied", 6), ("traffic_account", "APP"), ("id", "foreign")):
            value = document()
            change_body(value["before"]["nodes"]["image-S-1"]["raft_inventory"]["APP"],
                        lambda d: d["APP"]["S-R3F-original"].update({field: replacement}))
            with self.subTest(field=field), self.assertRaises(ValueError): collection.validate_collections(**value)

    def test_unknown_or_nonordinary_stream_configuration_is_not_implicitly_covered(self):
        for field, replacement in (("future_write_mode", True), ("persist_mode", "memory"),
                                   ("compression", "s2"), ("placement", {"cluster": "foreign"})):
            value = document()
            value["initial"]["config"][field] = replacement
            value["sealed"]["config"][field] = replacement
            for snap in (value["before"], value["after"]):
                for node in snap["nodes"].values():
                    change_body(node["jsz"], lambda j: j["account_details"][0]["stream_detail"][0]["config"].update({field: replacement}))
            with self.subTest(field=field), self.assertRaises(ValueError): collection.validate_collections(**value)


if __name__ == "__main__": unittest.main()
