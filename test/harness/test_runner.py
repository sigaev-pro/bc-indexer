"""Portable controls for the local native-seam runner; fake slices replace native processes."""

import copy
import hashlib
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
if behavior == "SLEEP":
    time.sleep(60)
os.mkdir(output)
data = open(request, "rb").read()
proof = {{"kind": "{kind}", "result": "FAIL" if behavior == "FAIL" else "PASS", "cleanup_complete": behavior != "DIRTY",
         "request": {{"path": request, "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}}}}
open(os.path.join(output, "proof.json"), "w").write(json.dumps(proof))
print(json.dumps({{"result": proof["result"]}}))
sys.exit(0 if proof["result"] == "PASS" else 2)
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
                "variant_id": "transfer-then-empty", "task_id": "E01-01.harness", "fixture_id": manifest["fixture"]["fixture_id"],
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
              "context": {"task_ids": ["E01-01.harness"], "case_owners": {"AC-CAP-01-01": "E01-01.harness"}, "rules": {}}}
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
                        patch.object(runner, "teardown", lambda: {"forced_container_removal": False,
                                                                  "remaining_harness_containers": [], "cleanup_complete": True})]
        for item in self.patches: item.start()

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
            record = runner.run_child(row, root / "out" / "child", time.monotonic() + 2)
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
        report = resign({**original, "runtime_admitted": True})
        with self.assertRaisesRegex(ValueError, "Not a valid offline"):
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
            runner.run_child(admitted["fixtures"][0], root / "late", time.monotonic() - 1)

    def test_cli_rejects_retry_metadata_for_admission(self):
        root, frozen, pin, plan = self.frozen("cli")
        argv = ["runner.py", "admit", str(frozen), "--resolution-sha256", pin, "--output", str(root / "out"),
                "--previous-run", str(root / "x"), "--previous-sha256", "0" * 64, "--retry-reason", "diagnostic retry"]
        with patch.object(runner.sys, "argv", argv), patch("builtins.print") as printed:
            self.assertEqual(runner.main(), 2)
        self.assertIn("Only run accepts retry metadata", printed.call_args[0][0])
        self.assertFalse((root / "out").exists())


if __name__ == "__main__": unittest.main()
