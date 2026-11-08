"""Fabricated transport proofs test adapter plumbing only; no native work runs."""

import copy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import build_contract as contract
import build_execution
import build_resources
import build_task_verifier as verifier
import build_vectors
import controller
from controller_backend import RealBackend
import evidence
import task_proofs
import test_initial_task
from test_acceptance import BASE, HEAD


SOURCE = Path(__file__).resolve().parents[2]


def transport_proof():
    """No process result here is real. Only the fake publication grants test authority."""
    recipes = json.loads((SOURCE / ".project/automation/task-recipes.json").read_text())
    recipe = task_proofs.snapshot(recipes, SOURCE)[verifier.TASK]
    files = {name: "a" * 64 for name in recipe["required_files"]}
    with tempfile.TemporaryDirectory() as temporary:
        manifest = build_vectors.fixture(temporary, SOURCE)
    manifest["evidence_kind"] = "actual-build"
    manifest["identity"].update(head_sha=HEAD, base_sha=BASE, verifier_source_commit=BASE, operation_id="verify-check-2")
    spec = {"schema_version": 1, "task_id": verifier.TASK, "run_id": manifest["identity"]["run_id"],
            "operation_id": "verify-check-2", "nonce": manifest["identity"]["nonce"],
            "images": [row["reference"] for row in manifest["plan"]["images"].values()]}
    log = copy.deepcopy(manifest["commands"][0]["stdout"])
    commands = []
    for name in contract.NATIVE:
        for field, size in (("binary", 128), ("oci_layer", 4096)):
            manifest["builds"][name][field] = {"path": "synthetic/" + name + "/" + field,
                "sha256": hashlib.sha256(("synthetic-only-" + name + field).encode()).hexdigest(), "size_bytes": size}
        graph = [{"Path": "github.com/cosmos/cosmos-sdk", "Version": "v0.1.0"},
                 {"Path": "github.com/cometbft/cometbft", "Version": "v0.1.0"}]
        args = build_execution.build_arguments(name, manifest["plan"]["sources"][name], 946684800, graph, 4)
        commands.append({"argv": ["docker", "container", "exec", "f" * 64, *args], "env": {}, "exit_code": 0,
                         "started_at": 1, "finished_at": 2, "stdout": log, "stderr": log})
    manifest["commands"] = commands
    manifest["profile_bindings"] = build_vectors.bindings(manifest["plan"], manifest["builds"])
    resources, intents = [], []
    for step in [*("go-" + name for name in contract.MODULES), *("runtime-" + name for name in contract.NATIVE)]:
        identifier = hashlib.sha256(("synthetic-owned-" + step).encode()).hexdigest()
        image = manifest["image_inputs"]["golang-build-image" if step.startswith("go-") else "debian-runtime-image"]
        intents.append({"name": "extractor-" + spec["nonce"][:24] + "-" + step,
                        "image_reference": image["reference"], "container_id": identifier})
        resources.append({"id": identifier, "image_id": "sha256:" + image["config"]["sha256"], "image_reference": image["reference"],
                          "labels": build_resources.labels(spec), "running": False, "remove_exit": 0})
    cleanup = {"schema_version": 1, "nonce": spec["nonce"], "complete": True, "resources": resources, "remaining": [],
               "creation_inventory": {"schema_version": 1, "nonce": spec["nonce"], "intents": intents}, "started_at": 1, "finished_at": 2}
    authentication = {"schema_version": 1, "synthetic": False, "verified": True, "bindings": [], "check_report": copy.deepcopy(log)}
    for pin in contract.inputs(SOURCE)[1]["package_indexes"]:
        row = {"index_url": pin["url"], "index_sha256": log["sha256"], "index_size": log["size_bytes"],
               "inrelease_url": pin["url"].split("/main/", 1)[0] + "/InRelease", "command_index": len(commands),
               **{key: copy.deepcopy(log) for key in ("inrelease", "release", "packages", "keyring", "verifier")}}
        commands.append({"argv": ["docker", "container", "exec", "f" * 64, "/usr/bin/sqv",
                       "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp", "--output=/proof/" + log["path"],
                       "--cleartext", "/proof/" + log["path"]], "env": {}, "exit_code": 0,
                       "started_at": 1, "finished_at": 2, "stdout": log, "stderr": log})
        authentication["bindings"].append(row)
    manifest["updater"]["signature_proof"] = {"path": "synthetic-signatures.json", "sha256": contract.digest(authentication),
                                               "size_bytes": len(contract.canonical(authentication))}
    report = {"contract": verifier.CONTRACT, "task_id": verifier.TASK, "scope": "isolated-native-builds", "runtime": "NOT_RUN",
              "evidence_kind": "actual-build", "control_inputs": "synthetic-protocol-only", "identity": manifest["identity"],
              "platform_results": {"linux/amd64": "BUILT", "linux/arm64": "NOT_RUN"}, "trusted_pack": recipe["trusted_pack"],
              "inputs": {"registry_json": (SOURCE / ".project/bootstrap/components/registry.json").read_text(),
                         "lock_json": (SOURCE / ".project/bootstrap/components/candidate-lock.json").read_text()},
              "outputs": {name: files[name] for name in verifier.OUTPUTS}, "outcomes": [{"id": name, "expected_exit": code, "actual_exit": code,
                    "stdout_sha256": "a" * 64, "stderr_sha256": "b" * 64,
                    "process_group": 123, "cleanup_confirmed": True} for name, code in verifier.expected_outcomes().items()],
              "manifest": manifest, "manifest_sha256": contract.digest(manifest),
              "manifest_ref": {"path": "manifest.json", "sha256": contract.digest(manifest), "size_bytes": len(contract.canonical(manifest))},
              "materialized_root": "/synthetic/verify-check-2/build-artifacts", "cleanup": {"spec": spec, "history": [cleanup]},
              "updater_authentication": authentication}
    checks = []
    argv = recipe["commands"] + [[sys.executable, "-I", str(SOURCE / ".project/automation/build_task_verifier.py"), "--candidate", "/synthetic-candidate", "--request", "/synthetic/verify-check-2/request.json"]]
    for number, command in enumerate(argv):
        stdout = json.dumps(report) if number == 2 else "synthetic transport log\n"
        request = {"operation_id": "verify-check-" + str(number), "command": command, "cwd": "/synthetic-candidate",
                   "deadline": 1900000000, "timeout_seconds": 7200 if number == 2 else 600,
                   "env": {"BOOTSTRAP_HEAD_SHA": HEAD, "BOOTSTRAP_BASE_SHA": BASE}, "stdin_sha256": hashlib.sha256(b"").hexdigest()}
        result = {"operation_id": request["operation_id"], "reason": "completed", "exit_code": 0, "cleanup_complete": True,
                  "stdout_sha256": hashlib.sha256(stdout.encode()).hexdigest(), "stderr_sha256": hashlib.sha256(b"").hexdigest()}
        if number == 2:
            request["owned_docker"] = spec
            result["docker_cleanup"] = cleanup
        result["request_sha256"] = evidence.digest(request)
        checks.append({"request": request, "execution": result, "stdout": stdout, "stderr": ""})
    return {"passed": True, "task_id": verifier.TASK, "head_sha": HEAD, "base_sha": BASE, "runtime": "NOT_RUN",
            "recipe": recipe, "recipe_sha256": evidence.digest(recipe), "verifier_source_commit": BASE,
            "authorization": dict(controller.AUTHORIZATIONS["D93"]),
            "verification_operation_id": "verify", "files": files, "checks": checks}


