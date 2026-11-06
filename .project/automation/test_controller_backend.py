"""Exercise real publication reconciliation against a fake GitHub transport."""

import copy
from pathlib import Path
import tempfile
import unittest

import acceptance
from controller import Blocked
from controller_backend import RealBackend
from test_controller import Crash


class PublicationBackend(RealBackend):
    def __init__(self):
        self.repository = "sigaev-pro/bc-indexer"
        self.root = Path("/isolated")
        self.remote_head = None
        self.pulls, self.comments, self.statuses = [], [], []
        self.counts = {name: 0 for name in ("push", "pr", "comment", "status")}
        self.crash_after = None
        self.main = "b" * 40

    def candidate(self, run, task):
        if self.main != task["workspace"]["base_sha"]:
            raise Blocked("BLOCKED_BASE_CHANGED", "base changed")
        return task["worker"]["commit_sha"]

    def changed(self, name):
        self.counts[name] += 1
        if self.crash_after == name:
            self.crash_after = None
            raise Crash(name)

    def git(self, directory, *arguments):
        if arguments[0] == "ls-remote":
            return self.remote_head + "\trefs/heads/owned" if self.remote_head else ""
        if arguments[0] == "push":
            self.remote_head = arguments[2].split(":")[0]
            self.changed("push")
            return ""
        raise AssertionError(arguments)

    def pages(self, path):
        if path.startswith("pulls?"):
            return copy.deepcopy(self.pulls)
        if path == "issues/90/comments":
            return copy.deepcopy(self.comments)
        raise AssertionError(path)

    def read(self, path):
        if path.endswith("/statuses?per_page=100"):
            return copy.deepcopy(self.statuses)
        raise AssertionError(path)

    def mutate(self, method, path, body):
        if path == "pulls":
            record = dict(body, number=90, state="open", head={"sha": self.remote_head})
            self.pulls.append(record)
            self.changed("pr")
        elif path == "issues/90/comments":
            record = dict(body, id=len(self.comments) + 100, user={"login": "sigaev-pro"},
                          html_url=f"https://github.com/{self.repository}/pull/90#issuecomment-100")
            self.comments.append(record)
            self.changed("comment")
        elif path.startswith("statuses/"):
            record = dict(body, id=len(self.statuses) + 200, creator={"login": "sigaev-pro"})
            self.statuses.append(record)
            self.changed("status")
        else:
            raise AssertionError((method, path))
        return copy.deepcopy(record)


class PublicationTests(unittest.TestCase):
    def task(self):
        return {"task_id": "BOOT-03", "workspace": {"branch": "owned", "path": "/isolated", "base_sha": "b" * 40},
                "worker": {"commit_sha": "a" * 40, "session_id": "worker"},
                "review": {"verdict": "PASS", "session_id": "reviewer", "checks": ["Independent checks passed."],
                           "unverified": ["Product runtime remains NOT_RUN."]},
                "verification": {"passed": True}}

    def run_record(self):
        return {"catalog": {"BOOT-03": {"snapshot": "## BOOT-03 — Controller"}}}

    def test_lost_responses_do_not_duplicate_real_adapter_mutations(self):
        for name in ("push", "pr", "comment", "status"):
            with self.subTest(name=name):
                backend = PublicationBackend()
                backend.crash_after = name
                with self.assertRaises(Crash):
                    backend.do_publish(self.run_record(), self.task(), {"id": "publication"}, None)
                result = backend.do_publish(self.run_record(), self.task(), {"id": "publication"}, None)
                self.assertEqual(result["number"], 90)
                self.assertEqual(backend.counts, {"push": 1, "pr": 1, "comment": 1, "status": 1})
                self.assertEqual(backend.statuses[0]["context"], acceptance.REVIEW_CONTEXT)

    def test_changed_base_rejects_publication_before_mutation(self):
        backend = PublicationBackend()
        backend.main = "f" * 40
        with self.assertRaises(Blocked):
            backend.do_publish(self.run_record(), self.task(), {"id": "publication"}, None)
        self.assertTrue(all(value == 0 for value in backend.counts.values()))

    def test_ambiguous_pr_does_not_get_a_review_status(self):
        backend = PublicationBackend()
        backend.remote_head = "a" * 40
        backend.pulls = [{"number": 90}, {"number": 91}]
        with self.assertRaises(Blocked):
            backend.do_publish(self.run_record(), self.task(), {"id": "publication"}, None)
        self.assertEqual(backend.counts["comment"], 0)
        self.assertEqual(backend.counts["status"], 0)


if __name__ == "__main__":
    unittest.main()
