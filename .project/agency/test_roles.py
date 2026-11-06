"""Check role assignment boundaries independently of model output."""

import copy
import unittest

import roles


class RoleBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.handoff = {
            "task_id": "BOOT-02", "stage": "BOOT", "stage_limit": "BOOT",
            "authorization_ref": "D86", "run_id": "bootstrap", "session_id": "probe",
            "base_commit": "1" * 40, "spec_commit": "2" * 40,
            "worktree": str(roles.ROOT), "read_only": True, "allowed_paths": [],
            "acceptance_criteria": ["Report the assigned role and stage limit"],
            "task": "Inspect the role instructions without changing files.",
        }

    def test_every_role_has_pinned_sources_and_complete_prompt(self):
        data = roles.registry()
        self.assertEqual(len(data["roles"]), 8)
        for name in data["roles"]:
            prompt, digest = roles.render(name, self.handoff)
            self.assertIn("<project_rules>", prompt)
            self.assertIn("<shared_contract>", prompt)
            self.assertIn("<task_handoff>", prompt)
            self.assertEqual(len(digest), 64)

    def test_d86_cannot_open_product_stage(self):
        item = dict(self.handoff, task_id="E01-01", stage="E01", stage_limit="E01")
        with self.assertRaisesRegex(ValueError, "BOOT only"):
            roles.render("devops", item)

    def test_resume_cannot_exceed_saved_stage_limit(self):
        item = dict(self.handoff, task_id="E01-01", stage="E01")
        with self.assertRaisesRegex(ValueError, "stage limit"):
            roles.render("devops", item)

    def test_reviewer_cannot_write(self):
        item = dict(self.handoff, read_only=False, allowed_paths=[".project/agency"])
        with self.assertRaisesRegex(ValueError, "reviewer must be read-only"):
            roles.render("reviewer", item)

    def test_unsafe_and_secret_paths_are_rejected(self):
        for path in ["../outside", "/tmp/outside", ".", ".git/config", ".env", "secrets/key", "a/../../b", "a\\b"]:
            item = dict(self.handoff, read_only=False, allowed_paths=[path])
            with self.subTest(path=path), self.assertRaises(ValueError):
                roles.render("devops", item)

    def test_missing_criteria_and_unknown_role_are_rejected(self):
        with self.assertRaises(ValueError):
            roles.render("unknown", self.handoff)
        item = copy.deepcopy(self.handoff)
        item["acceptance_criteria"] = []
        with self.assertRaises(ValueError):
            roles.render("architect", item)


if __name__ == "__main__":
    unittest.main()
