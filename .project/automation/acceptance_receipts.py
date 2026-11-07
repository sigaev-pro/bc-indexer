"""Verify merged receipts and the four explicitly pinned bootstrap exceptions."""

import hashlib
from pathlib import Path

import acceptance
import evidence
from controller import Blocked, read_json, require
import evidence_task_verifier as artifact_verifier
import task_proofs

HISTORICAL = {
    "BOOT-01": {"issue": 1, "head": "145f91e5c8cb7b855fc70db59a9decb065b41842",
                "merge": "145f91e5c8cb7b855fc70db59a9decb065b41842", "pr": None,
                "comments": [(6691486322, 1, "5b84d05035efa5a1e0776100c0ad3a935ebe68b9d5910ff885bcad50dbee301b")]},
    "BOOT-02": {"issue": 2, "head": "4205b97647abdfd71dd8228d11b65c2a36922a45",
                "merge": "6b8e1dd561d8be39f80b00ef5860d35b7db95d24", "pr": 85,
                "comments": [(6692237142, 85, "ba6aabfbe605d04defcaf397baa37c1dc4aa7f2ec312d46c5c9157c2e8b16fcb")]},
    "BOOT-04": {"issue": 3, "head": "071df3ea5549364c594829e219426adecdf69131",
                "merge": "1ab566cf402a84661729502685be284afb38ba73", "pr": 86,
                "comments": [(6692503421, 86, "04701f2ad490fcfd42fcec1b704fbc0c0b644bdfe3a8d0b8e4e2d4ae411e73c0")]},
    "BOOT-05": {"issue": 4, "head": "c102162ead6ca4bce575831fe28eb6682e82956c",
                "merge": "504b711dca4eb1c621f14ef54102102c3739248d", "pr": 87,
                "comments": [(6693795983, 87, "7e68ade63c94c5322997a19872e2f302e637148549da67c24e4a82f35d6f7858"),
                             (6693807028, 87, "372428d06005a12ab3b057605ff03e69958adab3c1513868fac26bafd4f29250"),
                             (6693814923, 4, "cc15db23816240873aff740a9f932b9e6ef97c88cca50c60840d08d45c708c85")]},
}


