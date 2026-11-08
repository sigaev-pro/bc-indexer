"""RunPlan CLI controls use declared synthetic artifacts, never runtime evidence."""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import run_plan


HERE = Path(__file__).resolve().parent
AXES = ("ac_id", "network", "profile", "profile_manifest_digest", "fork_variant", "fixture_kind", "suite", "variant_id")


def write(path, value):
    data = value if isinstance(value, bytes) else (json.dumps(value, indent=2) + "\n").encode()
    path.write_bytes(data)
    return {"path": path.name, "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}


def setup(root):
    inputs = write(root / "inputs.bin", b"independently declared synthetic input\n")
    oracle = write(root / "oracle.txt", b"independent fixed expectation: two native decisions\n")
    preparation = write(root / "preparation.json", {"synthetic": True})
    config = write(root / "chain.json", {"chainId": 15001, "londonBlock": 1})
    build = write(root / "build.json", {"synthetic": True, "not_native_build_evidence": True})
    case = {"schema_version": 3, "ac_id": "AC-CAP-01-01", "network": "ETH", "profile": "ethereum-mainnet",
            "profile_manifest_digest": "1" * 64, "fork_variant": "private-before-activation", "fixture_kind": "native-seam",
            "suite": "quick", "variant_id": "empty-block", "task_id": "E01-01.harness", "fixture_id": "private-two-blocks",
            "fixture_version": "1", "applicability": "APPLICABLE"}
    other = {**case, "variant_id": "transfer"}
    registry = {"schema_version": 3, "version": "synthetic-run-plan-1", "cases": [case, other]}
    bundle = {"contract": "evidence-artifacts-v1", "kind": "CaseRegistry", "document": registry,
              "context": {"task_ids": ["E01-01.harness"], "case_owners": {"AC-CAP-01-01": "E01-01.harness"}, "rules": {}}, "synthetic": True}
    bundle_ref = write(root / "registry.json", bundle)
    identity = {key: case[key] for key in AXES}
    plan = {"schema_version": 1, "plan_id": "private-plan-1", "fixture_kind": "native-seam", "suite": "quick",
            "mode": "standard", "coverage": "PARTIAL", "seed": 7, "profiles": ["ethereum-mainnet"],
            "case_identities": [identity], "build_manifest": build, "profile_manifests": [],
            "fixtures": [{"fixture_id": "private-two-blocks", "fixture_version": "1", "profile": "ethereum-mainnet",
                          "fork_variant": "private-before-activation", "identity": {"kind": "test-only", "chain_id": "15001",
                          "genesis_hash": "0x" + "2" * 64, "native_config": config}, "inputs": [inputs], "oracle": oracle,
                          "preparation": preparation, "public_correspondence": None}],
            "resources": {"architecture": "amd64", "cpus": 2, "ram_bytes": 1073741824, "disk_bytes": 1073741824, "parallelism": 1},
            "volumes": {"blocks": 2, "transactions": 1, "operations": 2, "payload_bytes": 1024, "record_bytes": 1024,
                        "outage_seconds": 0, "soak_seconds": 0},
            "deadlines": {"preparation_seconds": 900, "startup_seconds": 60, "event_seconds": 60,
                          "measured_seconds": 1800, "comparison_seconds": 60, "teardown_seconds": 60},
            "sources": [{"profile": "ethereum-mainnet", "fixture_id": "private-two-blocks", "fixture_version": "1",
                         "fork_variant": "private-before-activation", "start": {"kind": "prepared-genesis", "height": 0, "block_hash": "0x" + "2" * 64}}],
            "stop": {"operator": "AND", "duration_seconds": None, "native_decisions": 2, "driver_actions": 2,
                     "require_action_outcomes": True},
            "actions": [{"action_id": "import-two", "profile": "ethereum-mainnet", "kind": "native-import", "count": 2,
                         "input": inputs, "expected": oracle, "outcome": "applied", "fault_id": None}],
            "quotas": [{"case_identity": identity, "minimum": 1}], "faults": [], "terminal_fence": None}
    write(root / "plan.json", plan)
    return plan, bundle, bundle_ref["sha256"]


def retarget(plan, bundle, profile, kind, suite):
    family = "BSC" if profile.startswith("bsc-") else "POLY" if profile.startswith("polygon-") else "ETH"
    def change(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "profile": value[key] = profile
                elif key == "network": value[key] = family
                elif key == "fixture_kind": value[key] = kind
                elif key == "suite": value[key] = suite
                else: change(item)
        elif isinstance(value, list):
            for item in value: change(item)
    change(plan)
    change(bundle)
    plan["profiles"] = [profile]
    if kind == "public":
        plan["fixtures"][0]["identity"]["kind"] = "public"
        plan["sources"][0]["start"].update(kind="synced-head", height=123)
        plan["stop"].update(operator="OR", duration_seconds=60, native_decisions=2, driver_actions=None)


class RunPlanTests(unittest.TestCase):
    def test_schema_external_references_resolve_to_existing_local_contracts(self):
        schema = json.loads((HERE / "run-plan.schema.json").read_text())
        def check(value):
            if isinstance(value, dict):
                if "$ref" in value:
                    target = value["$ref"].partition("#")[0]
                    if target:
                        self.assertNotIn("://", target)
                        self.assertTrue((HERE / target).is_file(), target)
                for item in value.values(): check(item)
            elif isinstance(value, list):
                for item in value: check(item)
        check(schema)

    def call(self, root, pin, mode="check", output=None):
        args = [sys.executable, "-I", str(HERE / "run_plan.py"), mode, str(root / "plan.json"),
                "--registry-bundle", str(root / "registry.json"), "--registry-sha256", pin]
        if output is not None:
            args += ["--output", str(output)]
        result = subprocess.run(args, capture_output=True, text=True, timeout=15)
        return result, json.loads(result.stdout) if result.stdout else None

    def test_freeze_preserves_partial_selection_and_original_bytes_without_runtime_credit(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            _, _, pin = setup(root)
            raw = (root / "plan.json").read_bytes()
            result, report = self.call(root, pin, "freeze", root / "frozen")
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertTrue(report["plan_valid"])
            self.assertFalse(report["runtime_admitted"])
            self.assertEqual(report["coverage"]["selected"], 1)
            self.assertEqual(report["coverage"]["omitted_applicable"], 1)
            self.assertEqual([row["execution"] for row in report["cases"]], ["NOT_RUN", "NOT_RUN"])
            self.assertIn("profile_manifest_verification_unimplemented", report["blockers"])
            self.assertNotIn("terminal_fence_missing", report["blockers"])
            self.assertEqual((root / "frozen/plan.json").read_bytes(), raw)
            self.assertEqual((root / "frozen/registry.json").read_bytes(), (root / "registry.json").read_bytes())

    def test_parent_budget_and_finite_stop_cannot_be_relaxed(self):
        mutations = [
            lambda p: p["deadlines"].update(measured_seconds=1801),
            lambda p: p["deadlines"].update(comparison_seconds=1801),
            lambda p: p["deadlines"].update(event_seconds=1801),
            lambda p: p["deadlines"].update(startup_seconds=901),
            lambda p: p.update(mode="extended"),
            lambda p: p["stop"].update(operator="OR"),
            lambda p: p["stop"].update(native_decisions=None),
            lambda p: p["stop"].update(driver_actions=1),
            lambda p: p["stop"].update(duration_seconds=1801),
            lambda p: p["quotas"].append(copy.deepcopy(p["quotas"][0])),
            lambda p: p["volumes"].update(outage_seconds=1801),
        ]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            original, _, pin = setup(root)
            for number, mutation in enumerate(mutations):
                with self.subTest(number=number):
                    plan = copy.deepcopy(original)
                    mutation(plan)
                    write(root / "plan.json", plan)
                    result, report = self.call(root, pin)
                    self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                    self.assertFalse(report["runtime_admitted"])

    def test_public_mainnet_is_passive_and_testnet_requires_bound_outcomes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            plan, bundle, _ = setup(root)
            retarget(plan, bundle, "ethereum-mainnet", "public", "public-smoke")
            action = plan["actions"][0]
            action.update(kind="observe", count=1, input=None, outcome="observed")
            pin = write(root / "registry.json", bundle)["sha256"]
            write(root / "plan.json", plan)
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertIn("terminal_fence_missing", report["blockers"])
            self.assertFalse(report["runtime_admitted"])
            action.update(kind="controlled-transaction", input=plan["fixtures"][0]["inputs"][0], outcome="included-success", signer_chain_id="15001")
            write(root / "plan.json", plan)
            result, _ = self.call(root, pin)
            self.assertEqual(result.returncode, 2)
            retarget(plan, bundle, "ethereum-sepolia", "public", "public-smoke")
            pin = write(root / "registry.json", bundle)["sha256"]
            write(root / "plan.json", plan)
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertFalse(report["runtime_admitted"])
            for mutation in (lambda p: p.update(actions=[]),
                             lambda p: p["actions"][0].update(signer_chain_id="1"),
                             lambda p: p["actions"][0].update(outcome="observed"),
                             lambda p: p["fixtures"][0]["identity"].update(kind="test-only"),
                             lambda p: p["stop"].update(duration_seconds=None, native_decisions=None)):
                changed = copy.deepcopy(plan)
                mutation(changed)
                write(root / "plan.json", changed)
                result, _ = self.call(root, pin)
                self.assertEqual(result.returncode, 2, result.stdout)

    def test_exact_fence_declaration_is_required_but_never_treated_as_verified(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            plan, bundle, _ = setup(root)
            retarget(plan, bundle, "ethereum-mainnet", "local-e2e", "quick")
            pin = write(root / "registry.json", bundle)["sha256"]
            implementation = write(root / "fence.py", b"unverified synthetic fence declaration\n")
            proof = write(root / "fence-proof.json", {"claimed": "not trusted by this validator"})
            topology = {"kind": "standalone", "replicas": 1}
            digest = hashlib.sha256((json.dumps(topology, sort_keys=True, separators=(",", ":")) + "\n").encode()).hexdigest()
            plan["terminal_fence"] = {"implementation": implementation, "source_commit": "a" * 40, "build": plan["build_manifest"],
                                      "configuration": plan["fixtures"][0]["identity"]["native_config"], "topology": topology,
                                      "prior_conformance": {"artifact": proof, "implementation_sha256": implementation["sha256"],
                                      "source_commit": "a" * 40, "build_sha256": plan["build_manifest"]["sha256"],
                                      "configuration_sha256": plan["fixtures"][0]["identity"]["native_config"]["sha256"], "topology_digest": digest}}
            write(root / "plan.json", plan)
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertIn("terminal_fence_conformance_unverified", report["blockers"])
            self.assertFalse(report["runtime_admitted"])
            for key in ("implementation_sha256", "source_commit", "build_sha256", "configuration_sha256", "topology_digest"):
                changed = copy.deepcopy(plan)
                changed["terminal_fence"]["prior_conformance"][key] = "0" * (40 if key == "source_commit" else 64)
                write(root / "plan.json", changed)
                result, _ = self.call(root, pin)
                self.assertEqual(result.returncode, 2, result.stdout)

    def test_case_registry_fixture_and_coverage_bindings_are_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            original, bundle, pin = setup(root)
            mutations = [lambda p: p["case_identities"].append(copy.deepcopy(p["case_identities"][0])),
                         lambda p: p["case_identities"][0].update(variant_id="unknown"),
                         lambda p: p["case_identities"][0].update(profile_manifest_digest="0" * 64),
                         lambda p: p.update(coverage="COMPLETE"),
                         lambda p: p["fixtures"][0].update(fork_variant="future-disabled-fork"),
                         lambda p: p["fixtures"][0].update(fixture_version="2"),
                         lambda p: p["fixtures"].append(copy.deepcopy(p["fixtures"][0])),
                         lambda p: p["profiles"].append("unknown-network"),
                         lambda p: p["sources"][0]["start"].update(block_hash="0x" + "3" * 64),
                         lambda p: p.update(exclusions=["missing-case"]),
                         lambda p: p.update(schema_version=True)]
            for number, mutation in enumerate(mutations):
                with self.subTest(number=number):
                    plan = copy.deepcopy(original)
                    mutation(plan)
                    write(root / "plan.json", plan)
                    result, report = self.call(root, pin, "freeze", root / "rejected-output")
                    self.assertEqual(result.returncode, 2, result.stdout)
                    self.assertFalse(report["runtime_admitted"])
                    self.assertFalse((root / "rejected-output").exists())
            complete = copy.deepcopy(original)
            other = {key: bundle["document"]["cases"][1][key] for key in AXES}
            complete["case_identities"].append(other)
            complete["quotas"].append({"case_identity": other, "minimum": 1})
            complete["coverage"] = "COMPLETE"
            write(root / "plan.json", complete)
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertEqual(report["coverage"]["omitted_applicable"], 0)
            self.assertFalse(report["runtime_admitted"])
            bundle["document"]["cases"][1].update(applicability="DEFERRED", reason="not implemented", rule_ref="unapproved")
            pin = write(root / "registry.json", bundle)["sha256"]
            write(root / "plan.json", original)
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 2, result.stdout)
            self.assertIn("accepted applicability rule", report["error"])

    def test_real_file_hash_path_and_json_mutations_reject_before_freeze(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            plan, bundle, pin = setup(root)
            original_raw = (root / "plan.json").read_bytes()
            original_input = (root / "inputs.bin").read_bytes()
            for raw in (original_raw.replace(b'"schema_version": 1', b'"schema_version": 1, "schema_version": 1', 1),
                        original_raw.replace(b'"seed": 7', b'"seed": 1e-400'),
                        original_raw.replace(b'"seed": 7', b'"seed": NaN'),
                        original_raw.replace(b'"seed": 7', b'"seed": 7.0000000000000001')):
                (root / "plan.json").write_bytes(raw)
                result, _ = self.call(root, pin, "freeze", root / "bad")
                self.assertEqual(result.returncode, 2, result.stdout)
                self.assertFalse((root / "bad").exists())
            write(root / "plan.json", plan)
            (root / "inputs.bin").write_bytes(original_input + b"changed")
            result, _ = self.call(root, pin, "freeze", root / "bad")
            self.assertEqual(result.returncode, 2)
            self.assertFalse((root / "bad").exists())
            (root / "inputs.bin").write_bytes(original_input)
            for name in ("../inputs.bin", "symlink-input"):
                changed = copy.deepcopy(plan)
                changed["fixtures"][0]["inputs"][0]["path"] = name
                if name == "symlink-input": (root / name).symlink_to(root / "inputs.bin")
                write(root / "plan.json", changed)
                result, _ = self.call(root, pin)
                self.assertEqual(result.returncode, 2, result.stdout)
            write(root / "plan.json", plan)
            (root / "registry.json").write_bytes((root / "registry.json").read_bytes() + b" ")
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 2)
            self.assertIn("external trusted pin", report["error"])
            self.assertEqual((root / "inputs.bin").read_bytes(), original_input)

    def test_freeze_revalidates_authority_before_emitting_resolution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            _, _, pin = setup(root)
            raw = (root / "plan.json").read_bytes()
            output = root / "frozen"
            original_open = Path.open
            def replacing(path, *args, **kwargs):
                if path == output / "registry.json":
                    with original_open(root / "oracle.txt", "wb") as stream:
                        stream.write(b"changed after plan capture")
                return original_open(path, *args, **kwargs)
            with patch.object(Path, "open", new=replacing), self.assertRaises(ValueError):
                run_plan.resolve(root / "plan.json", root / "registry.json", pin, output)
            self.assertFalse((output / "resolution.json").exists())
            self.assertEqual((output / "plan.json").read_bytes(), raw)

    def test_explicit_extended_soak_can_exceed_standard_cap_but_stays_unrun(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            plan, bundle, _ = setup(root)
            retarget(plan, bundle, "ethereum-mainnet", "native-seam", "soak")
            plan.update(mode="extended")
            plan["deadlines"]["measured_seconds"] = 3600
            plan["volumes"]["soak_seconds"] = 3500
            pin = write(root / "registry.json", bundle)["sha256"]
            write(root / "plan.json", plan)
            result, report = self.call(root, pin)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertFalse(report["runtime_admitted"])
            self.assertTrue(all(row["execution"] == "NOT_RUN" for row in report["cases"]))


if __name__ == "__main__":
    unittest.main()
