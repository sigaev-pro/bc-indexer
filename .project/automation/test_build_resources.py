"""Exercise namespace ownership and daemon-side cleanup without native builds."""

import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import build_resources as resources


IMAGE = "docker.io/library/debian@sha256:" + "a" * 64
CID = "b" * 64


def specification():
    return {"schema_version": 1, "run_id": "run-test", "task_id": "E01-01.builds",
            "operation_id": "verify-test", "nonce": "c" * 64, "images": [IMAGE]}


class DockerFixture:
    def __init__(self, spec, present=True):
        self.spec = spec
        self.present = present
        self.calls = []
        self.foreign = False
        self.failed_scan = False
        self.failed_remove = False
        self.failed_create = False

    def __call__(self, arguments):
        self.calls.append(arguments)
        output, code = "", 0
        if arguments[0] == "ps":
            code = 1 if self.failed_scan else 0
            output = CID + "\n" if self.present else ""
        elif arguments[:2] == ["container", "create"]:
            self.present = True
            code = 1 if self.failed_create else 0
            output = CID + "\n"
        elif arguments[:2] == ["container", "inspect"]:
            labels = resources.labels(self.spec)
            if self.foreign:
                labels[resources.LABEL_PREFIX + "operation_id"] = "foreign"
            output = json.dumps([{"Id": CID, "Image": "sha256:" + "d" * 64,
                "Config": {"Image": IMAGE, "Labels": labels}, "State": {"Running": True}}])
        elif arguments[:2] == ["container", "rm"]:
            code = 1 if self.failed_remove else 0
            if not code:
                self.present = False
        elif arguments[:2] != ["container", "stop"]:
            raise AssertionError(arguments)
        return subprocess.CompletedProcess(arguments, code, output, "")


class BuildResourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.inventory = Path(self.temporary.name) / "docker-resources.json"

    def prepared(self, spec):
        docker = DockerFixture(spec, False)
        resources.begin(spec, self.inventory, docker)
        resources.create(spec, self.inventory, "fixture", IMAGE, [], ["true"], docker)
        return docker

    def test_empty_start_and_cleanup_are_explicit(self):
        spec = specification()
        docker = DockerFixture(spec, False)
        self.assertTrue(resources.begin(spec, self.inventory, docker)["empty"])
        self.assertTrue(resources.validate_cleanup(spec, resources.cleanup(spec, self.inventory, docker))["complete"])

    def test_existing_namespace_cannot_be_silently_adopted(self):
        spec = specification()
        with self.assertRaises(ValueError):
            resources.begin(spec, self.inventory, DockerFixture(spec))

    def test_cleanup_targets_the_full_immutable_id_and_confirms_absence(self):
        spec = specification()
        docker = self.prepared(spec)
        report = resources.cleanup(spec, self.inventory, docker)
        resources.validate_cleanup(spec, report)
        self.assertEqual(report["resources"][0]["id"], CID)
        self.assertIn(["container", "stop", "--time", "3", CID], docker.calls)
        self.assertIn(["container", "rm", "--force", CID], docker.calls)
        self.assertEqual(docker.calls[-1][0], "ps")

    def test_foreign_labels_forbid_all_mutation(self):
        spec = specification()
        docker = self.prepared(spec)
        docker.foreign = True
        self.assertFalse(resources.cleanup(spec, self.inventory, docker)["complete"])
        self.assertFalse(any(row[:2] in (["container", "stop"], ["container", "rm"]) for row in docker.calls))

    def test_daemon_failure_is_not_an_empty_namespace(self):
        spec = specification()
        docker = self.prepared(spec)
        docker.failed_scan = True
        self.assertFalse(resources.cleanup(spec, self.inventory, docker)["complete"])

    def test_failed_removal_preserves_uncertainty(self):
        spec = specification()
        docker = self.prepared(spec)
        docker.failed_remove = True
        self.assertFalse(resources.cleanup(spec, self.inventory, docker)["complete"])
        self.assertTrue(docker.present)

    def test_changed_cleanup_binding_is_rejected(self):
        spec = specification()
        report = resources.cleanup(spec, self.inventory, self.prepared(spec))
        for change in ({"nonce": "e" * 64}, {"complete": False}, {"remaining": [CID]}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                resources.validate_cleanup(spec, dict(report, **change))
        invalid = copy.deepcopy(report)
        invalid["resources"][0]["labels"][resources.LABEL_PREFIX + "task_id"] = "BOOT-09"
        with self.assertRaises(ValueError):
            resources.validate_cleanup(spec, invalid)

    def test_floating_images_unknown_fields_and_boolean_versions_fail_closed(self):
        for change in ({"images": ["debian:latest"]}, {"extra": True}, {"schema_version": True}, {"nonce": "short"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                resources.validate(dict(specification(), **change))

    def test_uncertain_create_is_reconciled_before_cleanup(self):
        spec = specification()
        docker = DockerFixture(spec, False)
        resources.begin(spec, self.inventory, docker)
        docker.failed_create = True
        with self.assertRaises(ValueError):
            resources.create(spec, self.inventory, "fixture", IMAGE, [], ["true"], docker)
        self.assertIsNone(resources.read_inventory(spec, self.inventory)["intents"][0]["container_id"])
        resources.validate_cleanup(spec, resources.cleanup(spec, self.inventory, docker))

    def test_unobserved_pending_create_cannot_be_declared_clean(self):
        spec = specification()
        docker = DockerFixture(spec, False)
        resources.begin(spec, self.inventory, docker)
        docker.failed_create = True
        with self.assertRaises(ValueError):
            resources.create(spec, self.inventory, "fixture", IMAGE, [], ["true"], docker)
        docker.present = False
        self.assertFalse(resources.cleanup(spec, self.inventory, docker)["complete"])


if __name__ == "__main__":
    unittest.main()
