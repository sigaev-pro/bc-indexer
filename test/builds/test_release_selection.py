"""Docker Engine release selection and exact source-tag regression controls."""

import copy
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import Mock

PROJECT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(PROJECT / "scripts/versioning"), str(PROJECT / "build/native")]
from sources import select_release
from update import git_pin


def release(tag, **flags):
    return dict(tag_name=tag, draft=False, prerelease=False) | flags


class DockerReleaseTests(unittest.TestCase):
    def setUp(self):
        registry = json.loads((PROJECT / ".project/bootstrap/components/registry.json").read_bytes())
        self.entry = next(row for row in registry["components"] if row["id"] == "docker-engine")
        self.transport = Mock()

    def test_current_engine_tag_outranks_legacy_release_in_either_order(self):
        rows = [release("docker-v29.9.3"), release("v28.5.2")]
        for order in (rows, rows[::-1]):
            with self.subTest(tags=[row["tag_name"] for row in order]):
                self.transport.pages.return_value = order
                self.assertEqual(select_release(self.entry, self.transport), "docker-v29.9.3")

    def test_engine_namespaces_stability_and_complete_version_are_required(self):
        excluded = ["api/v999.0.0", "client/v999.0.0", "api/docker-v999.0.0",
                    "client/docker-v999.0.0", "docker-v999.0.0-rc1", "v999.0.0-rc1",
                    "docker-v999.0.0+build", "v999.0", "docker-v999.0", "999.0.0"]
        for tag in excluded:
            with self.subTest(tag=tag):
                self.transport.pages.return_value = [release(tag), release("v28.5.2")]
                self.assertEqual(select_release(self.entry, self.transport), "v28.5.2")
        for tag in ("docker-v999.0.0", "v999.0.0"):
            for flag in ("draft", "prerelease"):
                with self.subTest(tag=tag, flag=flag):
                    self.transport.pages.return_value = [release(tag, **{flag: True}), release("v28.5.2")]
                    self.assertEqual(select_release(self.entry, self.transport), "v28.5.2")

    def test_engine_versions_use_numeric_order_across_both_tag_forms(self):
        for tags, expected in [(["docker-v29.9.3", "docker-v29.10.0"], "docker-v29.10.0"),
                               (["docker-v29.9.3", "v30.0.0"], "v30.0.0")]:
            with self.subTest(tags=tags):
                self.transport.pages.return_value = [release(tag) for tag in tags]
                self.assertEqual(select_release(self.entry, self.transport), expected)

    def test_engine_tag_resolution_preserves_original_tag(self):
        self.transport.pages.return_value = [release("docker-v29.9.3"), release("v28.5.2")]
        commit, tag_object = "a" * 40, "b" * 40
        self.transport.json.side_effect = [{"object": {"type": "tag", "sha": tag_object}},
                                           {"object": {"type": "commit", "sha": commit}}]
        latest, pin = git_pin(self.entry, {"dependency_inputs": []}, {}, self.transport)
        self.assertEqual((latest, pin["version"], pin["commit"]), ("docker-v29.9.3", "docker-v29.9.3", commit))
        self.assertEqual([call.args[0] for call in self.transport.json.call_args_list],
                         ["https://api.github.com/repos/moby/moby/git/ref/tags/docker-v29.9.3",
                          "https://api.github.com/repos/moby/moby/git/tags/" + tag_object])

    def test_engine_tag_support_is_scoped_to_component_and_official_source(self):
        for changes in ({"id": "other-component"}, {"repository": "example/other"},
                        {"kind": "git_module_tags"}, {"channel": "upstream-bundle"}):
            with self.subTest(changes=changes):
                entry = copy.deepcopy(self.entry)
                (entry if "id" in changes else entry["source"]).update(changes)
                self.transport.pages.return_value = [release("docker-v999.0.0"), release("v28.5.2")]
                if changes.get("kind") == "git_module_tags":
                    self.transport.pages.return_value = [dict(ref="refs/tags/cmd/protoc-gen-go-grpc/docker-v999.0.0"),
                                                        dict(ref="refs/tags/cmd/protoc-gen-go-grpc/v28.5.2")]
                    expected = "cmd/protoc-gen-go-grpc/v28.5.2"
                else:
                    expected = "v28.5.2"
                self.assertEqual(select_release(entry, self.transport), expected)


if __name__ == "__main__":
    unittest.main()
