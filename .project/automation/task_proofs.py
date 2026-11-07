"""Bind initial-task verification to frozen inputs and the coordinator's public report."""

import copy
import hashlib
import json
from pathlib import Path
import re

import acceptance
import evidence
import evidence_task_verifier as verifier


def snapshot(recipes, source):
    result = copy.deepcopy(recipes)
    if verifier.TASK in result:
        result[verifier.TASK]["trusted_pack"] = verifier.pack(source)
    return result


def check_recipe(recipe, source):
    declared = acceptance.parse_json((Path(source) / ".project/automation/task-recipes.json").read_text())[verifier.TASK]
    acceptance.require(recipe == dict(declared, trusted_pack=verifier.pack(source)),
                       "trusted recipe or verifier bytes changed; resume cannot adopt a new snapshot")
    acceptance.require(recipe["artifact_contract"] == verifier.CONTRACT, "unsupported artifact contract")
    acceptance.require(recipe["role"] == "qa"
                       and set(recipe["allowed_paths"]) == {"test/evidence/", ".project/evidence/E01-01.evidence.md", ".project/PROJECT_STATUS.md"}
                       and set(recipe["required_files"]) == set(verifier.OUTPUTS) | {".project/evidence/E01-01.evidence.md"}
                       and recipe["commands"] == [["python3", ".project/automation/ci.py"],
                                                  ["python3", "-I", "-m", "unittest", "discover", "-s", "test/evidence", "-p", "test_*.py"]],
                       "initial task recipe does not match its exact trusted output and path contract")


def check_verification(proof, head, base):
    acceptance.require(proof["task_id"] == verifier.TASK and proof["head_sha"] == head and proof["base_sha"] == base
                       and proof["passed"] is True and proof["runtime"] == "NOT_RUN", "artifact proof identity differs")
    recipe = proof["recipe"]
    acceptance.require(proof["recipe_sha256"] == evidence.digest(recipe)
                       and recipe["artifact_contract"] == verifier.CONTRACT
                       and set(recipe["trusted_pack"]) == set(verifier.PACK_FILES)
                       and all(re.fullmatch(r"[a-f0-9]{64}", value) for value in recipe["trusted_pack"].values()),
                       "artifact proof recipe or trusted pack is invalid")
    acceptance.require(re.fullmatch(acceptance.SHA_PATTERN, proof["verifier_source_commit"])
                       and proof["authorization"]["reference"] != "D86"
                       and proof["authorization"]["stage_limit"] in {"E01", "E02", "E03", "E04", "E05", "E06", "E07"},
                       "artifact proof lacks a product launch and exact verifier source")
    acceptance.require(set(proof["files"]) == set(recipe["required_files"])
                       and set(verifier.OUTPUTS) <= set(proof["files"])
                       and all(re.fullmatch(r"[a-f0-9]{64}", value) for value in proof["files"].values()), "artifact proof output set differs")
    checks = proof["checks"]
    acceptance.require(len(checks) == len(recipe["commands"]) + 1, "artifact proof is missing required executions")
    for number, row in enumerate(checks):
        request, execution = row["request"], row["execution"]
        acceptance.require(request["operation_id"] == execution["operation_id"]
                           == proof["verification_operation_id"] + "-check-" + str(number)
                           and execution["request_sha256"] == evidence.digest(request)
                           and execution["reason"] == "completed" and execution["exit_code"] == 0
                           and execution.get("cleanup_complete") is True,
                           "artifact proof has an incomplete or mismatched process result")
        acceptance.require(request["env"] == {"BOOTSTRAP_HEAD_SHA": head, "BOOTSTRAP_BASE_SHA": base}
                           and request["stdin_sha256"] == hashlib.sha256(b"").hexdigest(), "artifact execution context differs")
        for field in ("stdout", "stderr"):
            acceptance.require(hashlib.sha256(row[field].encode()).hexdigest() == execution[field + "_sha256"],
                               "retained artifact log differs from its execution hash")
        if number < len(recipe["commands"]):
            acceptance.require(request["command"] == recipe["commands"][number], "artifact prerequisite command differs")
        else:
            command = request["command"]
            acceptance.require(len(command) == 5 and command[1] == "-I"
                               and command[2].endswith("/.project/automation/evidence_task_verifier.py")
                               and command[3] == "--candidate" and command[4] == request["cwd"], "trusted verifier command differs")
            report = acceptance.parse_json(row["stdout"])
            verifier.validate_report(report)
            acceptance.require(report["outputs"] == {name: proof["files"][name] for name in verifier.OUTPUTS},
                               "trusted vector report describes different output bytes")
    return proof


def publication(proof):
    check_verification(proof, proof["head_sha"], proof["base_sha"])
    return {"schema_version": 1, "producer": "controller", "scope": "artifact-conformance",
            "task_id": verifier.TASK, "head_sha": proof["head_sha"], "base_sha": proof["base_sha"],
            "proof_sha256": evidence.digest(proof), "timing": "after-independent-review",
            "reviewer_attestation": False, "runtime": "NOT_RUN"}


def verify_public_binding(receipt, read):
    """A local digest alone is not authority; check the actual coordinator report."""
    expected = receipt["capture"]["expected"]
    proof = check_verification(receipt["task_verification"], expected["head_sha"], expected["base_sha"])
    acceptance.require(proof == receipt["capture"]["snapshot"]["task_verification"],
                       "receipt task proof differs from the retained pre-merge snapshot")
    saved = receipt["capture"]["snapshot"]["review_comment"]
    public = read(f"issues/comments/{expected['review_comment_id']}")
    acceptance.require(public["user"]["login"] == expected["coordinator_login"] == "sigaev-pro"
                       and all(public[key] == saved[key] for key in ("id", "body", "issue_url", "html_url")),
                       "artifact proof is not bound to the actual coordinator publication")
    match = re.fullmatch(re.escape(acceptance.REVIEW_MARKER) + r"\s*```json\n(.*)\n```", public["body"].strip(), re.S)
    acceptance.require(match is not None, "coordinator publication is not structured")
    report = acceptance.parse_json(match.group(1))
    acceptance.require(report.get("controller_verification") == publication(proof),
                       "local artifact proof differs from the authenticated public binding")
    comparison = read(f"compare/{proof['verifier_source_commit']}...{expected['base_sha']}")
    acceptance.require(comparison["status"] in {"identical", "ahead"}
                       and comparison["merge_base_commit"]["sha"] == proof["verifier_source_commit"],
                       "trusted verifier source was not accepted before the candidate base")
    return proof
