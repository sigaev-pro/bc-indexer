"""Portable checks for the declared image-fence census and process identities."""

import base64
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import nats_image_checks as checks


def encoded(value):
    return base64.b64encode(value).decode()


def sample(topology="R3"):
    plan = {"schema_version": 1, "messages": 3, "payload_bytes": 32,
            "measured_seconds": 900, "topologies": ["standalone", "R3"]}
    attempts = []
    for ordinal, logical in enumerate((0, 1, 1, 2), 1):
        data = checks.payload(logical, plan["payload_bytes"])
        reply = f"_INBOX.IMAGE.P{ordinal}".encode()
        ack = json.dumps({"stream": "FENCE", "seq": ordinal}, separators=(",", ":")).encode() + b"\r\n"
        attempts.append({"ordinal": ordinal, "logical_message": logical, "connection": "publisher-1",
                         "frame_ordinal": ordinal + 3,
                         "request": encoded(b"PUB fence.input " + reply + b" " + str(len(data)).encode() + b"\r\n" + data + b"\r\n"),
                         "response": encoded(b"MSG " + reply + b" 1 " + str(len(ack)).encode() + b"\r\n" + ack + b"\r\n"),
                         "forwarded_order": ordinal * 3 + 10, "response_order": ordinal * 3 + 11,
                         "application_order": ordinal * 3 + 12,
                         "application_result": "TIMEOUT" if ordinal == 2 else "ACK",
                         "server_outcome": "ACKNOWLEDGED", "retry_of": 2 if ordinal == 3 else None})
    boundaries = {"publishers_closed": 40, "seal_ack": 41, "restart_started": 50,
                  "fence_checked": 60, "export_complete": 70, "finish": 80}
    processes = []
    count = 1 if topology == "standalone" else 3
    for generation in (0, 1):
        for index in range(count):
            name = f"image-S-{index + 1}"
            config = checks.expected_config(topology, generation, index)
            processes.append({"name": name, "generation": generation, "pid": 100 + generation * 10 + index,
                              "start_ticks": 1000 + generation * 100 + index,
                              "info_id": "N" + chr(ord("A") + generation * 3 + index) * 55,
                              "member_id": checks.member_id(name),
                              "argv": ["/usr/local/bin/nats-server", "--config", f"/proof/config/{generation}-{name}.conf"],
                              "config_bytes": encoded(json.dumps(config).encode()),
                              "store": f"/proof/stores/{name}", "store_device": 1, "store_inode": 20 + index,
                              "started_order": 1 + index if generation == 0 else 51 + index,
                              "exited_order": 42 + index if generation == 0 else 71 + index,
                              "exit_code": 0, "wait_error": None, "forced_kill": False})
    return {"schema_version": 1, "kind": "nats-image-supplied-observations", "plan": plan,
            "topology": topology, "census": {"connection": "publisher-1", "closed_order": 40, "attempts": attempts},
            "boundaries": boundaries, "processes": processes}


