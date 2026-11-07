"""Independent GitHub numeric-alias pagination controls using synthetic sources."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path[:0] = [str(Path(__file__).resolve().parent),
               str(Path(__file__).resolve().parents[2] / "scripts/versioning"),
               str(Path(__file__).resolve().parents[2] / "build/native")]
from fixtures import Files, OfficialFixture, PROJECT, REGISTRY, LOCK, hash_bytes, packed
from sources import Transport

REPO = "https://api.github.com/repos/example/synthetic-client"
START = REPO + "/releases?per_page=100"
REPOSITORY_ID = 700001
NUMERIC = "https://api.github.com/repositories/700001/releases?per_page=100&page=2"


def next_link(url):
    return {"link": '<' + url + '>; rel="next"'}


def repository_metadata(repository=REPO, identifier=REPOSITORY_ID):
    return dict(id=identifier, full_name=repository.removeprefix("https://api.github.com/repos/"), url=repository)


def transport(root, rows):
    files = Files(root)
    observations = [dict(url=url, status=status, headers=headers, body=files.put(body), observed_at=1234567890)
                    for url, body, headers, status in rows]
    path = root / "transport.json"
    path.write_bytes(packed(dict(schema_version=1, synthetic=True, observations=observations)))
    return Transport(root / "retained", path)


class GitHubPaginationTests(unittest.TestCase):
    def test_named_numeric_named_pages_retain_exact_repository_binding(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            third = REPO + "/releases?per_page=100&page=3"
            fourth = NUMERIC.replace("page=2", "page=4")
            rows = [(START, [{"page": 1}], next_link(NUMERIC), 200),
                    (REPO, repository_metadata(), {}, 200),
                    (NUMERIC, [{"page": 2}], next_link(third), 200),
                    (third, [{"page": 3}], next_link(fourth), 200),
                    (fourth, [{"page": 4}], {}, 200)]
            source = transport(root, rows)
            self.assertEqual(source.pages(START), [{"page": number} for number in range(1, 5)])
            self.assertEqual(set(source.rows), {START, REPO, NUMERIC, third, fourth})
            self.assertIn(REPO, source.used)
            self.assertEqual(json.loads((root / source.rows[REPO]["body"]["path"]).read_bytes())["id"], REPOSITORY_ID)

    def test_namespaced_endpoint_tail_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            tail = "/git/matching-refs/tags/cmd/protoc-gen-go-grpc/"
            start = REPO + tail
            numeric = "https://api.github.com/repositories/700001" + tail + "?page=2"
            source = transport(root, [(start, [{"ref": "one"}], next_link(numeric), 200), (numeric, [{"ref": "two"}], {}, 200),
                                      (REPO, repository_metadata(), {}, 200)])
            self.assertEqual(source.pages(start), [{"ref": "one"}, {"ref": "two"}])

    def test_named_pages_need_no_metadata_request(self):
        with tempfile.TemporaryDirectory() as directory:
            second = START + "&page=2"
            source = transport(Path(directory).resolve(), [(START, [1], next_link(second), 200), (second, [2], {}, 200)])
            self.assertEqual(source.pages(START), [1, 2])
            self.assertNotIn(REPO, source.rows)

    def test_wrong_id_endpoint_origin_and_named_repository_are_rejected(self):
        for url in (NUMERIC.replace("700001", "700002"), NUMERIC.replace("/releases", "/tags"),
                    NUMERIC.replace("api.github.com", "raw.githubusercontent.com"), NUMERIC.replace("https:", "http:"),
                    NUMERIC.replace("700001", "0700001"), NUMERIC.replace("700001", "-1"),
                    "https://api.github.com/repos/other/client/releases?page=2"):
            with self.subTest(url=url), tempfile.TemporaryDirectory() as directory:
                source = transport(Path(directory).resolve(), [(START, [1], next_link(url), 200), (REPO, repository_metadata(), {}, 200)])
                with self.assertRaises(ValueError):
                    source.pages(START)
                self.assertNotIn(url, source.rows)

    def test_missing_malformed_or_unavailable_metadata_cannot_bind_an_alias(self):
        for metadata, status in ((None, None), (repository_metadata(identifier=True), 200), (repository_metadata(identifier=0), 200),
                                 (repository_metadata(identifier=-1), 200), (repository_metadata(identifier="700001"), 200),
                                 (repository_metadata(identifier=700001.0), 200), ({}, 200), ([], 200),
                                 (b'{"id":', 200), (repository_metadata(), 503)):
            with self.subTest(metadata=metadata, status=status), tempfile.TemporaryDirectory() as directory:
                rows = [(START, [1], next_link(NUMERIC), 200)]
                if status is not None:
                    rows.append((REPO, metadata, {}, status))
                source = transport(Path(directory).resolve(), rows)
                with self.assertRaises(ValueError):
                    source.pages(START)
                self.assertNotIn(NUMERIC, source.rows)

    def test_other_repository_metadata_is_not_a_fallback(self):
        with tempfile.TemporaryDirectory() as directory:
            wrong_metadata = "https://api.github.com/repos/other/client"
            source = transport(Path(directory).resolve(), [(START, [1], next_link(NUMERIC), 200),
                               (wrong_metadata, repository_metadata(wrong_metadata), {}, 200)])
            with self.assertRaises(ValueError):
                source.pages(START)
            self.assertNotIn(wrong_metadata, source.rows)
            self.assertNotIn(NUMERIC, source.rows)

    def test_alias_loops_ambiguous_next_links_and_page_limit_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            start = START + "&page=1"
            alias = NUMERIC.replace("page=2", "page=1")
            source = transport(Path(directory).resolve(), [(start, [1], next_link(alias), 200), (REPO, repository_metadata(), {}, 200)])
            with self.assertRaises(ValueError):
                source.pages(start)
            self.assertNotIn(alias, source.rows)
        with tempfile.TemporaryDirectory() as directory:
            headers = {"link": next_link(NUMERIC)["link"] + ", " + next_link(START + "&page=2")["link"]}
            source = transport(Path(directory).resolve(), [(START, [1], headers, 200)])
            with self.assertRaises(ValueError):
                source.pages(START)
            self.assertEqual(set(source.rows), {START})
        with tempfile.TemporaryDirectory() as directory:
            rows = [(START + "&page=" + str(page), [page], next_link(START + "&page=" + str(page + 1)), 200) for page in range(1, 66)]
            source = transport(Path(directory).resolve(), rows)
            with self.assertRaises(ValueError):
                source.pages(START + "&page=1")
            self.assertEqual(len(source.rows), 64)

    def test_image_paging_still_rejects_a_different_endpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            url = "https://registry-1.docker.io/v2/library/example/tags/list"
            other = "https://registry-1.docker.io/v2/library/other/tags/list?page=2"
            source = transport(Path(directory).resolve(), [(url, {"name": "library/example", "tags": ["1.0.0"]}, next_link(other), 200)])
            with self.assertRaises(ValueError):
                source.pages(url, "library/example")
            self.assertNotIn(other, source.rows)

    def test_complete_check_records_metadata_in_component_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            repository = "https://api.github.com/repos/ethereum/go-ethereum"
            start = repository + "/releases?per_page=100"
            second = "https://api.github.com/repositories/700001/releases?per_page=100&page=2"
            fixture.rows[start]["headers"] = next_link(second)
            fixture.add(second, [{"tag_name": "v3.2.1", "draft": False, "prerelease": False}])
            fixture.add(repository, repository_metadata(repository))
            report = root / "report.json"
            result = subprocess.run([sys.executable, "-I", str(PROJECT / "scripts/refresh-versions"), "check",
                "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                "--report", str(report), "--fixture-transport", str(fixture.save())], capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            value = json.loads(report.read_bytes())
            row = next(row for row in value["components"] if row["id"] == "geth")
            self.assertEqual(row["latest_stable"], "v3.2.1")
            self.assertIn(repository, row["sources"])
            self.assertIn(repository, {observation["url"] for observation in value["observations"]})

    def test_metadata_full_name_must_match_configured_repository_exactly(self):
        for name in (None, "other/synthetic-client", "example/other", "Example/synthetic-client", 123):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                metadata = repository_metadata()
                if name is None:
                    metadata.pop("full_name")
                else:
                    metadata["full_name"] = name
                source = transport(Path(directory).resolve(), [(START, [1], next_link(NUMERIC), 200), (REPO, metadata, {}, 200),
                                                               (NUMERIC, [2], {}, 200)])
                with self.assertRaises(ValueError):
                    source.pages(START)
                self.assertNotIn(NUMERIC, source.rows)

    def test_metadata_url_must_be_the_canonical_named_endpoint(self):
        for url in (None, "https://api.github.com/repos/other/client", "https://api.github.com/repositories/700001",
                    REPO + "?page=2", REPO + "#fragment", REPO + "/releases", REPO.replace("https:", "http:"),
                    REPO.replace("api.github.com", "raw.githubusercontent.com"), 123):
            with self.subTest(url=url), tempfile.TemporaryDirectory() as directory:
                metadata = repository_metadata()
                if url is None:
                    metadata.pop("url")
                else:
                    metadata["url"] = url
                source = transport(Path(directory).resolve(), [(START, [1], next_link(NUMERIC), 200), (REPO, metadata, {}, 200),
                                                               (NUMERIC, [2], {}, 200)])
                with self.assertRaises(ValueError):
                    source.pages(START)
                self.assertNotIn(NUMERIC, source.rows)
