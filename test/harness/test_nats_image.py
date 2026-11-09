"""Portable byte joins and admission controls; no native service is started."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

import nats_image
from nats_image_proof import config_join, initial_rounds, validate_report
from test_nats_image_checks import encoded
from test_nats_image_collection import document, change_body


def native_document(directory, topology="R3"):
    source = document(topology)
    basic = source["basic"]
    count = 1 if topology == "standalone" else 3
    basic["boundaries"]["seal_ack"] = 45
    basic["boundaries"]["restart_started"] = 80
    for index, process in enumerate(basic["processes"][:count]): process["exited_order"] = 46 + index
    for process in basic["processes"][count:]: process["started_order"] += 30
    result = {"schema_version": 1, "kind": "nats-unchanged-image-native-observations", "status": "PASS", "error": "",
              **source, "rpc": [], "initial_inventory": [], "store_snapshots": [], "cleanup_complete": True,
              "restart_publication_denial": encoded(b'-ERR \'Permissions Violation for Publish to "fence.input"\'\r\n')}
    def call(name, subject, request_body, response, order):
        index = len(result["rpc"]) + 1
        role, connection = ("manager", count) if index <= 2 else ("reader", 2 * count + 3)
        reply = f"_INBOX.IMAGE.{role}.C{connection:03d}.R{index}".encode()
        body = json.dumps(request_body).encode()
        payload = json.dumps(response).encode()
        row = {"name": name, "request": encoded(b"PUB " + subject.encode() + b" " + reply + b" " + str(len(body)).encode() + b"\r\n" + body + b"\r\n"),
               "response": encoded(b"MSG " + reply + b" 1 " + str(len(payload)).encode() + b"\r\n" + payload + b"\r\n"),
               "body": encoded(payload), "order": order}
        result["rpc"].append(row)
        return row
    call("create", "$JS.API.STREAM.CREATE.FENCE", {k: v for k, v in source["initial"]["config"].items() if k != "metadata"}, source["initial"], 8)
    call("seal", "$JS.API.STREAM.UPDATE.FENCE", source["sealed"]["config"], source["sealed"], 41)
    call("fenced-info", "$JS.API.STREAM.INFO.FENCE", {}, source["sealed"], 410)
    import base64
    for exported in result["exports"]:
        index = exported["sequence"]
        row = call(f"export-{index}", "$JS.API.STREAM.MSG.GET.FENCE", {"seq": index},
                   json.loads(base64.b64decode(exported["response"])), 420 + index)
        exported.update(order=row["order"], response=row["body"])
    for index, process in enumerate(basic["processes"][:count]):
        row = copy.deepcopy(source["before"]["nodes"][process["name"]]["jsz"])
        row["order"] = 9 + index
        def mutate(value):
            value["server_id"] = process["info_id"]
            value.update(streams=1, consumers=0, messages=0, bytes=0)
            value["account_details"][0]["stream_detail"][0].update(source["initial"])
            value["account_details"][0]["stream_detail"][0]["config"] = {**source["initial"]["config"], "metadata": {"_nats.req.level": "0"}}
        change_body(row, mutate)
        result["initial_inventory"].append(row)
    wire = directory / "wire"
    wire.mkdir()
    sessions = [("reader", index, []) for index in range(count)]
    sessions += [("manager", 0, result["rpc"][:2]), ("publisher", 0, basic["census"]["attempts"])]
    sessions += [("reader", count + index, []) for index in range(count)]
    denied_reply = f"_INBOX.IMAGE.reader.C{2 * count + 2:03d}.DENIED".encode()
    sessions += [("reader", count, [{"request": encoded(b"PUB fence.input " + denied_reply + b" 9\r\nforbidden\r\n"),
                                     "response": result["restart_publication_denial"]}]), ("reader", count, result["rpc"][2:])]
    for index, (role, process_index, rows) in enumerate(sessions):
        process = basic["processes"][process_index]
        connect = json.dumps({"user": role, "pass": "fixture-only", "verbose": False, "pedantic": True, "protocol": 1},
                             sort_keys=True, separators=(",", ":")).encode()
        sent = b"CONNECT " + connect + f"\r\nSUB _INBOX.IMAGE.{role}.C{index:03d}.> 1\r\nPING\r\n".encode()
        info = {"server_id": process["info_id"], "server_name": process["name"], "version": "2.15.2", "git_commit": "5a815f1",
                "go": "go1.27.3", "host": "127.0.0.1", "port": 19001 + process_index % count, "proto": 1, "max_payload": 8192,
                "headers": True, "auth_required": True, "jetstream": True, "client_id": index + 7,
                "client_ip": "127.0.0.1", "api_lvl": 5, "xkey": "PUBLIC-KEY"}
        received = b"INFO " + json.dumps(info).encode() + b"\r\nPONG\r\nINFO "
        received += json.dumps({**info, "connect_info": True, "remote_account": "APP"}).encode() + b"\r\n"
        sent += b"".join(base64.b64decode(row["request"]) for row in rows)
        received += b"".join(base64.b64decode(row["response"]) for row in rows)
        (wire / f"{index:03d}-{role}.sent").write_bytes(sent)
        (wire / f"{index:03d}-{role}.received").write_bytes(received)
    (directory / "attempts").mkdir()
    for attempt in basic["census"]["attempts"]:
        for field in ("request", "response"):
            (directory / "attempts" / f"{attempt['ordinal']:03d}.{field}").write_bytes(base64.b64decode(attempt[field]))
    (directory / "rpc").mkdir()
    for index, row in enumerate(result["rpc"], 1):
        for key in ("request", "response"):
            (directory / "rpc" / f"{index:03d}.{key}").write_bytes(base64.b64decode(row[key]))
    (directory / "http").mkdir()
    observations = list(result["initial_inventory"])
    for bracket in (result["before"], result["after"]):
        for node in bracket["nodes"].values():
            observations.extend([node["health"], node["jsz"], *node["raft_inventory"].values(), *node["groups"].values()])
    for index, row in enumerate(observations):
        (directory / "http" / f"{index:04d}.json").write_text(json.dumps({**row, "error": ""}))
    for name in ("config", "stores", "before-stores"): (directory / name).mkdir()
    for process in basic["processes"]:
        (directory / "config" / f"{process['generation']}-{process['name']}.conf").write_bytes(base64.b64decode(process["config_bytes"]))
        store = directory / "stores" / process["name"]
        store.mkdir(exist_ok=True)
        (directory / "before-stores" / process["name"]).mkdir(exist_ok=True)
        (directory / "before-stores" / process["name"] / "native.blk").write_bytes(b"immutable native bytes")
        if process["generation"] == 0:
            index = len(result["store_snapshots"])
            result["store_snapshots"].append({"name": process["name"], "started_order": 49 + index * 2,
                "finished_order": 50 + index * 2, "files": [{"path": "native.blk", "sha256": hashlib.sha256(b"immutable native bytes").hexdigest(),
                                                         "size_bytes": len(b"immutable native bytes")}]})
        identity = store.stat()
        process.update(store_device=identity.st_dev, store_inode=identity.st_ino)
    return result


class NativeProofTests(unittest.TestCase):
    def test_actual_api_and_jsz_metadata_have_exact_distinct_shapes(self):
        fixture = json.loads((Path(__file__).parent / "fixtures/nats-image/metadata-observations.json").read_text())
        self.assertEqual(fixture["source_report_sha256"], "4f7cdee2155195540cb2be280e6ad2a8e02a6dba07512082c579944d9b11313a")
        config_join(fixture["jsz"], fixture["api"], fixture["plan"], fixture["topology"], True)
        for surface in ("api", "jsz"):
            for mutation in ("missing", "missing-key", "unknown", "wrong-required", "required-type", "wrong-version", "level-type", "nonmetadata", "nonmetadata-type"):
                value = copy.deepcopy(fixture)
                config = value[surface]
                if mutation == "missing": config.pop("metadata")
                elif mutation == "missing-key": config["metadata"].pop("_nats.req.level")
                elif mutation == "unknown": config["metadata"]["user-field"] = "unexpected"
                elif mutation == "wrong-required": config["metadata"]["_nats.req.level"] = "1"
                elif mutation == "required-type": config["metadata"]["_nats.req.level"] = 0
                elif mutation == "wrong-version": config["metadata"]["_nats.ver"] = "2.15.1"
                elif mutation == "level-type": config["metadata"]["_nats.level"] = 5
                elif mutation == "nonmetadata": config["duplicate_window"] += 1
                else: config["duplicate_window"] = float(config["duplicate_window"])
                with self.subTest(surface=surface, mutation=mutation), self.assertRaises(ValueError):
                    config_join(value["jsz"], value["api"], value["plan"], value["topology"], True)

    def test_every_connection_scope_and_selected_process_is_bound(self):
        for mutation in ("manager-ack", "reader-prefix", "reader-identity", "missing", "extra"):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root, "standalone")
                self.assertEqual(validate_report(value, root)["upper_sequence"], 4)
                if mutation == "manager-ack":
                    # Exact foreign frame from attempt 02's manager socket.
                    path = root / "wire/001-manager.received"
                    path.write_bytes(path.read_bytes() + b'MSG _INBOX.IMAGE.P1 1 26\r\n{"stream":"FENCE","seq":1}\r\n')
                elif mutation == "reader-prefix":
                    path = root / "wire/005-reader.sent"
                    path.write_bytes(path.read_bytes().replace(b"reader.C005.>", b"reader.C003.>"))
                elif mutation == "reader-identity":
                    path = root / "wire/005-reader.received"
                    path.write_bytes(path.read_bytes().replace(b'"server_name": "image-S-1"', b'"server_name": "foreign"'))
                elif mutation == "missing":
                    (root / "wire/000-reader.sent").unlink()
                else:
                    (root / "wire/099-manager.sent").write_bytes(b"unaccounted connection")
                with self.subTest(mutation=mutation), self.assertRaises(ValueError): validate_report(value, root)

    def test_matching_info_frames_must_match_the_selected_process_config(self):
        for update in ({"server_name": "foreign-node"}, {"port": 19003}, {"proto": 2}, {"max_payload": 4096},
                       {"proto": True}, {"port": 19001.0}, {"max_payload": "8192"}):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root, "standalone")
                path = root / "wire/002-publisher.received"
                initial, pong, connected, rest = path.read_bytes().split(b"\r\n", 3)
                first, second = json.loads(initial[5:]), json.loads(connected[5:])
                first.update(update)
                second.update(update)
                path.write_bytes(b"\r\n".join([b"INFO " + json.dumps(first).encode(), pong, b"INFO " + json.dumps(second).encode(), rest]))
                with self.subTest(update=update), self.assertRaises(ValueError): validate_report(value, root)
    def test_every_retained_attempt_http_record_and_snapshot_byte_is_bound(self):
        def duplicate_http(root):
            row = json.loads((root / "http/0000.json").read_text())
            row["body"] = encoded(b"{\"contradiction\":true}")
            (root / "http/9999.json").write_text(json.dumps(row))
        for mutate in (
            duplicate_http,
            lambda root: (root / "attempts/001.request").write_bytes(b"PUB unrelated 0\r\n\r\n"),
            lambda root: (root / "attempts/001.response").write_bytes(b"unrelated acknowledgment"),
            lambda root: (root / "before-stores/image-S-1/native.blk").write_bytes(b"changed"),
            lambda root: (root / "before-stores/image-S-1/native.blk").unlink(),
            lambda root: (root / "before-stores/image-S-1/extra.blk").write_bytes(b"extra"),
        ):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root)
                self.assertEqual(validate_report(value, root)["upper_sequence"], 4)
                mutate(root)
                with self.subTest(mutate=mutate), self.assertRaises(ValueError): validate_report(value, root)

    def test_native_config_joins_preserve_boolean_and_integer_types(self):
        import base64
        for site in ("fenced", "initial"):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root, "standalone")
                if site == "fenced":
                    row = value["rpc"][2]
                    payload = json.loads(base64.b64decode(row["body"]))
                    payload["config"].update(num_replicas=True, max_age=False)
                    data = json.dumps(payload).encode()
                    row["body"] = encoded(data)
                    row["response"] = encoded(b"MSG _INBOX.IMAGE.reader.C005.R3 1 " + str(len(data)).encode() + b"\r\n" + data + b"\r\n")
                    (root / "rpc/003.response").write_bytes(base64.b64decode(row["response"]))
                else:
                    change_body(value["initial_inventory"][0], lambda j: j["account_details"][0]["stream_detail"][0]["config"].update(num_replicas=True, max_age=False))
                    (root / "http/0000.json").write_text(json.dumps({**value["initial_inventory"][0], "error": ""}))
                with self.subTest(site=site), self.assertRaises(ValueError): validate_report(value, root)

    def test_raw_connect_info_cannot_change_account_identity_or_types(self):
        for update in ({"server_id": "OTHER"}, {"remote_account": "SYS"}, {"connect_info": 1},
                       {"acc_is_sys": True}, {"port": 19001.0}):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root, "standalone")
                path = root / "wire/002-publisher.received"
                initial, pong, connected, rest = path.read_bytes().split(b"\r\n", 3)
                info = json.loads(connected[5:])
                info.update(update)
                path.write_bytes(b"\r\n".join([initial, pong, b"INFO " + json.dumps(info).encode(), rest]))
                with self.subTest(update=update), self.assertRaises(ValueError): validate_report(value, root)

    def test_snapshot_inventory_requires_safe_unique_paths_and_pre_restart_capture(self):
        for mutate in (
            lambda row: row["files"].append(copy.deepcopy(row["files"][0])),
            lambda row: row["files"][0].update(path="../native.blk"),
            lambda row: row["files"][0].update(path="/native.blk"),
            lambda row: row["files"][0].update(size_bytes=True),
            lambda row: row.update(started_order=1),
            lambda row: row.update(finished_order=81),
        ):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root, "standalone")
                mutate(value["store_snapshots"][0])
                with self.subTest(mutate=mutate), self.assertRaises(ValueError): validate_report(value, root)
    def test_actual_byte_joins_are_required_on_both_topologies(self):
        for topology in ("standalone", "R3"):
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root, topology)
                self.assertEqual(validate_report(value, root)["upper_sequence"], 4)
                for mutate in (
                    lambda d: d["rpc"][2].update(body=encoded(b"{}")),
                    lambda d: d["initial_inventory"].pop(),
                    lambda d: d["rpc"][3].update(name="export-99"),
                    lambda d: d.update(restart_publication_denial=encoded(b"+OK\r\n")),
                    lambda d: d.update(cleanup_complete=False),
                ):
                    bad = copy.deepcopy(value)
                    mutate(bad)
                    with self.subTest(topology=topology, mutate=mutate), self.assertRaises(ValueError): validate_report(bad, root)
                position = 2 if topology == "standalone" else 4
                (root / "wire" / f"{position:03d}-publisher.sent").write_bytes(b"unaccounted publication")
                with self.assertRaisesRegex(ValueError, "requests or subscription"): validate_report(value, root)

    def test_every_retained_readiness_sample_is_validated_and_only_the_final_round_is_frozen(self):
        def pending(row, **changes):
            sample = copy.deepcopy(row)
            def mutate(value):
                value.update(streams=0)
                value["account_details"][0]["stream_detail"] = []
                value.update(changes.pop("body", {}))
            change_body(sample, mutate)
            sample.update(changes)
            return sample

        def write(root, order, row, error=""):
            (root / "http" / f"{9000 + order:04d}.json").write_text(json.dumps({**row, "order": order, "error": error}))

        def standalone(root, extra):
            # Move the selected sample to order 12 and retain earlier polls at 9-11.
            value = native_document(root, "standalone")
            selected = value["initial_inventory"][0]
            (root / "http/0000.json").unlink()
            selected["order"] = 12
            (root / "http/0000.json").write_text(json.dumps({**selected, "error": ""}))
            extra(root, selected)
            return value

        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            value = standalone(root, lambda r, s: [write(r, order, pending(s)) for order in (9, 10, 11)])
            self.assertEqual(validate_report(value, root)["upper_sequence"], 4)
        rejected = {
            "failed request": lambda r, s: write(r, 11, pending(s), "boom"),
            "server error": lambda r, s: write(r, 11, pending(s, status=500)),
            "malformed body": lambda r, s: write(r, 11, {**pending(s), "body": encoded(b"{")}),
            "foreign node": lambda r, s: write(r, 11, pending(s, node="foreign-node")),
            "other path": lambda r, s: write(r, 11, pending(s, path="/varz")),
            "foreign server": lambda r, s: write(r, 11, pending(s, body={"server_id": "OTHER"})),
            "nonempty pending": lambda r, s: write(r, 11, pending(s, body={"messages": 1})),
            "pending consumers": lambda r, s: write(r, 11, pending(s, body={"consumers": 1})),
            "null pending detail": lambda r, s: write(r, 11, pending(s, body={"account_details": [{"name": "APP", "id": "APP", "stream_detail": None}]})),
            "contradictory pending detail": lambda r, s: write(r, 11, pending(s, body={"account_details": s_body(s)["account_details"]})),
            "earlier complete round": lambda r, s: write(r, 11, s),
            "standalone stream group": lambda r, s: (change_body(s, lambda j: j["account_details"][0]["stream_detail"][0].update(stream_raft_group="S-R3F-x")),
                                                     (r / "http/0000.json").write_text(json.dumps({**s, "error": ""}))),
        }
        def s_body(selected):
            import base64
            return json.loads(base64.b64decode(selected["body"]))
        for name, extra in rejected.items():
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = standalone(root, extra)
                with self.subTest(case=name), self.assertRaises(ValueError): validate_report(value, root)
        for name, extra in {
            "R3 server error": lambda r, v: write(r, 12, pending(v["initial_inventory"][2], status=500)),
            "R3 foreign node": lambda r, v: write(r, 12, pending(v["initial_inventory"][2], node="foreign-node", path="/varz")),
            "R3 mixed polls": lambda r, v: write(r, 12, v["initial_inventory"][2]),
        }.items():
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                value = native_document(root)
                if name == "R3 mixed polls":
                    # Select S-3 from a later poll than S-1/S-2, with a pending S-3 between.
                    third = value["initial_inventory"][2]
                    write(root, 11, pending(third))
                    (root / "http/0002.json").unlink()
                    third["order"] = 12
                extra(root, value)
                with self.subTest(case=name), self.assertRaises(ValueError): validate_report(value, root)

    def test_initial_rounds_accept_a_delayed_voter_and_freeze_only_the_final_round(self):
        with tempfile.TemporaryDirectory() as temp:
            value = native_document(Path(temp).resolve())
        value["basic"]["census"]["attempts"][0]["forwarded_order"] = 30
        calls = [{"order": 8}]
        ready = value["initial_inventory"]

        def empty(row):
            sample = copy.deepcopy(row)
            change_body(sample, lambda j: (j.update(streams=0), j["account_details"][0].pop("stream_detail")))
            return sample

        def regroup(row):
            sample = copy.deepcopy(row)
            change_body(sample, lambda j: j["account_details"][0]["stream_detail"][0].update(stream_raft_group="S-R3F-recreated"))
            return sample

        def window(*rows):
            retained = {}
            for order, row in enumerate(rows, 9):
                retained[order] = {**copy.deepcopy(row), "order": order, "error": ""}
            return retained

        def select(retained, *orders):
            bad = copy.deepcopy(value)
            bad["initial_inventory"] = [{k: v for k, v in retained[o].items() if k != "error"} for o in orders]
            return bad

        delayed = window(ready[0], ready[1], empty(ready[2]), *ready)
        initial_rounds(select(delayed, 12, 13, 14), delayed, calls)
        cases = {
            "mixed polls": (delayed, (9, 10, 14)),
            "earlier partial round": (delayed, (9, 10, 11)),
            "final round incomplete": (window(ready[0], ready[1], empty(ready[2]), ready[0], ready[1], empty(ready[2])), (12, 13, 14)),
            "voter group changed": (window(ready[0], regroup(ready[1]), ready[2]), (9, 10, 11)),
            "misaligned round": (window(empty(ready[0]), *ready), (10, 11, 12)),
            "voter order changed": (window(ready[1], ready[0], ready[2]), (9, 10, 11)),
        }
        for name, (retained, orders) in cases.items():
            with self.subTest(case=name), self.assertRaises(ValueError):
                initial_rounds(select(retained, *orders), retained, calls)

    def test_original_stream_group_is_bound_before_restart(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            value = native_document(root)
            change_body(value["initial_inventory"][0], lambda j: j["account_details"][0]["stream_detail"][0].update(stream_raft_group="S-R3F-recreated"))
            with self.assertRaises(ValueError): validate_report(value, root)

    def test_plan_admission_rejects_before_creating_output_or_native_dispatch(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            path = root / "plan.json"
            path.write_text('{"schema_version":1,"messages":3,"payload_bytes":32,"measured_seconds":900,"topologies":["standalone","R3"],"extra":true}')
            pin = hashlib.sha256(path.read_bytes()).hexdigest()
            with patch.object(nats_image, "Native", side_effect=AssertionError("native dispatch forbidden")):
                with self.assertRaises(ValueError): nats_image.run(root / "absent", "0" * 64, path, pin, root / "output")
            self.assertFalse((root / "output").exists())

    def test_retry_requires_authenticated_terminal_failed_predecessor(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp).resolve() / "failed.json"
            build = {"path": "/original/manifest", "sha256": "1" * 64, "size_bytes": 1}
            value = {"kind": "nats-image-preparation", "result": "FAIL", "build_manifest": build, "cleanup_complete": True, "attempt_index": 1}
            path.write_text(json.dumps(value))
            pin = hashlib.sha256(path.read_bytes()).hexdigest()
            self.assertEqual(nats_image.prior(path, pin, "Corrected driver logic", "nats-image-preparation", build, time.monotonic()+10)[1], 2)
            for change in ({"result": "PASS"}, {"cleanup_complete": False}, {"build_manifest": {}}, {"attempt_index": True},
                           {"attempt_index": -1}, {"attempt_index": 0}, {"attempt_index": 2**63}):
                path.write_text(json.dumps({**value, **change}))
                current_pin = hashlib.sha256(path.read_bytes()).hexdigest()
                with self.subTest(change=change), self.assertRaises(ValueError):
                    nats_image.prior(path, current_pin, "Corrected driver logic", "nats-image-preparation", build, time.monotonic()+10)


if __name__ == "__main__": unittest.main()