class ImageObservationTests(unittest.TestCase):
    def test_plan_is_closed_bounded_and_binary_payload_is_independent(self):
        plan = sample()["plan"]
        checks.validate_plan(plan)
        data = checks.payload(0, 32)
        self.assertEqual(len(data), 32)
        self.assertEqual(data[:5], b"\x00\r\n\xff\x80")
        self.assertEqual(data[5:13], b"\x00\x00\x00\x00\x00\x25\x4a\x6f")
        self.assertEqual(checks.payload(1, 16)[:13], b"\x00\r\n\xff\x80\x00\x00\x00\x01\x11\x36\x5b\x80")
        self.assertNotEqual(data, checks.payload(1, 32))
        for key, value in (("messages", True), ("messages", 1), ("messages", 257),
                           ("payload_bytes", 15), ("payload_bytes", 4097), ("measured_seconds", 1801),
                           ("topologies", ["R3"]), ("extra", 1)):
            with self.subTest(field=key, value=value), self.assertRaises(ValueError):
                checks.validate_plan({**plan, key: value})

    def test_complete_attempts_preserve_lost_ack_and_exact_retry_as_two_appends(self):
        document = sample()
        stored = checks.validate_census(document["plan"], document["census"])
        self.assertEqual([row["sequence"] for row in stored], [1, 2, 3, 4])
        self.assertEqual(stored[1]["data"], stored[2]["data"])
        self.assertEqual(stored[0]["data"][:5], b"\x00\r\n\xff\x80")
        for mutate in (
            lambda c: c["attempts"].pop(2),
            lambda c: c["attempts"][2].update(retry_of=None),
            lambda c: c["attempts"][1].update(server_outcome="UNKNOWN"),
            lambda c: c["attempts"][1].update(application_result="ACK"),
            lambda c: c["attempts"][2].update(connection="hidden-publisher"),
            lambda c: c["attempts"][2].update(frame_ordinal=4),
            lambda c: c["attempts"][1].update(response_order=c["attempts"][1]["application_order"] + 1),
            lambda c: c.update(closed_order=1),
        ):
            census = copy.deepcopy(document["census"])
            mutate(census)
            with self.assertRaises(ValueError):
                checks.validate_census(document["plan"], census)

    def test_raw_frames_reject_byte_changes_unrelated_acks_and_duplicate_keys(self):
        document = sample()
        original = document["census"]["attempts"][1]
        for body in (b'{"stream":"FENCE","seq":3}', b'{"stream":"OTHER","seq":2}',
                     b'{"stream":"FENCE","seq":3,"seq":2}', b'{"stream":"OTHER","stream":"FENCE","seq":2}'):
            census = copy.deepcopy(document["census"])
            census["attempts"][1]["response"] = encoded(b"MSG _INBOX.IMAGE.P2 1 " + str(len(body)).encode() + b"\r\n" + body + b"\r\n")
            with self.assertRaises(ValueError):
                checks.validate_census(document["plan"], census)
        for field, raw in (("request", base64.b64decode(original["request"]).replace(b"\xff", b"\xfe", 1)),
                           ("request", base64.b64decode(original["request"])[:-1]),
                           ("response", b"PONG\r\n"),
                           ("response", base64.b64decode(original["response"]).replace(b"P2", b"P1", 1))):
            census = copy.deepcopy(document["census"])
            census["attempts"][1][field] = encoded(raw)
            with self.assertRaises(ValueError):
                checks.validate_census(document["plan"], census)

    def test_process_wait_store_and_restart_bindings_are_required(self):
        for topology in ("standalone", "R3"):
            document = sample(topology)
            result = checks.validate_observations(document)
            self.assertTrue(result["observation_valid"])
            self.assertFalse(result["runtime_admitted"])
            self.assertIn("authenticated native execution", result["missing_evidence"])
        document = sample()
        for field, value in (("exit_code", 1), ("wait_error", "output copy failed"), ("forced_kill", True),
                             ("info_id", document["processes"][3]["info_id"]), ("exited_order", 52),
                             ("member_id", "foreign1"), ("argv", ["substitute"]), ("pid", True)):
            changed = copy.deepcopy(document)
            changed["processes"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                checks.validate_observations(changed)
        for field, value in (("store", "/proof/new-store"), ("store_inode", 999), ("started_order", 43), ("start_ticks", 1)):
            changed = copy.deepcopy(document)
            changed["processes"][3][field] = value
            with self.subTest(restart=field), self.assertRaises(ValueError):
                checks.validate_observations(changed)

    def test_restart_config_cannot_reenable_writers_or_add_an_ingress(self):
        document = sample()
        for mutate in (
            lambda c: c["accounts"]["APP"]["users"].append(checks.expected_config("R3", 0, 0)["accounts"]["APP"]["users"][0]),
            lambda c: c["accounts"]["APP"]["users"][0]["permissions"]["publish"].update(allow=[">"]),
            lambda c: c["accounts"]["APP"]["users"][0]["permissions"].update(allow_responses=True),
            lambda c: c["accounts"]["APP"]["users"][0]["permissions"].update(allow_responses=0),
            lambda c: c.update(max_payload=8192.0),
            lambda c: c.update(no_auth_user="reader"),
            lambda c: c.update(websocket={"port": 9999}),
            lambda c: c["jetstream"].update(store_dir="/proof/replacement"),
        ):
            changed = copy.deepcopy(document)
            config = json.loads(base64.b64decode(changed["processes"][3]["config_bytes"]))
            mutate(config)
            changed["processes"][3]["config_bytes"] = encoded(json.dumps(config).encode())
            with self.assertRaises(ValueError):
                checks.validate_observations(changed)

    def test_cli_authenticates_the_exact_buffer_without_native_dispatch(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            path = root / "observations.json"
            path.write_text(json.dumps(sample()))
            command = [sys.executable, "-B", "-I", str(Path(checks.__file__)), str(path), "--sha256", hashlib.sha256(path.read_bytes()).hexdigest()]
            result = subprocess.run(command, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(json.loads(result.stdout)["runtime_admitted"])
            path.write_text('{"schema_version":1,"schema_version":1}')
            self.assertEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 2)
            command[-1] = hashlib.sha256(path.read_bytes()).hexdigest()
            self.assertEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 2)
            self.assertEqual(sorted(p.name for p in root.iterdir()), ["observations.json"])


if __name__ == "__main__":
    unittest.main()
