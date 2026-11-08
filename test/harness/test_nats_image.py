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
from nats_image_proof import validate_report
from test_nats_image_checks import encoded
from test_nats_image_collection import document, change_body


def native_document(directory, topology="R3"):
    source = document(topology)
    basic = source["basic"]
    count = 1 if topology == "standalone" else 3
    basic["boundaries"]["seal_ack"] = 45
    for index, process in enumerate(basic["processes"][:count]): process["exited_order"] = 46 + index
    result = {"schema_version": 1, "kind": "nats-unchanged-image-native-observations", "status": "PASS", "error": "",
              **source, "rpc": [], "initial_inventory": [], "cleanup_complete": True,
              "restart_publication_denial": encoded(b'-ERR \'Permissions Violation for Publish to "fence.input"\'\r\n')}
    def call(name, subject, request_body, response, order):
        index = len(result["rpc"]) + 1
        reply = f"_INBOX.IMAGE.R{index}".encode()
        body = json.dumps(request_body).encode()
        payload = json.dumps(response).encode()
        row = {"name": name, "request": encoded(b"PUB " + subject.encode() + b" " + reply + b" " + str(len(body)).encode() + b"\r\n" + body + b"\r\n"),
               "response": encoded(b"MSG " + reply + b" 1 " + str(len(payload)).encode() + b"\r\n" + payload + b"\r\n"),
               "body": encoded(payload), "order": order}
        result["rpc"].append(row)
        return row
    call("create", "$JS.API.STREAM.CREATE.FENCE", source["initial"]["config"], source["initial"], 8)
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
            value["account_details"][0]["stream_detail"][0].update(source["initial"])
        change_body(row, mutate)
        result["initial_inventory"].append(row)
    wire = directory / "wire"
    wire.mkdir()
    connect = json.dumps({"user": "publisher", "pass": "fixture-only", "verbose": False, "pedantic": True, "protocol": 1},
                         sort_keys=True, separators=(",", ":")).encode()
    sent = b"CONNECT " + connect + b"\r\nSUB _INBOX.IMAGE.> 1\r\nPING\r\n"
    received = b"INFO " + json.dumps({"server_id": basic["processes"][0]["info_id"]}).encode() + b"\r\nPONG\r\n"
    for attempt in basic["census"]["attempts"]:
        sent += base64.b64decode(attempt["request"])
        received += base64.b64decode(attempt["response"])
    (wire / "004-publisher.sent").write_bytes(sent)
    (wire / "004-publisher.received").write_bytes(received)
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
        identity = store.stat()
        process.update(store_device=identity.st_dev, store_inode=identity.st_ino)
    return result


class NativeProofTests(unittest.TestCase):
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
                (root / "wire" / "004-publisher.sent").write_bytes(b"unaccounted publication")
                with self.assertRaisesRegex(ValueError, "ingress"): validate_report(value, root)

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
            for change in ({"result": "PASS"}, {"cleanup_complete": False}, {"build_manifest": {}}, {"attempt_index": True}):
                path.write_text(json.dumps({**value, **change}))
                current_pin = hashlib.sha256(path.read_bytes()).hexdigest()
                with self.subTest(change=change), self.assertRaises(ValueError):
                    nats_image.prior(path, current_pin, "Corrected driver logic", "nats-image-preparation", build, time.monotonic()+10)


if __name__ == "__main__": unittest.main()
