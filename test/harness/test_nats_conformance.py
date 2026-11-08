"""Portable checks for named native cases and isolated source preparation."""

import copy
import base64
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

import nats_conformance
from test_nats_fence import frontier


def native_report(name, event_names, observations):
    return {"schema_version": 1, "kind": "nats-source-conformance", "case": name, "outcome": "PASS",
            "cleanup": {"shutdown_complete": True},
            "events": [{"ordinal": i + 1, "name": event} for i, event in enumerate(event_names)],
            "observations": observations}


class ConformanceTests(unittest.TestCase):
    def test_corrected_helper_retry_authenticates_old_and_new_source_bindings(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            def write(name, value):
                path = root / name
                path.write_text(json.dumps(value))
                return nats_conformance.reference(path)
            archive = root / "source.tar"
            archive.write_bytes(b"fixed accepted source archive")
            source_ref = nats_conformance.reference(archive)
            closure = {"source_commit": nats_conformance.SOURCE,
                       "source_archive": {**source_ref, "path": "source.tar"}}
            build = write("manifest.json", {"evidence_kind": "actual-build", "task_id": "E01-01.builds",
                                           "module_closures": {"nats-server": closure}})
            original = {"schema_version": 1, "kind": "nats-source-conformance-tool", "build_manifest": build,
                        "source_commit": nats_conformance.SOURCE, "source_archive": source_ref,
                        "module_closure_sha256": nats_conformance.digest(closure),
                        "compiler": nats_conformance.COMPILER_IMAGE, "overlay": "original helper identity"}
            old_ref = write("old-tool.json", original)
            new_ref = write("new-tool.json", {**original, "overlay": "corrected helper identity"})
            failed = {"kind": "nats-source-conformance-run", "result": "FAIL", "binding": old_ref, "attempt_index": 1}
            previous = write("failure.json", failed)
            previous_path = Path(previous["path"])
            retained, attempt, transition = nats_conformance.run_retry(previous_path, "Correct native collection wait", new_ref, time.monotonic() + 10)
            self.assertEqual((retained, attempt), (previous, 2))
            self.assertEqual(transition["old_tool"], old_ref)
            self.assertEqual(transition["new_tool"], new_ref)
            for label, old_change, tool_change, correction in (
                ("non-failure", {"result": "PASS"}, {}, "correction"),
                ("boolean ordinal", {"attempt_index": True}, {}, "correction"),
                ("missing old tool", {"binding": {**old_ref, "path": str(root / "absent")}}, {}, "correction"),
                ("wrong old pin", {"binding": {**old_ref, "sha256": "0" * 64}}, {}, "correction"),
                ("source changed", {}, {"source_commit": "0" * 40}, "correction"),
                ("closure changed", {}, {"module_closure_sha256": "0" * 64}, "correction"),
                ("archive changed", {}, {"source_archive": {**source_ref, "sha256": "0" * 64}}, "correction"),
                ("build changed", {}, {"build_manifest": {**build, "path": str(root / "other-manifest.json")}}, "correction"),
                ("silent substitution", {}, {}, None),
            ):
                (root / "other-manifest.json").write_bytes((root / "manifest.json").read_bytes())
                write("failure.json", {**failed, **old_change})
                changed_ref = write("new-tool.json", {**original, "overlay": "corrected helper identity", **tool_change})
                with self.subTest(invalid=label), self.assertRaises((ValueError, OSError)):
                    nats_conformance.run_retry(previous_path, correction, changed_ref, time.monotonic() + 10)
            write("failure.json", failed)
            for field, value in (("source_commit", "0" * 40), ("module_closure_sha256", "0" * 64)):
                altered_old = write("old-tool.json", {**original, field: value})
                write("failure.json", {**failed, "binding": altered_old})
                current_ref = write("new-tool.json", {**original, "overlay": "corrected helper identity"})
                with self.subTest(old_tool=field), self.assertRaises(ValueError):
                    nats_conformance.run_retry(previous_path, "correction", current_ref, time.monotonic() + 10)
            write("old-tool.json", original)
            write("failure.json", failed)
            with self.assertRaisesRegex(ValueError, "Invalid retry lineage"):
                nats_conformance.retry(previous_path, "correction", "nats-source-preparation", build, time.monotonic() + 10)

    def test_production_inventory_also_binds_non_go_compiler_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            (root / "server").mkdir()
            for name, value in {"go.mod": b"module fixture", "go.sum": b"sum", "server/native.go": b"package server",
                                "server/embedded.txt": b"production input"}.items():
                (root / name).write_bytes(value)
            before = nats_conformance.production_inventory(root, time.monotonic() + 10)
            (root / "server/embedded.txt").write_bytes(b"changed production input")
            self.assertNotEqual(before, nats_conformance.production_inventory(root, time.monotonic() + 10))

    def test_lost_ack_requires_the_actual_correlated_response_before_loss(self):
        name = "TestExtractorFenceLostAck"
        body = b'{"stream":"FENCE","seq":1}'
        request = b"PUB fence.input _INBOX.EXTRACTOR 20\r\nsource-fence-payload\r\n"
        response = b"MSG _INBOX.EXTRACTOR 1 " + str(len(body)).encode() + b"\r\n" + body + b"\r\n"
        observations = {"request_frame": base64.b64encode(request).decode(), "response_frame": base64.b64encode(response).decode(),
                        "ack_forwarded": False, "message": {"subject": "fence.input", "sequence": 1,
                                                              "data": base64.b64encode(b"source-fence-payload").decode()}}
        observations["binding"], observations["recovered"] = frontier()
        report = native_report(name, ["request_forwarded", "response_observed", "response_dropped", "application_timeout"], observations)
        nats_conformance.validate_case(report, name)
        for broken in (response.replace(b"EXTRACTOR", b"OTHER"), response.replace(b'"seq":1', b'"seq":2'), b"PONG\r\n", response[:-1]):
            altered = copy.deepcopy(report)
            altered["observations"]["response_frame"] = base64.b64encode(broken).decode()
            with self.subTest(frame=broken), self.assertRaises(ValueError):
                nats_conformance.validate_case(altered, name)
        for duplicate in (b'{"stream":"FENCE","seq":2,"seq":1}',
                          b'{"stream":"OTHER","stream":"FENCE","seq":1}',
                          b'{"stream":"FENCE","seq":1,"extra":{"key":1,"key":2}}'):
            altered = copy.deepcopy(report)
            raw = b"MSG _INBOX.EXTRACTOR 1 " + str(len(duplicate)).encode() + b"\r\n" + duplicate + b"\r\n"
            altered["observations"]["response_frame"] = base64.b64encode(raw).decode()
            with self.subTest(duplicate=duplicate), self.assertRaisesRegex(ValueError, "Duplicate JSON key"):
                nats_conformance.validate_case(altered, name)

    def test_delayed_apply_requires_unapplied_empty_queue_then_recovered_progress(self):
        binding, stable = frontier()
        blocked = copy.deepcopy(stable)
        blocked["fence-1"]["$G"]["S-frozen"]["applied"] = 6
        name = "TestExtractorFenceDelayedApply"
        report = native_report(name, ["apply_entered", "blocked_sample", "apply_released", "recovered_sample"],
                               {"binding": binding, "blocked": blocked, "recovered_before": stable, "recovered_after": stable})
        nats_conformance.validate_case(report, name)
        for field, value in (("blocked", stable), ("recovered_after", blocked)):
            changed = copy.deepcopy(report)
            changed["observations"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                nats_conformance.validate_case(changed, name)
        for label, mutate in (
            ("missing voter", lambda sample: sample.pop("fence-3")),
            ("extra voter", lambda sample: sample.update(foreign=copy.deepcopy(sample["fence-3"]))),
            ("foreign ID", lambda sample: sample["fence-2"]["$G"]["S-frozen"].update(id="foreign1")),
            ("boolean applied", lambda sample: sample["fence-1"]["$G"]["S-frozen"].update(applied=False)),
            ("boolean queue", lambda sample: sample["fence-1"]["$G"]["S-frozen"].update(ipq_apply_len=False)),
            ("unsafe follower", lambda sample: sample["fence-2"]["$G"]["S-frozen"].update(paused=True)),
            ("changed term", lambda sample: sample["fence-2"]["$G"]["S-frozen"].update(term=8)),
            ("uncommitted work", lambda sample: sample["fence-2"]["$G"]["S-frozen"].update(pindex=9)),
            ("applied exceeds commit", lambda sample: sample["fence-2"]["$G"]["S-frozen"].update(applied=9)),
            ("response queue", lambda sample: sample["fence-2"]["$G"]["S-frozen"].update(ipq_resp_len=1)),
        ):
            changed = copy.deepcopy(report)
            mutate(changed["observations"]["blocked"])
            with self.subTest(invalid=label), self.assertRaises(ValueError):
                nats_conformance.validate_case(changed, name)

    def test_leader_change_rejects_crossing_collection_and_requires_new_stability(self):
        binding, initial = frontier()
        changed = copy.deepcopy(initial)
        for server, raw in changed.items():
            row = raw["$G"]["S-frozen"]
            row.update(leader="zyhNdjE2", term=3, state="LEADER" if server == "fence-2" else "FOLLOWER")
        name = "TestExtractorFenceLeaderChange"
        report = native_report(name, ["initial_sample", "stepdown_requested", "changed_sample", "stable_sample"],
                               {"binding": binding, "initial": initial, "changed": changed, "stable": changed})
        nats_conformance.validate_case(report, name)
        for field, value in (("changed", initial), ("stable", initial)):
            broken = copy.deepcopy(report)
            broken["observations"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                nats_conformance.validate_case(broken, name)

    def test_native_command_proof_rejects_changed_argv_and_offline_policy(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            def build_tool(network="none", mismatch=False):
                result = {"commands": []}
                for index, tail in enumerate(nats_conformance.PREPARATION_COMMANDS):
                    command = ["docker", "run", "--network", network, "--platform", "linux/amd64", "--pull", "never", "--read-only"]
                    if index < 2:
                        command += ["--workdir", "/source"]
                        for value in ("CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off",
                                      "GOFLAGS=-mod=readonly -modfile=/work/metadata/go.mod", "GOMODCACHE=/modules", "GOTMPDIR=/work/build-temp", "TMPDIR=/work/build-temp"):
                            command += ["--env", value]
                    command += [nats_conformance.COMPILER_IMAGE, *tail]
                    record = {"argv": command, "exit_code": 0, "reason": None, "cleanup_complete": True}
                    if mismatch and index == 1: record["argv"] = ["different"]
                    row = {}
                    for key, value in {"request": json.dumps({"argv": command}), "result": json.dumps(record),
                                       "stdout": "\n".join(nats_conformance.CASES) + "\n" if index == 2 else "",
                                       "stderr": "", "cleanup": "{}"}.items():
                        path = root / f"{index}.{key}"
                        path.write_text(value)
                        row[key] = nats_conformance.reference(path)
                    result["commands"].append(row)
                return result
            nats_conformance.command_provenance(build_tool(), time.monotonic() + 10)
            for options, expected in (({"network": "host"}, "isolation mismatch"),
                                      ({"mismatch": True}, "command failed or changed")):
                with self.subTest(options=options), self.assertRaisesRegex(ValueError, expected):
                    nats_conformance.command_provenance(build_tool(**options), time.monotonic() + 10)
            tool = build_tool()
            listing = root / "2.stdout"
            listing.write_text("NotTheRequiredNativeTests\n")
            tool["commands"][2]["stdout"] = nats_conformance.reference(listing)
            original = nats_conformance.artifact
            replaced = []
            def replace_after_authentication(ref, *args, **kwargs):
                result = original(ref, *args, **kwargs)
                if ref == tool["commands"][2]["stdout"]:
                    listing.write_text("\n".join(nats_conformance.CASES) + "\n")
                    replaced.append(True)
                return result
            with patch.object(nats_conformance, "artifact", side_effect=replace_after_authentication):
                with self.assertRaisesRegex(ValueError, "Artifact bytes changed|Compiled native case list differs"):
                    nats_conformance.command_provenance(tool, time.monotonic() + 10)
            self.assertEqual(replaced, [True])

    def test_zero_exit_without_the_selected_native_case_cannot_pass(self):
        name = "TestExtractorFenceStandalone"
        for output in (b"PASS\n", b"testing: warning: no tests to run\nPASS\n",
                       b"=== RUN   Other\n--- PASS: Other (0.01s)\nPASS\n",
                       b"=== RUN   TestExtractorFenceStandalone\n--- SKIP: TestExtractorFenceStandalone (0s)\nPASS\n"):
            with self.subTest(output=output), self.assertRaises(ValueError):
                nats_conformance.selected_test(output, name)
        nats_conformance.selected_test(("=== RUN   " + name + "\n--- PASS: " + name + " (0.10s)\nPASS\n").encode(), name)

    def test_owned_source_excludes_only_root_package_tests(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            (root / "server/sub").mkdir(parents=True)
            for name, data in {"go.mod": b"module fixture", "go.sum": b"sum", "server/native.go": b"package server",
                               "server/upstream_test.go": b"package server", "server/sub/keep_test.go": b"package sub"}.items():
                (root / name).write_bytes(data)
            result = nats_conformance.exclude_upstream_tests(root)
            self.assertEqual([row["path"] for row in result], ["server/upstream_test.go"])
            self.assertFalse((root / "server/upstream_test.go").exists())
            self.assertEqual((root / "server/native.go").read_bytes(), b"package server")
            self.assertTrue((root / "server/sub/keep_test.go").is_file())
            self.assertEqual((root / "go.mod").read_bytes(), b"module fixture")

    def test_native_case_report_requires_cleanup_order_and_actual_message(self):
        report = {"schema_version": 1, "kind": "nats-source-conformance", "case": "TestExtractorFenceStandalone",
                  "outcome": "PASS", "cleanup": {"shutdown_complete": True},
                  "events": [{"ordinal": i + 1, "name": name} for i, name in enumerate([
                      "storage_entered", "seal_started", "storage_released", "seal_completed"])],
                  "observations": {"mutex_locked": True, "seal_after_release": True,
                      "stream": {"config": {"name": "FENCE", "sealed": True, "discard": "new", "deny_delete": True,
                                              "deny_purge": True, "allow_rollup_hdrs": False, "max_age": 0}},
                      "message": {"subject": "fence.input", "sequence": 1, "data": "c291cmNlLWZlbmNlLXBheWxvYWQ="}}}
        nats_conformance.validate_case(report, report["case"])
        for mutate in (
            lambda row: row["cleanup"].update(shutdown_complete=False),
            lambda row: row["events"].reverse(),
            lambda row: row["observations"].update(mutex_locked=False),
            lambda row: row["observations"]["message"].update(sequence=2),
            lambda row: row["observations"]["stream"]["config"].update(sealed=False),
            lambda row: row.update(case="Unknown"),
        ):
            broken = copy.deepcopy(report)
            mutate(broken)
            with self.assertRaises(ValueError):
                nats_conformance.validate_case(broken, report["case"])


if __name__ == "__main__":
    unittest.main()
