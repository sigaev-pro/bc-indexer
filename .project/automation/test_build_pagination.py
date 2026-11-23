"""Independent retained GitHub pagination controls; no live-source acceptance."""

import copy
from pathlib import Path
import tempfile
import unittest

import build_contract as contract
import build_updates as updates
import build_source_evidence


class PaginationTests(unittest.TestCase):
    repository = "https://api.github.com/repos/ethereum/go-ethereum"
    first = repository + "/releases?per_page=100"
    second = "https://api.github.com/repositories/15452919/releases?per_page=100&page=2"

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.rows = []
        self.add(self.repository, {"id": 15452919, "full_name": "ethereum/go-ethereum", "url": self.repository})
        self.add(self.first, [{"tag_name": "v1.14.12"}], '<' + self.second + '>; rel="next", <' + self.second + '>; rel="last"')
        self.add(self.second, [{"tag_name": "v1.14.11"}])

    def add(self, url, body, link=""):
        path = self.root / (str(len(self.rows)) + ".json")
        path.write_bytes(contract.canonical(body))
        row = {"url": url, "status": 200, "headers": {"link": link} if link else {},
               "body": contract.reference(self.root, path), "observed_at": 946684800}
        self.rows.append(row)
        return row

    def observations(self, rows=None):
        # Bind modified synthetic responses before testing pagination semantics.
        rows = [build_source_evidence.fixture_observation(row, self.root) for row in (self.rows if rows is None else rows)]
        return updates.Observations(self.root, rows, 946684800, True)

    def test_named_release_endpoint_follows_bound_numeric_repository_alias(self):
        observations = self.observations()
        self.assertEqual(observations.pages(self.first), [{"tag_name": "v1.14.12"}, {"tag_name": "v1.14.11"}])
        self.assertEqual(observations.used, {self.repository, self.first, self.second})

    def test_namespaced_tag_tail_is_preserved_and_metadata_can_be_declared_without_alias(self):
        first = self.repository + "/git/matching-refs/tags/cmd/tool/"
        second = "https://api.github.com/repositories/15452919/git/matching-refs/tags/cmd/tool/?page=2"
        self.add(first, [{"ref": "refs/tags/cmd/tool/v1.0.0"}], '<' + second + '>; rel="next"')
        self.add(second, [])
        observations = self.observations()
        self.assertEqual(len(observations.pages(first)), 1)
        self.assertIn(self.repository, observations.used)
        self.rows[1]["headers"] = {}
        observations = self.observations()
        self.assertEqual(len(observations.pages(self.first)), 1)
        self.assertIn(self.repository, observations.used)

    def test_numeric_alias_requires_exact_repository_metadata(self):
        for change in ("missing", "wrong-id", "boolean-id", "zero-id", "string-id", "wrong-name", "wrong-url", "name-case", "url-case", "forged-hash", "unavailable"):
            with self.subTest(change=change):
                rows = copy.deepcopy(self.rows)
                if change == "missing":
                    rows.pop(0)
                elif change == "forged-hash":
                    rows[0]["body"]["sha256"] = "0" * 64
                elif change == "unavailable":
                    rows[0]["status"] = 404
                else:
                    body = {"id": 15452919, "full_name": "ethereum/go-ethereum", "url": self.repository}
                    field, value = {"wrong-id": ("id", 123), "boolean-id": ("id", True), "zero-id": ("id", 0),
                                    "string-id": ("id", "15452919"), "wrong-name": ("full_name", "another/repository"),
                                    "name-case": ("full_name", "Ethereum/go-ethereum"), "url-case": ("url", self.repository.replace("ethereum/", "Ethereum/")),
                                    "wrong-url": ("url", "https://api.github.com/repos/another/repository")}[change]
                    body[field] = value
                    path = self.root / (change + ".json")
                    path.write_bytes(contract.canonical(body))
                    rows[0]["body"] = contract.reference(self.root, path)
                with self.assertRaises(ValueError):
                    self.observations(rows).pages(self.first)

    def test_other_repository_tail_origin_and_ambiguous_or_looping_links_fail(self):
        for target in (self.second.replace("15452919", "123"), self.second.replace("/releases", "/tags"),
                       self.second.replace("api.github.com", "raw.githubusercontent.com"),
                       self.second.replace("https://", "http://"), self.first):
            with self.subTest(target=target):
                rows = copy.deepcopy(self.rows)
                rows[1]["headers"]["link"] = '<' + target + '>; rel="next"'
                with self.assertRaises(ValueError):
                    self.observations(rows).pages(self.first)
        rows = copy.deepcopy(self.rows)
        rows[1]["headers"]["link"] = ('<' + self.second + '>; rel="next", ') * 2
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            self.observations(rows).pages(self.first)

    def test_named_pages_and_non_github_pages_keep_existing_path_rules(self):
        same = self.first + "&page=2"
        self.add(same, [])
        rows = copy.deepcopy(self.rows)
        rows[1]["headers"]["link"] = '<' + same + '>; rel="next"'
        rows.pop(0)
        self.assertEqual(len(self.observations(rows).pages(self.first)), 1)
        first, second = "https://go.dev/releases?page=1", "https://go.dev/releases?page=2"
        row = self.add(first, [1], '<' + second + '>; rel="next"')
        self.add(second, [2])
        self.assertEqual(self.observations().pages(first), [1, 2])
        row["headers"]["link"] = '<https://go.dev/repositories/15452919/releases?page=2>; rel="next"'
        with self.assertRaises(ValueError):
            self.observations().pages(first)

    def test_page_limit_still_applies_to_numeric_aliases(self):
        for number in range(3, 66):
            url = self.second.rsplit("=", 1)[0] + "=" + str(number)
            self.rows[-1]["headers"]["link"] = '<' + url + '>; rel="next"'
            self.add(url, [])
        with self.assertRaisesRegex(ValueError, "pagination loops or changes"):
            self.observations().pages(self.first)

    def test_named_and_numeric_aliases_cannot_repeat_a_logical_page(self):
        first = self.first + "&page=1"
        for target in (self.repository + "/releases?page=1&per_page=100",
                       "https://api.github.com/repositories/15452919/releases?page=1&per_page=100"):
            with self.subTest(target=target):
                self.rows = self.rows[:1]
                self.add(first, [{"tag_name": "v1.14.12"}], '<' + target + '>; rel="next"')
                self.add(target, [{"tag_name": "v1.14.12"}])
                with self.assertRaisesRegex(ValueError, "pagination loops"):
                    self.observations().pages(first)

    def metadata(self):
        return {"id": 15452919, "full_name": "ethereum/go-ethereum", "url": self.repository}

    def test_unfetched_metadata_defers_binding_to_the_caller_and_then_binds(self):
        third = "https://api.github.com/repositories/15452919/releases?per_page=100&page=3"
        self.add(third, [{"tag_name": "v1.14.10"}])
        self.rows[2]["headers"]["link"] = '<' + third + '>; rel="next"'
        rows = copy.deepcopy(self.rows)
        rows.pop(0)  # the real-world pass did not pre-fetch repository metadata
        observations = self.observations(rows)
        for _ in range(2):  # a replay defers again instead of consuming a source
            with self.assertRaises(updates.AliasBindingRequired) as pending:
                observations.pages(self.first)
            self.assertEqual(pending.exception.named_url, self.first)
            self.assertEqual(pending.exception.metadata_url, self.repository)
            self.assertEqual(pending.exception.numeric_url, self.second)
            self.assertEqual(observations.deferred, {self.second: self.repository})
            self.assertEqual(observations.used, {self.first})
        self.assertIs(type(pending.exception), updates.AliasBindingRequired)
        self.assertIsInstance(pending.exception, ValueError)
        observations.bindAlias(self.first, self.metadata())
        self.assertEqual(observations.deferred, {})
        self.assertEqual(observations.pages(self.first),
                         [{"tag_name": "v1.14.12"}, {"tag_name": "v1.14.11"}, {"tag_name": "v1.14.10"}])
        self.assertEqual(observations.used, {self.first, self.second, third})

    def test_caller_binding_rejects_wrong_metadata_and_wrong_numeric_identity_after_binding(self):
        rows = copy.deepcopy(self.rows)
        rows.pop(0)
        for change in ("boolean-id", "zero-id", "string-id", "missing-id", "wrong-name",
                       "name-case", "wrong-url", "url-case", "non-object"):
            with self.subTest(change=change):
                metadata = [15452919] if change == "non-object" else self.metadata()
                if change != "non-object":
                    field, value = {"boolean-id": ("id", True), "zero-id": ("id", 0),
                                    "string-id": ("id", "15452919"), "missing-id": ("id", None),
                                    "wrong-name": ("full_name", "another/repository"),
                                    "name-case": ("full_name", "Ethereum/go-ethereum"),
                                    "wrong-url": ("url", "https://api.github.com/repos/another/repository"),
                                    "url-case": ("url", self.repository.replace("ethereum/", "Ethereum/"))}[change]
                    metadata[field] = value
                observations = self.observations(rows)
                with self.assertRaisesRegex(ValueError, "invalid or different identity"):
                    observations.bindAlias(self.first, metadata)
                with self.assertRaises(updates.AliasBindingRequired):  # nothing was bound
                    observations.pages(self.first)
        # A bound identity is exact: a numeric alias carrying another repository
        # ID is rejected after binding, in both directions of the mismatch.
        wrong = copy.deepcopy(rows)
        wrong[0]["headers"]["link"] = '<' + self.second.replace("15452919", "123") + '>; rel="next"'
        wrong_observations = self.observations(wrong)
        wrong_observations.bindAlias(self.first, self.metadata())
        with self.assertRaisesRegex(ValueError, "pagination loops or changes source identity"):
            wrong_observations.pages(self.first)
        misbound = self.observations(rows)
        misbound.bindAlias(self.first, dict(self.metadata(), id=123))
        with self.assertRaisesRegex(ValueError, "pagination loops or changes source identity"):
            misbound.pages(self.first)
        # A correct binding is idempotent and cannot be rebound to another ID.
        observations = self.observations(rows)
        self.assertEqual(observations.bindAlias(self.first, self.metadata()), "/repositories/15452919")
        self.assertEqual(observations.bindAlias(self.first, self.metadata()), "/repositories/15452919")
        with self.assertRaisesRegex(ValueError, "cannot be rebound"):
            observations.bindAlias(self.first, dict(self.metadata(), id=15452918))
        self.assertEqual(observations.pages(self.first), [{"tag_name": "v1.14.12"}, {"tag_name": "v1.14.11"}])

    def test_bind_alias_requires_a_named_official_github_endpoint(self):
        observations = self.observations()
        for named_url in (self.second, self.repository,
                          "http://api.github.com/repos/ethereum/go-ethereum/releases",
                          "https://raw.githubusercontent.com/ethereum/go-ethereum/releases",
                          "https://go.dev/releases?per_page=100"):
            with self.subTest(named_url=named_url), self.assertRaisesRegex(ValueError, "named official GitHub repository endpoint"):
                observations.bindAlias(named_url, self.metadata())

    def test_caller_binding_cannot_contradict_a_retained_metadata_observation(self):
        observations = self.observations()
        with self.assertRaisesRegex(ValueError, "differs from the retained observation"):
            observations.bindAlias(self.first, dict(self.metadata(), id=123))
        observations.bindAlias(self.first, self.metadata())
        self.assertEqual(observations.pages(self.first), [{"tag_name": "v1.14.12"}, {"tag_name": "v1.14.11"}])
        self.assertEqual(observations.used, {self.repository, self.first, self.second})


if __name__ == "__main__":
    unittest.main()
