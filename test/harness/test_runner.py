"""Portable controls for the local native-seam runner; fake slices replace native processes."""

import copy
import hashlib
import os
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

import profile_manifest
import run_plan
import runner
from test_profile_manifest import family_tree

SLICES = {"geth-import": "local-native-import", "bsc-parlia-import": "bsc-local-native-import", "bor-import": "bor-local-native-import"}
AXES = ("ac_id", "network", "profile", "profile_manifest_digest", "fork_variant", "fixture_kind", "suite", "variant_id")
FAKE = '''import hashlib, json, os, sys, time
args = sys.argv[1:]
request = args[1]
output = args[args.index("--output") + 1]
behavior = json.loads(os.environ.get("FAKE_SLICE", "{{}}")).get("{kind}", "PASS")
if os.environ.get("FAKE_PID"):
    open(os.environ["FAKE_PID"], "w").write(str(os.getpid()))
if behavior == "IGNORE_TERM":
    import signal
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    time.sleep(60)
if behavior == "SLEEP":
    time.sleep(60)
if behavior == "LOUD":
    sys.stdout.write("x" * 20000)
os.mkdir(output)
data = open(request, "rb").read()
proof = {{"kind": "{kind}", "result": "FAIL" if behavior == "FAIL" else "PASS", "cleanup_complete": behavior != "DIRTY",
         "native_owner_nonce": "fake", "request": {{"path": request, "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}}}}
if behavior == "OTHERKIND": proof["kind"] = "other"
if behavior == "OTHERREQUEST": proof["request"]["sha256"] = "0" * 64
text = {{"LIST": "[]", "BADJSON": "{{not json", "DUPKEY": '{{"kind": "a", "kind": "b"}}'}}.get(behavior, json.dumps(proof))
open(os.path.join(output, "proof.json"), "w").write(text)
print(json.dumps({{"result": proof["result"]}}))
sys.exit(3 if behavior == "EXIT" else 0 if proof["result"] == "PASS" else 2)
'''