class BuildAdapterTests(unittest.TestCase):
    def test_closed_recipe_and_unknown_task_boundary(self):
        proof = transport_proof()
        task_proofs.check_recipe(proof["recipe"], SOURCE, verifier.TASK)
        backend = RealBackend.__new__(RealBackend)
        backend.source = SOURCE
        self.assertTrue(backend.eligible_recipe(verifier.TASK, {verifier.TASK: proof["recipe"]}))
        self.assertFalse(backend.eligible_recipe("E01-01.harness", {"E01-01.harness": proof["recipe"]}))
        for key in ("commands", "trusted_pack", "allowed_paths"):
            changed = copy.deepcopy(proof["recipe"])
            changed[key] = {} if key == "trusted_pack" else []
            with self.subTest(field=key), self.assertRaises(ValueError):
                task_proofs.check_recipe(changed, SOURCE, verifier.TASK)

    def test_fabricated_transport_proof_roundtrip_is_only_a_parser_test(self):
        proof = transport_proof()
        self.assertEqual(task_proofs.check_verification(proof, HEAD, BASE), proof)
        self.assertEqual(task_proofs.publication(proof)["scope"], "isolated-native-builds")

    def test_finite_build_deadlines_and_exact_authority_are_required(self):
        proof = transport_proof()
        proof["authorization"] = dict(controller.AUTHORIZATIONS["D92"])
        for row in proof["checks"]:
            row["request"]["deadline"] = proof["authorization"]["hard_deadline"]
            row["execution"]["request_sha256"] = evidence.digest(row["request"])
        self.assertEqual(task_proofs.check_verification(proof, HEAD, BASE), proof)
        for mode in ("unknown-policy", "infinite-deadline", "missing-deadline", "negative-deadline", "unbounded-timeout", "expired-d92"):
            changed = copy.deepcopy(proof if mode == "expired-d92" else transport_proof())
            if mode == "unknown-policy": changed["authorization"]["reference"] = "UNREVIEWED"
            request = changed["checks"][0]["request"]
            if mode == "infinite-deadline": request["deadline"] = float("inf")
            if mode == "missing-deadline": request["deadline"] = None
            if mode == "negative-deadline": request["deadline"] = -1
            if mode == "unbounded-timeout": request["timeout_seconds"] = 7201
            if mode == "expired-d92": request["deadline"] += 1
            if mode != "infinite-deadline":
                changed["checks"][0]["execution"]["request_sha256"] = evidence.digest(request)
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                task_proofs.check_verification(changed, HEAD, BASE)

    def test_stale_task_head_base_source_outputs_and_cleanup_are_rejected(self):
        mutations = [lambda p: p.update(task_id="E01-01.evidence"), lambda p: p.update(head_sha="f" * 40),
                     lambda p: p.update(base_sha="f" * 40), lambda p: p.update(verifier_source_commit="f" * 40),
                     lambda p: p["files"].update({"build/native/plan.py": "f" * 64}),
                     lambda p: p["checks"][-1]["execution"]["docker_cleanup"].update(complete=False),
                     lambda p: p["checks"][-1]["execution"]["docker_cleanup"]["creation_inventory"]["intents"].pop()]
        for mutation in mutations:
            proof = transport_proof()
            mutation(proof)
            with self.subTest(mutation=mutation), self.assertRaises((ValueError, KeyError, TypeError)):
                task_proofs.check_verification(proof, HEAD, BASE)

    def test_malformed_or_partial_report_cannot_be_rehashed_into_acceptance(self):
        for mutation in (lambda r: r["manifest"]["builds"].pop("bor"), lambda r: r["manifest"]["module_closures"].pop("grpc-go"),
                         lambda r: r["manifest"]["os_closures"].pop("golang-build-image"), lambda r: r["outcomes"].pop(),
                         lambda r: r["outcomes"][0].update(cleanup_confirmed=False),
                         lambda r: r.update(evidence_kind="synthetic-fixture"), lambda r: r["manifest"].update(schema_version=True),
                         lambda r: r["manifest"]["builds"]["geth"].update(binary={"path": "../fake", "size_bytes": 128, "sha256": "f" * 64}),
                         lambda r: r["manifest"]["profile_bindings"][0].update(native_component="bsc"),
                         lambda r: r["updater_authentication"]["bindings"][0].update(command_index=0),
                         lambda r: r["updater_authentication"]["bindings"][0].update(inrelease_url="https://example.org/InRelease"),
                         lambda r: r["updater_authentication"]["bindings"][0].update(unchecked=True)):
            proof = transport_proof()
            row = proof["checks"][-1]
            report = json.loads(row["stdout"])
            mutation(report)
            report["manifest_sha256"] = report["manifest_ref"]["sha256"] = contract.digest(report["manifest"])
            report["manifest_ref"]["size_bytes"] = len(contract.canonical(report["manifest"]))
            row["stdout"] = json.dumps(report)
            row["execution"]["stdout_sha256"] = hashlib.sha256(row["stdout"].encode()).hexdigest()
            with self.subTest(mutation=mutation), self.assertRaises((ValueError, KeyError, TypeError)):
                task_proofs.check_verification(proof, HEAD, BASE)

    def test_bound_build_receipt_and_publication_use_existing_acceptance_path(self):
        helper = test_initial_task.ArtifactReceiptTests()
        with patch.object(test_initial_task, "verifier", verifier), patch.object(test_initial_task, "proof_fixture", transport_proof):
            helper.setUp()
        self.addCleanup(helper.doCleanups)
        receipt = helper.ledger.save(helper.receipt())
        self.assertEqual(helper.ledger.load(verifier.TASK), receipt)
        comment = helper.rows["issues/comments/" + str(helper.snapshot["review_comment_id"])]
        comment["body"] += "\nChanged public binding."
        with self.assertRaises(controller.Blocked):
            helper.ledger.load(verifier.TASK)

    def test_existing_evidence_receipt_protocol_is_unchanged(self):
        proof = test_initial_task.proof_fixture()
        self.assertEqual(task_proofs.check_verification(proof, HEAD, BASE), proof)
        self.assertEqual(task_proofs.publication(proof)["scope"], "artifact-conformance")


if __name__ == "__main__":
    unittest.main()