class Ledger:
    def __init__(self, directory, read, repository="sigaev-pro/bc-indexer"):
        self.directory, self.read, self.repository = Path(directory), read, repository

    def merged_facts(self, head, merge, number=None):
        commit = self.read(f"git/commits/{merge}")
        candidate = self.read(f"git/commits/{head}")
        require(commit["sha"] == merge and candidate["sha"] == head
                and commit["tree"]["sha"] == candidate["tree"]["sha"],
                "BLOCKED_EVIDENCE", "merged tree differs from the accepted candidate")
        main = self.read("git/ref/heads/main")["object"]["sha"]
        comparison = self.read(f"compare/{merge}...{main}")
        require(comparison["status"] in {"identical", "ahead"}
                and comparison["merge_base_commit"]["sha"] == merge,
                "BLOCKED_EVIDENCE", "accepted merge is not an ancestor of current main")
        pull = self.read(f"pulls/{number}") if number else None
        if pull:
            require(pull["number"] == number and pull["merged"] is True
                    and pull["merge_commit_sha"] == merge and pull["head"]["sha"] == head
                    and pull["base"]["ref"] == "main"
                    and pull["head"]["repo"]["full_name"] == self.repository == pull["base"]["repo"]["full_name"],
                    "BLOCKED_EVIDENCE", "remote pull request does not identify the accepted merge")
        return {"commit": commit, "head_commit": candidate, "pull_request": pull,
                "main_sha_at_verification": main, "ancestry": comparison}

    def historical(self, task):
        require(task in HISTORICAL, "BLOCKED_EVIDENCE", "task has no historical acceptance exception")
        pin = HISTORICAL[task]
        proof = []
        for comment_id, issue, body_hash in pin["comments"]:
            comment = self.read(f"issues/comments/{comment_id}")
            require(comment["id"] == comment_id and comment["user"]["login"] == "sigaev-pro"
                    and comment["issue_url"] == f"https://api.github.com/repos/{self.repository}/issues/{issue}"
                    and hashlib.sha256(comment["body"].encode()).hexdigest() == body_hash,
                    "BLOCKED_EVIDENCE", "pinned historical coordinator evidence changed")
            proof.append(comment)
        issue = self.read(f"issues/{pin['issue']}")
        require(issue["number"] == pin["issue"] and issue["state"] == "closed"
                and f"<!-- extractor-task:{task} -->" in issue["body"],
                "BLOCKED_EVIDENCE", "historical task identity or closure does not match")
        facts = self.merged_facts(pin["head"], pin["merge"], pin["pr"])
        if pin["pr"]:
            require(pin["head"] in {p["sha"] for p in facts["commit"]["parents"]},
                    "BLOCKED_EVIDENCE", "historical merge does not contain the reviewed head")
        return {"schema_version": 1, "kind": "historical-bootstrap-import", "task_id": task,
                "repository": self.repository, "head_sha": pin["head"], "merge_sha": pin["merge"],
                "proof": {"comments": proof, "issue": issue, "merged": facts},
                "limitations": "Explicit coordinator-reviewed bootstrap exception. No original pre-merge snapshot is reconstructed. Product runtime is NOT_RUN."}

    def authenticate_capture(self, bundle):
        """Authenticate an externally supplied retained proof against public records."""
        evidence.validate_capture(bundle)
        snapshot = bundle["snapshot"]
        require(snapshot["repository"] == self.repository
                and (snapshot["task_id"].startswith("BOOT-") or task_proofs.configured(snapshot["task_id"])),
                "BLOCKED_EVIDENCE", "this task has no configured acceptance adapter")
        comment = self.read(f"issues/comments/{snapshot['review_comment_id']}")
        for field in ("id", "body", "issue_url", "html_url"):
            require(comment[field] == snapshot["review_comment"][field],
                    "BLOCKED_EVIDENCE", "retained review differs from the public report")
        require(comment["user"]["login"] == snapshot["coordinator_login"] == "sigaev-pro",
                "BLOCKED_EVIDENCE", "retained proof has an untrusted publisher")
        run = self.read(f"actions/runs/{snapshot['ci_run']['id']}")
        for field in ("id", "head_sha", "path", "event", "status", "conclusion"):
            require(run[field] == snapshot["ci_run"][field], "BLOCKED_EVIDENCE", "retained CI run differs from its public record")
        require(run["repository"]["full_name"] == self.repository, "BLOCKED_EVIDENCE", "CI repository differs")
        check = max((c for c in snapshot["check_runs"]["check_runs"]
                     if c["name"] == acceptance.CI_CONTEXT and c["app"]["id"] == acceptance.ACTIONS_APP_ID), key=lambda c: c["id"])
        actual_check = self.read(f"check-runs/{check['id']}")
        for field in ("id", "head_sha", "name", "status", "conclusion", "details_url"):
            require(actual_check[field] == check[field], "BLOCKED_EVIDENCE", "retained CI check differs from its public record")
        require(actual_check["app"]["id"] == acceptance.ACTIONS_APP_ID,
                "BLOCKED_EVIDENCE", "CI check has an unexpected publisher")
        head = snapshot["pull_request"]["head"]["sha"]
        statuses = self.read(f"commits/{head}/statuses?per_page=100")
        require(len(statuses) < 100, "BLOCKED_EVIDENCE", "status history requires unsupported pagination")
        saved = acceptance.review_status(snapshot["combined_status"], snapshot["commit_statuses"], self.repository, head)
        rows = [row for row in statuses if row["id"] == saved["id"]]
        require(len(rows) == 1 and all(rows[0][field] == saved[field] for field in acceptance.STATUS_FIELDS)
                and rows[0]["creator"]["login"] == "sigaev-pro", "BLOCKED_EVIDENCE", "retained status is not authenticated")

    def verify_task_proof(self, receipt):
        try:
            return task_proofs.verify_public_binding(receipt, self.read)
        except (ValueError, KeyError, TypeError, AttributeError) as error:
            raise Blocked("BLOCKED_ARTIFACT_PROOF", str(error)) from error

    def from_capture(self, bundle, provenance, task_verification=None):
        self.authenticate_capture(bundle)
        expected = bundle["expected"]
        if task_proofs.configured(expected["task_id"]):
            task_verification = task_verification if task_verification is not None else bundle["snapshot"].get("task_verification")
            self.verify_task_proof({"capture": bundle, "task_verification": task_verification})
        pull = self.read(f"pulls/{expected['pull_request_number']}")
        require(pull["merged"] is True, "BLOCKED_NOT_MERGED", "capture belongs to an unmerged pull request")
        facts = self.merged_facts(expected["head_sha"], pull["merge_commit_sha"], expected["pull_request_number"])
        require({p["sha"] for p in facts["commit"]["parents"]} == {expected["head_sha"], expected["base_sha"]},
                "BLOCKED_EVIDENCE", "actual merge parents differ from the captured candidate")
        receipt = {"schema_version": 1, "kind": "merged-acceptance", "task_id": expected["task_id"],
                "repository": self.repository, "head_sha": expected["head_sha"], "merge_sha": pull["merge_commit_sha"],
                "capture": bundle, "merge": facts, "implementation_provenance": provenance,
                "runtime": "NOT_RUN"}
        if task_proofs.configured(expected["task_id"]):
            receipt["task_verification"] = task_verification
        return receipt

    def verify(self, task, receipt, fresh=True):
        require(receipt["schema_version"] == 1 and receipt["repository"] == self.repository
                and receipt["task_id"] == task and (task.startswith("BOOT-") or task_proofs.configured(task)),
                "BLOCKED_EVIDENCE", "acceptance receipt identity mismatch")
        if receipt["kind"] == "historical-bootstrap-import":
            require(task in HISTORICAL, "BLOCKED_EVIDENCE", "unknown historical exception")
            pin = HISTORICAL[task]
            require(receipt["head_sha"] == pin["head"] and receipt["merge_sha"] == pin["merge"],
                    "BLOCKED_EVIDENCE", "historical accepted commit differs from the pin")
            comments = receipt["proof"]["comments"]
            require(len(comments) == len(pin["comments"]), "BLOCKED_EVIDENCE", "historical evidence is incomplete")
            for comment, (number, issue, body_hash) in zip(comments, pin["comments"]):
                require(comment["id"] == number and comment["user"]["login"] == "sigaev-pro"
                        and comment["issue_url"] == f"https://api.github.com/repos/{self.repository}/issues/{issue}"
                        and hashlib.sha256(comment["body"].encode()).hexdigest() == body_hash,
                        "BLOCKED_EVIDENCE", "retained historical evidence changed")
            number = pin["pr"]
        elif receipt["kind"] == "merged-acceptance":
            evidence.validate_capture(receipt["capture"])
            expected = receipt["capture"]["expected"]
            require(expected["task_id"] == task and expected["repository"] == self.repository
                    and expected["head_sha"] == receipt["head_sha"], "BLOCKED_EVIDENCE", "receipt differs from the retained capture")
            commit = receipt["merge"]["commit"]
            require(commit["sha"] == receipt["merge_sha"]
                    and commit["tree"]["sha"] == receipt["capture"]["snapshot"]["head_commit"]["tree"]["sha"]
                    and {p["sha"] for p in commit["parents"]} == {expected["head_sha"], expected["base_sha"]},
                    "BLOCKED_EVIDENCE", "retained merge receipt differs from the accepted tree or parents")
            number = expected["pull_request_number"]
            if task_proofs.configured(task):
                self.verify_task_proof(receipt)
        else:
            raise Blocked("BLOCKED_EVIDENCE", "unknown acceptance receipt kind")
        if fresh:
            self.merged_facts(receipt["head_sha"], receipt["merge_sha"], number)
        return receipt

    def save(self, receipt):
        self.verify(receipt["task_id"], receipt)
        path = self.directory / (receipt["task_id"] + ".json")
        record = {"receipt": receipt, "sha256": evidence.digest(receipt)}
        if path.exists():
            old = self.load(receipt["task_id"])
            require(old["head_sha"] == receipt["head_sha"] and old["merge_sha"] == receipt["merge_sha"],
                    "BLOCKED_EVIDENCE", "a different commit already owns this task's acceptance")
            return old
        evidence.atomic_json(path, record, exclusive=True)
        return receipt

    def load(self, task):
        record = read_json(self.directory / (task + ".json"))
        require(record["sha256"] == evidence.digest(record["receipt"]), "BLOCKED_EVIDENCE", "acceptance receipt digest mismatch")
        return self.verify(task, record["receipt"])