def write(path, value):
    data = (json.dumps(value, indent=2) + "\n").encode()
    path.write_bytes(data)
    return {"path": str(path), "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}


def build_plan(root, mutate_plan=None, slices=tuple(SLICES)):
    """Write three synthetic family trees, their test-only manifests and a frozen native-seam plan."""
    cases, fixtures, sources, actions, quotas, manifests, profiles = [], [], [], [], [], [], []
    for slice_name in slices:
        directory = root / slice_name
        directory.mkdir()
        manifest = family_tree(directory, slice_name)
        manifest["fixture"]["fixture_id"] = "private-" + slice_name
        digest = profile_manifest.evidence.digest(manifest)
        profile = manifest["profile"]
        profiles.append(profile)
        case = {"schema_version": 3, "ac_id": "AC-CAP-01-01", "network": manifest["family"], "profile": profile,
                "profile_manifest_digest": digest, "fork_variant": "private-genesis", "fixture_kind": "native-seam", "suite": "quick",
                "variant_id": "transfer-then-empty", "task_id": "E01-02.lifecycle_seams", "fixture_id": manifest["fixture"]["fixture_id"],
                "fixture_version": "1", "applicability": "APPLICABLE"}
        cases.append(case)
        identity = {key: case[key] for key in AXES}
        quotas.append({"case_identity": identity, "minimum": 1})
        fixtures.append({"fixture_id": case["fixture_id"], "fixture_version": "1", "profile": profile, "fork_variant": "private-genesis",
                         "identity": {"kind": "test-only", "chain_id": manifest["identity"]["chain_id"],
                                      "genesis_hash": manifest["identity"]["genesis_hash"], "native_config": manifest["inputs"][0]},
                         "inputs": manifest["inputs"], "oracle": manifest["oracle"], "preparation": manifest["preparation"],
                         "public_correspondence": None})
        sources.append({"profile": profile, "fixture_id": case["fixture_id"], "fixture_version": "1", "fork_variant": "private-genesis",
                        "start": {"kind": "prepared-genesis", "height": 0, "block_hash": manifest["identity"]["genesis_hash"]}})
        actions.append({"action_id": "import-" + slice_name, "profile": profile, "kind": "native-import", "count": 1,
                        "input": manifest["runner"]["request"], "expected": manifest["oracle"], "outcome": "applied", "fault_id": None})
        manifests.append({"profile": profile, "profile_manifest_digest": digest, "artifact": write(directory / "manifest.json", manifest)})
        build = manifest["build_manifest"]
    bundle = {"contract": "evidence-artifacts-v1", "kind": "CaseRegistry", "synthetic": True,
              "document": {"schema_version": 3, "version": "synthetic-runner-1", "cases": cases},
              "context": {"task_ids": ["E01-02.lifecycle_seams"], "case_owners": {"AC-CAP-01-01": "E01-02.lifecycle_seams"}, "rules": {}}}
    registry = write(root / "registry.json", bundle)
    plan = {"schema_version": 1, "plan_id": "synthetic-local-runner", "fixture_kind": "native-seam", "suite": "quick", "mode": "standard",
            "coverage": "PARTIAL", "seed": 1, "profiles": profiles, "case_identities": [row["case_identity"] for row in quotas],
            "build_manifest": build, "profile_manifests": manifests, "fixtures": fixtures,
            "resources": {"architecture": "amd64", "cpus": 2, "ram_bytes": 1024, "disk_bytes": 1024, "parallelism": 1},
            "volumes": {"blocks": 2, "transactions": 1, "operations": 1, "payload_bytes": 0, "record_bytes": 0, "outage_seconds": 0, "soak_seconds": 0},
            "deadlines": {"preparation_seconds": 900, "startup_seconds": 60, "event_seconds": 300, "measured_seconds": 1800,
                          "comparison_seconds": 300, "teardown_seconds": 120},
            "sources": sources, "stop": {"operator": "AND", "duration_seconds": None, "native_decisions": len(slices),
                                         "driver_actions": len(slices), "require_action_outcomes": True},
            "actions": actions, "quotas": quotas, "faults": [], "terminal_fence": None}
    if mutate_plan: mutate_plan(plan)
    write(root / "plan.json", plan)
    frozen = root / "frozen"
    run_plan.resolve(root / "plan.json", root / "registry.json", registry["sha256"], frozen)
    return frozen, hashlib.sha256((frozen / "resolution.json").read_bytes()).hexdigest()


def observed(**changes):
    value = {"architecture": "amd64", "cpus": 8, "memory_bytes": 1 << 34, "free_disk_bytes": 1 << 40,
             "docker": {"cpus": 8, "memory_bytes": 1 << 34, "architecture": "x86_64", "harness_containers": []}}
    for key, item in changes.items():
        if key.startswith("docker_"): value["docker"][key[7:]] = item
        else: value[key] = item
    return value


class RunnerBase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        fakes = {}
        for slice_name, kind in SLICES.items():
            path = self.root / f"fake-{slice_name}.py"
            path.write_text(FAKE.format(kind=kind))
            fakes[slice_name] = {**runner.CHILDREN[slice_name], "script": str(path)}
        self.patches = [patch.dict(runner.CHILDREN, fakes), patch.object(runner, "host_resources", lambda _: observed()),
                        patch.object(runner, "teardown", self.fake_teardown), patch.object(runner, "LOCK", self.root / "runner.lock")]
        for item in self.patches: item.start()

    def fake_teardown(self, output, nonces=()):
        self.nonces = list(nonces)
        self.teardowns = getattr(self, "teardowns", 0) + 1
        return {"forced_container_removal": False, "remaining_owned_containers": [], "foreign_harness_containers": [],
                "cleanup_complete": True}

    def tearDown(self):
        for item in reversed(self.patches): item.stop()
        self.temp.cleanup()


class RunnerTests(RunnerBase):
    def setup_dirs(self, name):
        (self.root / name / "inputs").mkdir(parents=True)

    def test_admits_and_runs_all_three_families_without_public_credit(self):
        self.setup_dirs("out")
        frozen, pin = build_plan(self.root / "out" / "inputs")
        record = runner.run(frozen, pin, self.root / "out" / "run")
        self.assertEqual(record["result"], "PASS")
        self.assertEqual([row["result"] for row in record["children"]], ["PASS"] * 3)
        self.assertFalse(record["public_profile_credit"])
        self.assertIn("private_identity_has_no_public_profile_credit", record["blockers"])
        admission = json.loads((self.root / "out/run/admission.json").read_text())
        self.assertTrue(admission["runtime_admitted"])
        self.assertNotIn("profile_manifest_verification_unimplemented", admission["blockers"])
        for ref in record["case_results"]:
            self.assertEqual(json.loads(Path(ref["path"]).read_text())["verdict"], "NOT_RUN")
        for ref in record["reader_boundaries"]:
            boundary = json.loads(Path(ref["path"]).read_text())
            self.assertFalse(boundary["run_started"])
            self.assertTrue(all(row["status"] == "NOT_RUN" for row in boundary["conditions"].values()))
        phases = record["phases"]
        self.assertTrue(phases["preparation"]["finished_at"] <= phases["execution"]["started_at"] <=
                        phases["execution"]["finished_at"] <= phases["teardown"]["started_at"])

    def test_admission_rejects_plan_and_binding_changes(self):
        def mutate_manifest_digest(plan):
            plan["profile_manifests"][0]["profile_manifest_digest"] = "0" * 64
            plan["case_identities"][0]["profile_manifest_digest"] = "0" * 64
            plan["quotas"][0]["case_identity"]["profile_manifest_digest"] = "0" * 64
        cases = {
            "missing manifest": (lambda p: p["profile_manifests"].pop(), "one test-only ProfileManifest"),
            "manifest digest": (mutate_manifest_digest, None),
            "other oracle": (lambda p: p["fixtures"][0].update(oracle=p["fixtures"][1]["oracle"]), None),
            "other preparation": (lambda p: p["fixtures"][0].update(preparation=p["fixtures"][1]["preparation"]), None),
            "other inputs": (lambda p: p["fixtures"][0].update(inputs=p["fixtures"][1]["inputs"]), None),
            "action input": (lambda p: p["actions"][0].update(input=p["actions"][1]["input"]), "native-import action"),
            "two imports": (lambda p: p["actions"][0].update(count=2), None),
            "parallel": (lambda p: p["resources"].update(parallelism=2), "parallelism"),
            "short budget": (lambda p: p["deadlines"].update(measured_seconds=900, event_seconds=300, comparison_seconds=300), "parent measured budget"),
        }
        for index, (name, (mutate, reason)) in enumerate(cases.items()):
            root = self.root / f"case{index}"
            root.mkdir()
            try:
                frozen, pin = build_plan(root, mutate)
            except ValueError:
                continue  # rejected earlier by the offline RunPlan check, which is also a rejection
            with self.subTest(case=name), self.assertRaisesRegex(ValueError, reason or ""):
                runner.admit(frozen, pin, root / "out", time.monotonic() + 30)
            self.assertFalse((root / "out").exists())

    def test_admission_rejects_other_fixture_kinds_and_stale_freezes(self):
        root = self.root / "kinds"
        root.mkdir()
        frozen, pin = build_plan(root)
        with self.assertRaisesRegex(ValueError, "external pin"):
            runner.admit(frozen, "0" * 64, root / "out", time.monotonic() + 30)
        plan = json.loads((frozen / "plan.json").read_text())
        for change in ({"fixture_kind": "local-e2e"}, {"mode": "extended"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                runner.bind_fixtures({**plan, **change}, None, time.monotonic() + 30)
        (frozen / "plan.json").write_bytes((frozen / "plan.json").read_bytes() + b" ")
        with self.assertRaisesRegex(ValueError, "differs from the resolution"):
            runner.admit(frozen, pin, root / "out", time.monotonic() + 30)

    def test_resource_checks_reject_insufficient_or_shared_hosts(self):
        plan = {"resources": {"architecture": "amd64", "cpus": 2, "ram_bytes": 1024, "disk_bytes": 1024, "parallelism": 1}}
        runner.check_resources(plan, observed())
        for change in ({"architecture": "arm64"}, {"docker_architecture": "aarch64"}, {"cpus": 1}, {"docker_cpus": 1},
                       {"memory_bytes": 10}, {"docker_memory_bytes": 10}, {"free_disk_bytes": 10},
                       {"docker_harness_containers": ["foreign"]}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                runner.check_resources(plan, observed(**change))

    def test_child_failures_stop_dispatch_and_fail_the_run(self):
        for index, behavior in enumerate(({"local-native-import": "FAIL"}, {"local-native-import": "DIRTY"})):
            root = self.root / f"fail{index}"
            root.mkdir()
            frozen, pin = build_plan(root)
            with patch.dict("os.environ", {"FAKE_SLICE": json.dumps(behavior)}):
                record = runner.run(frozen, pin, root / "out")
            with self.subTest(behavior=behavior):
                self.assertEqual(record["result"], "FAIL")
                self.assertEqual([row["result"] for row in record["children"]], ["PASS", "FAIL"])
                self.assertEqual(len(record["not_dispatched"]), 1)
                self.assertTrue(all(json.loads(Path(ref["path"]).read_text())["verdict"] == "NOT_RUN" for ref in record["case_results"]))

    def test_parent_deadline_kills_the_child_group(self):
        root = self.root / "deadline"
        root.mkdir()
        frozen, pin = build_plan(root, slices=("geth-import",))
        with patch.dict("os.environ", {"FAKE_SLICE": json.dumps({"local-native-import": "SLEEP"})}):
            plan, bundle, admitted = runner.admit(frozen, pin, root / "out", time.monotonic() + 30)
            row = admitted["fixtures"][0]
            started = time.monotonic()
            (root / "logs").mkdir()
            record = runner.run_child(row, root / "out" / "child", time.monotonic() + 2, root / "logs")
        self.assertLess(time.monotonic() - started, 20)
        self.assertEqual((record["reason"], record["result"], record["process_group_absent"]), ("PARENT_DEADLINE", "FAIL", True))

    def test_single_retry_requires_a_retained_first_fail(self):
        root = self.root / "retry"
        root.mkdir()
        frozen, pin = build_plan(root)
        with patch.dict("os.environ", {"FAKE_SLICE": json.dumps({"bor-local-native-import": "FAIL"})}):
            first = runner.run(frozen, pin, root / "first")
        first_ref = root / "first" / "run-record.json"
        first_sha = hashlib.sha256(first_ref.read_bytes()).hexdigest()
        second = runner.run(frozen, pin, root / "second", first_ref, first_sha, "Repeat after a diagnostic correction")
        self.assertEqual(second["attempt"]["attempt_index"], 2)
        self.assertEqual(second["attempt"]["first_run_id"], first["run_id"])
        second_sha = hashlib.sha256((root / "second" / "run-record.json").read_bytes()).hexdigest()
        rejected = {
            "passing first": (root / "second" / "run-record.json", second_sha, "Repeat after a diagnostic correction"),
            "wrong pin": (first_ref, "0" * 64, "Repeat after a diagnostic correction"),
            "short reason": (first_ref, first_sha, "x"),
        }
        for name, (path, sha, reason) in rejected.items():
            with self.subTest(case=name), self.assertRaises(ValueError):
                runner.run(frozen, pin, root / ("bad-" + name.replace(" ", "-")), path, sha, reason)
        with self.assertRaises(ValueError):
            runner.run(frozen, pin, root / "orphan", None, first_sha, None)


class RunnerDefenseTests(RunnerBase):
    """Direct controls for checks that the offline RunPlan check usually catches first."""

    def frozen(self, name="defense"):
        root = self.root / name
        root.mkdir()
        frozen, pin = build_plan(root)
        return root, frozen, pin, json.loads((frozen / "plan.json").read_text())

    def test_binding_rejects_each_changed_declaration(self):
        root, frozen, pin, plan = self.frozen()
        runner.bind_fixtures(plan, None, time.monotonic() + 30)
        extra = copy.deepcopy(plan["fixtures"][0])
        extra["fixture_id"] = "second-fixture"
        cases = {
            "declared digest": (lambda p: p["profile_manifests"][0].update(profile_manifest_digest="0" * 64), "digest or profile"),
            "swapped manifest": (lambda p: p["profile_manifests"][0].update(artifact=p["profile_manifests"][1]["artifact"]), "digest or profile"),
            "two fixtures": (lambda p: p["fixtures"].append(extra), "exactly one local fixture"),
            "public identity": (lambda p: p["fixtures"][0]["identity"].update(kind="public"), "test-only fixtures"),
            "public correspondence": (lambda p: p["fixtures"][0].update(public_correspondence=p["fixtures"][0]["oracle"]), "test-only fixtures"),
            "fixture id": (lambda p: p["fixtures"][0].update(fixture_id="other"), "fixture identity differs"),
            "chain id": (lambda p: p["fixtures"][0]["identity"].update(chain_id="1"), "chain identity"),
            "case digest": (lambda p: p["case_identities"][0].update(profile_manifest_digest="0" * 64), "another ProfileManifest"),
            "fault": (lambda p: p.update(faults=[{"fault_id": "f", "kind": "process-crash", "maximum_duration_seconds": 1}]), "no fault actions"),
            "driver actions": (lambda p: p["stop"].update(driver_actions=1), "no fault actions"),
        }
        for name, (mutate, reason) in cases.items():
            changed = copy.deepcopy(plan)
            mutate(changed)
            with self.subTest(case=name), self.assertRaisesRegex(ValueError, reason):
                runner.bind_fixtures(changed, None, time.monotonic() + 30)

    def repin(self, frozen, report):
        data = runner.evidence.canonical(report)
        (frozen / "resolution.json").write_bytes(data)
        return hashlib.sha256(data).hexdigest()

    def test_frozen_plan_rejects_relative_or_rewritten_resolutions(self):
        root, frozen, pin, plan = self.frozen()
        deadline = time.monotonic() + 30
        with self.assertRaisesRegex(ValueError, "must be absolute"):
            runner.frozen_plan(Path("relative"), pin, deadline)
        original = json.loads((frozen / "resolution.json").read_text())
        def resign(report):
            unsigned = {key: value for key, value in report.items() if key != "resolution_digest"}
            report["resolution_digest"] = runner.evidence.digest(unsigned)
            return report
        for change in ({"runtime_admitted": True}, {"kind": "other"}, {"plan_valid": False}):
            report = resign({**original, **change})
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "Not a valid offline"):
                runner.frozen_plan(frozen, self.repin(frozen, report), deadline)
        with self.assertRaisesRegex(ValueError, "Resolution digest changed"):
            runner.frozen_plan(frozen, self.repin(frozen, {**original, "coverage": {}}), deadline)
        bundle = json.loads((frozen / "registry.json").read_text())
        bundle["document"]["version"] = "changed"
        data = (json.dumps(bundle) + "\n").encode()
        (frozen / "registry.json").write_bytes(data)
        report = resign({**original, "registry_bundle": {**original["registry_bundle"], "sha256": hashlib.sha256(data).hexdigest(),
                                                           "size_bytes": len(data)}})
        with self.assertRaisesRegex(ValueError, "Registry digest changed"):
            runner.frozen_plan(frozen, self.repin(frozen, report), deadline)

    def test_docker_queries_and_output_ownership_fail_closed(self):
        class Result:
            def __init__(self, code, stdout=""): self.returncode, self.stdout = code, stdout
        info = json.dumps({"NCPU": 4, "MemTotal": 1, "Architecture": "x86_64"})
        for name, (results, reason) in {"info": ([Result(1, info)], "Docker is unavailable"),
                                        "owned": ([Result(0, info), Result(1)], "Owned container query")}.items():
            with self.subTest(query=name), patch.object(runner.subprocess, "run", side_effect=results), self.assertRaisesRegex(ValueError, reason):
                runner.docker_state()
        root, frozen, pin, plan = self.frozen("ownership")
        (root / "taken").mkdir()
        for output in (root / "taken", Path("relative-output"), root / "missing-parent" / "out"):
            with self.subTest(output=str(output)), self.assertRaisesRegex(ValueError, "new absolute directory"):
                runner.admit(frozen, pin, output, time.monotonic() + 30)
        _, _, admitted = runner.admit(frozen, pin, root / "out", time.monotonic() + 30)
        with self.assertRaisesRegex(ValueError, "expired before dispatch"):
            runner.run_child(admitted["fixtures"][0], root / "late", time.monotonic() - 1, root)

    def test_cli_rejects_retry_metadata_for_admission(self):
        root, frozen, pin, plan = self.frozen("cli")
        argv = ["runner.py", "admit", str(frozen), "--resolution-sha256", pin, "--output", str(root / "out"),
                "--previous-run", str(root / "x"), "--previous-sha256", "0" * 64, "--retry-reason", "diagnostic retry"]
        with patch.object(runner.sys, "argv", argv), patch("builtins.print") as printed:
            self.assertEqual(runner.main(), 2)
        self.assertIn("Only run accepts retry metadata", printed.call_args[0][0])
        self.assertFalse((root / "out").exists())


class RunnerRobustnessTests(RunnerBase):
    def plan(self, name, slices=tuple(SLICES)):
        root = self.root / name
        root.mkdir()
        return (root, *build_plan(root, slices=slices))

    def test_bad_child_proofs_fail_after_full_teardown(self):
        for behavior in ("LIST", "BADJSON", "DUPKEY", "OTHERKIND", "OTHERREQUEST", "EXIT"):
            root, frozen, pin = self.plan("proof-" + behavior.lower(), ("geth-import",))
            self.teardowns = 0
            with patch.dict("os.environ", {"FAKE_SLICE": json.dumps({"local-native-import": behavior})}):
                record = runner.run(frozen, pin, root / "out")
            with self.subTest(behavior=behavior):
                self.assertEqual((record["result"], record["children"][0]["result"], self.teardowns), ("FAIL", "FAIL", 1))
                self.assertTrue((root / "out" / "run-record.json").is_file())

    def test_incomplete_teardown_fails_an_otherwise_passing_run(self):
        root, frozen, pin = self.plan("dirty-teardown", ("geth-import",))
        dirty = lambda output, nonces=(): {"forced_container_removal": True, "remaining_owned_containers": ["left"],
                                "foreign_harness_containers": [], "cleanup_complete": False}
        with patch.object(runner, "teardown", dirty):
            record = runner.run(frozen, pin, root / "out")
        self.assertEqual((record["result"], record["children"][0]["result"]), ("FAIL", "PASS"))

    def test_deadline_escalates_to_sigkill_for_a_child_that_ignores_sigterm(self):
        root, frozen, pin = self.plan("ignore-term", ("geth-import",))
        pid_file = root / "child.pid"
        with patch.dict("os.environ", {"FAKE_SLICE": json.dumps({"local-native-import": "IGNORE_TERM"}), "FAKE_PID": str(pid_file)}):
            _, _, admitted = runner.admit(frozen, pin, root / "out", time.monotonic() + 30)
            (root / "logs").mkdir()
            started = time.monotonic()
            record = runner.run_child(admitted["fixtures"][0], root / "out" / "child", time.monotonic() + 1.5, root / "logs")
        self.assertLess(time.monotonic() - started, 30)
        self.assertEqual((record["reason"], record["process_group_absent"]), ("PARENT_DEADLINE", True))
        with self.assertRaises(ProcessLookupError):
            os.kill(int(pid_file.read_text()), 0)

    def test_stop_group_tolerates_a_zombie_only_group(self):
        class Leader:
            pid, waited = 999999, 0
            def wait(self, timeout=None):
                self.waited += 1
        leader = Leader()
        with patch.object(runner.os, "killpg", side_effect=PermissionError):
            runner.stop_group(leader)
        self.assertEqual(leader.waited, 1)

    def test_records_output_tail_nonce_and_skip_evidence_on_error(self):
        root, frozen, pin = self.plan("loud", ("geth-import",))
        with patch.dict("os.environ", {"FAKE_SLICE": json.dumps({"local-native-import": "LOUD"})}):
            record = runner.run(frozen, pin, root / "out")
        child = record["children"][0]
        self.assertEqual((child["stdout"]["bytes"] > 20000, len(child["stdout"]["tail"]), child["native_owner_nonce"]), (True, 4096, "fake"))
        self.assertEqual(self.nonces, ["fake"])
        root, frozen, pin = self.plan("raises", ("geth-import",))
        with patch.object(runner, "run_child", side_effect=OSError("child launch failed")):
            record = runner.run(frozen, pin, root / "out")
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("child launch failed", record["error"])
        self.assertNotIn("case_results", record)

    def test_teardown_errors_and_deadlines_fail_the_run(self):
        root, frozen, pin = self.plan("teardown-error", ("geth-import",))
        with patch.object(runner, "teardown", side_effect=OSError("docker down")):
            record = runner.run(frozen, pin, root / "out")
        self.assertEqual((record["result"], record["teardown"]["cleanup_complete"]), ("FAIL", False))
        root = self.root / "teardown-slow"
        root.mkdir()
        frozen, pin = build_plan(root, lambda p: p["deadlines"].update(teardown_seconds=1), slices=("geth-import",))
        def slow(output, nonces=()):
            time.sleep(1.2)
            return self.fake_teardown(output, nonces)
        with patch.object(runner, "teardown", slow):
            record = runner.run(frozen, pin, root / "out")
        self.assertEqual((record["result"], record["teardown"]["deadline_exceeded"]), ("FAIL", True))

    def test_admission_honors_the_plan_preparation_deadline(self):
        root = self.root / "slow-admission"
        root.mkdir()
        frozen, pin = build_plan(root, lambda p: p["deadlines"].update(preparation_seconds=1, startup_seconds=1), slices=("geth-import",))
        def slow(_):
            time.sleep(1.2)
            return observed()
        with patch.object(runner, "host_resources", slow), self.assertRaises(ValueError):
            runner.admit(frozen, pin, root / "out", time.monotonic() + 1800)
        self.assertFalse((root / "out").exists())

    def test_evidence_failure_after_teardown_still_writes_a_fail_record(self):
        root, frozen, pin = self.plan("evidence-error", ("geth-import",))
        with patch.object(runner, "reader_boundaries", side_effect=ValueError("Artifact bytes changed")):
            record = runner.run(frozen, pin, root / "out")
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("Artifact bytes changed", json.loads((root / "out" / "run-record.json").read_text())["error"])

    def test_child_pass_requires_every_condition(self):
        row = {"slice": "bsc-parlia-import", "request": {"path": "/r", "sha256": "1" * 64, "size_bytes": 1}}
        proof = {"kind": "bsc-local-native-import", "result": "PASS", "cleanup_complete": True, "request": dict(row["request"])}
        self.assertTrue(runner.child_passed(row, proof, 0, True))
        for name, (changed, code, absent) in {
            "kind": ({**proof, "kind": "bor-local-native-import"}, 0, True), "result": ({**proof, "result": "FAIL"}, 0, True),
            "cleanup": ({**proof, "cleanup_complete": False}, 0, True), "request": ({**proof, "request": {**row["request"], "sha256": "2" * 64}}, 0, True),
            "exit": (proof, 1, True), "group": (proof, 0, False), "not object": ([proof], 0, True),
        }.items():
            with self.subTest(case=name):
                self.assertFalse(runner.child_passed(row, changed, code, absent))

    def test_child_command_passes_the_pinned_observer_tool_only_where_needed(self):
        request = {"path": "/req.json"}
        tool = {"path": "/tool.json", "sha256": "3" * 64}
        geth = runner.child_command({"slice": "geth-import", "request": request, "observer_tool": None}, Path("/o"))
        self.assertNotIn("--observer-tool", geth)
        for slice_name in ("bsc-parlia-import", "bor-import"):
            command = runner.child_command({"slice": slice_name, "request": request, "observer_tool": tool}, Path("/o"))
            self.assertEqual(command[command.index("--observer-tool") + 1:command.index("--observer-tool") + 4],
                             ["/tool.json", "--observer-tool-sha256", "3" * 64])
            self.assertEqual(command[-2:], ["--output", "/o"])

    def test_signal_kills_the_child_group_and_still_tears_down(self):
        import os, signal, threading
        root, frozen, pin = self.plan("signal", ("geth-import",))
        self.teardowns = 0
        pid_file = root / "child.pid"
        def signal_when_child_runs():
            deadline = time.monotonic() + 20
            while not pid_file.exists() and time.monotonic() < deadline:
                time.sleep(0.05)
            os.kill(os.getpid(), signal.SIGTERM)
        timer = threading.Thread(target=signal_when_child_runs)
        started = time.monotonic()
        with patch.dict("os.environ", {"FAKE_SLICE": json.dumps({"local-native-import": "SLEEP"}), "FAKE_PID": str(pid_file)}):
            timer.start()
            record = runner.run(frozen, pin, root / "out")
        timer.join()
        child = int(pid_file.read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(child, 0)
        self.assertTrue((root / "out" / "run-record.json").is_file())
        self.assertLess(time.monotonic() - started, 20)
        self.assertEqual((record["result"], self.teardowns), ("FAIL", 1))
        self.assertIn("Interrupted", record["error"])
        self.assertIsNot(signal.getsignal(signal.SIGTERM), runner.raise_interrupted)

    def test_single_writer_lock_rejects_a_second_runner(self):
        import fcntl
        root, frozen, pin = self.plan("lock", ("geth-import",))
        with open(runner.LOCK, "a") as handle:
            fcntl.flock(handle, fcntl.LOCK_SH)
            with self.assertRaisesRegex(ValueError, "single-writer lock"):
                runner.run(frozen, pin, root / "out")
        self.assertFalse((root / "out").exists())

    def test_teardown_removes_only_containers_mounted_from_this_run(self):
        (self.root / "real-scoped").mkdir()
        output = self.root / "scoped"
        output.symlink_to(self.root / "real-scoped")
        mounts = {"mine": [{"Source": str(output / "children" / "x")}], "foreign": [{"Source": "/elsewhere"}],
                  "sibling": [{"Source": str(output) + "-2/x"}], "labeled": [{"Source": "/other"}],
                  "resolved": [{"Source": str(output.resolve() / "y")}]}
        labels = {"mine": "n1", "foreign": "zz", "sibling": "zz", "labeled": "child-nonce", "resolved": "n2"}
        state = {"live": ["mine", "foreign", "sibling", "labeled", "resolved"], "removed": []}
        class Result:
            def __init__(self, code, stdout=""): self.returncode, self.stdout = code, stdout
        def fake_run(command, **_):
            if command[:2] == ["docker", "inspect"]:
                return Result(0, json.dumps(mounts[command[-1]]) + "|" + labels[command[-1]]) if command[-1] in state["live"] else Result(1)
            if command[:3] == ["docker", "rm", "-f"]:
                state["removed"] += command[3:]
                state["live"] = [item for item in state["live"] if item not in command[3:]]
                return Result(0)
            raise AssertionError(command)
        for item in self.patches[2:3]: item.stop()
        try:
            with patch.object(runner.subprocess, "run", fake_run), \
                 patch.object(runner, "docker_state", lambda: {"harness_containers": list(state["live"])}):
                cleanup = runner.teardown(output, ["child-nonce"])
                state["live"] = [item for item in state["live"] if item not in ("foreign", "sibling")]
                clean = runner.teardown(output, [])
        finally:
            self.patches[2].start()
        self.assertEqual(sorted(state["removed"]), ["labeled", "mine", "resolved"])
        self.assertEqual((cleanup["cleanup_complete"], sorted(cleanup["foreign_harness_containers"])), (False, ["foreign", "sibling"]))
        self.assertTrue(clean["cleanup_complete"])

    def test_existing_output_is_never_written(self):
        root, frozen, pin = self.plan("existing", ("geth-import",))
        output = root / "out"
        output.mkdir()
        (output / "keep.txt").write_text("keep")
        for _ in range(2):
            with self.assertRaisesRegex(ValueError, "new absolute directory"):
                runner.run(frozen, pin, output)
            self.assertEqual(sorted(item.name for item in output.iterdir()), ["keep.txt"])

    def test_admission_failure_after_creating_the_output_leaves_a_fail_record(self):
        root, frozen, pin = self.plan("partial", ("geth-import",))
        original = runner.canonical_ref
        def fail_admission(path, value):
            if path.name == "admission.json":
                raise OSError("disk full")
            return original(path, value)
        with patch.object(runner, "canonical_ref", fail_admission), self.assertRaisesRegex(OSError, "disk full"):
            runner.run(frozen, pin, root / "out")
        record = json.loads((root / "out" / "run-record.json").read_text())
        self.assertEqual((record["result"], record["children"]), ("FAIL", []))
        self.assertIn("disk full", record["error"])

    def test_stop_group_keeps_runner_signals_blocked_until_the_group_is_gone(self):
        import os, signal, subprocess, sys
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"], start_new_session=True)
        original = runner.stop_group_unmasked
        def signal_first(process):
            os.kill(os.getpid(), signal.SIGTERM)
            original(process)
        previous = signal.signal(signal.SIGTERM, runner.raise_interrupted)
        try:
            with patch.object(runner, "stop_group_unmasked", signal_first), self.assertRaises(runner.Interrupted):
                runner.stop_group(child)
        finally:
            signal.signal(signal.SIGTERM, previous)
            if child.returncode is None:
                child.kill()
                child.wait()
                self.fail("The signal interrupted stop_group before the child was reaped")
        self.assertTrue(runner.group_absent(child.pid))

    def test_signal_during_the_final_record_goes_to_the_caller_handler(self):
        import os, signal
        root, frozen, pin = self.plan("final-signal", ("geth-import",))
        seen = []
        original = runner.canonical_ref
        def signal_while_writing(path, value):
            if path.name == "run-record.json":
                os.kill(os.getpid(), signal.SIGTERM)
            return original(path, value)
        previous = signal.signal(signal.SIGTERM, lambda number, _frame: seen.append(number))
        try:
            with patch.object(runner, "canonical_ref", signal_while_writing):
                record = runner.run(frozen, pin, root / "out")
            handler = signal.getsignal(signal.SIGTERM)
        finally:
            signal.signal(signal.SIGTERM, previous)
        self.assertEqual(record["result"], "PASS")
        self.assertEqual(json.loads((root / "out" / "run-record.json").read_text())["result"], "PASS")
        self.assertEqual(seen, [signal.SIGTERM])
        self.assertIsNot(handler, runner.raise_interrupted)
        runner.single_writer().close()

    def test_teardown_fails_for_kept_or_unknown_containers(self):
        output = self.root / "kept"
        class Result:
            def __init__(self, code, stdout=""): self.returncode, self.stdout = code, stdout
        def check(live, inspectable, removable):
            state = {"live": list(live), "removed": []}
            def fake_run(command, **_):
                if command[:2] == ["docker", "inspect"]:
                    name = command[-1]
                    if name in state["live"] and name in inspectable:
                        return Result(0, json.dumps([{"Source": str(output / name)}]) + "|<no value>")
                    return Result(1)
                if command[:3] == ["docker", "rm", "-f"]:
                    state["removed"] += command[3:]
                    state["live"] = [item for item in state["live"] if item not in command[3:] or item not in removable]
                    return Result(0)
                raise AssertionError(command)
            listings = []
            def docker_state():
                listings.append(list(state["live"]))
                return {"harness_containers": list(state["live"])}
            self.patches[2].stop()
            try:
                with patch.object(runner.subprocess, "run", fake_run), patch.object(runner, "docker_state", docker_state):
                    return runner.teardown(output), state, listings
            finally:
                self.patches[2].start()
        kept, state, _ = check(["a"], {"a"}, set())
        self.assertEqual((kept["cleanup_complete"], kept["remaining_owned_containers"], state["removed"]), (False, ["a"], ["a"]))
        gone, state, _ = check(["a"], {"a"}, {"a"})
        self.assertEqual((gone["cleanup_complete"], gone["forced_container_removal"]), (True, True))
        unknown, _, listings = check(["u"], set(), set())
        self.assertEqual((unknown["cleanup_complete"], unknown["uninspected_containers"]), (False, ["u"]))
        self.assertEqual(len(listings), 2)
        vanished = {"live": ["v"]}
        def vanishing_state():
            current = list(vanished["live"])
            vanished["live"] = []
            return {"harness_containers": current}
        self.patches[2].stop()
        try:
            with patch.object(runner, "inspect", lambda _: None), patch.object(runner, "docker_state", vanishing_state):
                vanished_cleanup = runner.teardown(output)
        finally:
            self.patches[2].start()
        self.assertEqual((vanished_cleanup["cleanup_complete"], vanished_cleanup["uninspected_containers"]), (True, []))

    def test_single_writer_lock_is_private_and_refuses_a_symlink(self):
        import os, stat
        handle = runner.single_writer()
        handle.close()
        self.assertEqual(stat.S_IMODE(os.stat(runner.LOCK).st_mode), 0o600)
        os.unlink(runner.LOCK)
        target = self.root / "lock-target"
        target.write_text("")
        os.symlink(target, runner.LOCK)
        with self.assertRaises(OSError):
            runner.single_writer()

    def test_retry_rules_reject_each_invalid_first_run(self):
        root = self.root / "retry-rules"
        root.mkdir()
        good = {"kind": "local-native-seam-run", "result": "FAIL", "attempt": {"attempt_index": 1}, "resolution_sha256": "a" * 64, "run_id": "r1"}
        def pinned(name, value):
            path = root / (name + ".json")
            path.write_text(json.dumps(value))
            return path, hashlib.sha256(path.read_bytes()).hexdigest()
        path, sha = pinned("good", good)
        self.assertEqual(runner.retry_attempt(path, sha, "diagnostic correction", "a" * 64, time.monotonic() + 30)["attempt_index"], 2)
        for name, change in {"pass": {"result": "PASS"}, "second": {"attempt": {"attempt_index": 2}},
                             "kind": {"kind": "other"}, "plan": {"resolution_sha256": "b" * 64}}.items():
            bad, bad_sha = pinned(name, {**good, **change})
            with self.subTest(case=name), self.assertRaisesRegex(ValueError, "retried once"):
                runner.retry_attempt(bad, bad_sha, "diagnostic correction", "a" * 64, time.monotonic() + 30)
        with self.assertRaisesRegex(ValueError, "without a previous run"):
            runner.retry_attempt(None, None, "diagnostic correction", "a" * 64, time.monotonic() + 30)

    def test_binding_rejects_remaining_single_conditions(self):
        root, frozen, pin = self.plan("bind-more")
        plan = json.loads((frozen / "plan.json").read_text())
        manifests = plan["profile_manifests"]
        changed = copy.deepcopy(plan)
        changed["profile_manifests"][0].update(artifact=manifests[1]["artifact"], profile_manifest_digest=manifests[1]["profile_manifest_digest"])
        with self.assertRaisesRegex(ValueError, "digest or profile"):
            runner.bind_fixtures(changed, None, time.monotonic() + 30)
        changed = copy.deepcopy(plan)
        changed["actions"][0]["count"] = 2
        with self.assertRaisesRegex(ValueError, "one native-import action"):
            runner.bind_fixtures(changed, None, time.monotonic() + 30)
        cases = {
            "fixture version": lambda p: p["fixtures"][0].update(fixture_version="2"),
            "fork variant": lambda p: p["fixtures"][0].update(fork_variant="other"),
            "genesis hash": lambda p: p["fixtures"][0]["identity"].update(genesis_hash="0x" + "9" * 64),
            "second action": lambda p: p["actions"].append({**p["actions"][0], "action_id": "again"}),
            "observe action": lambda p: p["actions"][0].update(kind="observe"),
            "action expected": lambda p: p["actions"][0].update(expected=p["actions"][1]["expected"]),
            "no case": lambda p: p.update(case_identities=[c for c in p["case_identities"] if c["profile"] != p["fixtures"][0]["profile"]]),
            "fence": lambda p: p.update(terminal_fence={"declared": True}),
        }
        for name, mutate in cases.items():
            changed = copy.deepcopy(plan)
            mutate(changed)
            with self.subTest(case=name), self.assertRaises(ValueError):
                runner.bind_fixtures(changed, None, time.monotonic() + 30)

    def test_frozen_plan_reauthenticates_every_artifact(self):
        root, frozen, pin = self.plan("artifacts", ("geth-import",))
        runner.frozen_plan(frozen, pin, time.monotonic() + 30)
        report = json.loads((frozen / "resolution.json").read_text())
        target = Path(next(ref["path"] for ref in report["artifacts"] if ref["path"].endswith("expected.json")))
        target.write_bytes(target.read_bytes() + b" ")
        with self.assertRaises(ValueError):
            runner.frozen_plan(frozen, pin, time.monotonic() + 30)


if __name__ == "__main__": unittest.main()
