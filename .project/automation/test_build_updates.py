"""Synthetic 41-component source controls, not live updater acceptance."""

import copy
import gzip
import hashlib
import json
import lzma
from pathlib import Path
import tempfile
import unittest

import build_contract as contract
import build_update_vectors as vectors
import build_updates as updates


SOURCE = Path(__file__).resolve().parents[2]


class UpdateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.fixture = vectors.fixture(self.temporary.name, SOURCE)

    def validate(self, report=None, mode="check", synthetic=True):
        f = self.fixture
        return updates.validate_report(report or f["report"], f["root"], f["registry"], f["active"],
                                       f["registry_hash"], f["lock_hash"], mode, synthetic, vectors.FIXTURE_TIME)

    def row(self, report, name):
        return next(row for row in report["components"] if row["id"] == name)

    def package_observations(self):
        transport = json.loads(self.fixture["transport"].read_bytes())
        self.assertTrue(transport["synthetic"])
        return {row["url"]: row for row in transport["observations"]
                if row["url"].endswith(("/Packages.gz", "/Packages.xz"))}

    def report_with_indexes(self, urls, mode="check"):
        observations = self.package_observations()
        indexes = [{"url": url, "sha256": hashlib.sha256(
            (self.fixture["root"] / observations[url]["body"]["path"]).read_bytes()).hexdigest()} for url in urls]
        report = copy.deepcopy(self.fixture["report"])
        for row in report["components"]:
            if "package_indexes" in row["selected"]:
                row["selected"]["package_indexes"] = copy.deepcopy(indexes)
                row["sources"] = [urls[0]]
        if mode == "update":
            candidate = copy.deepcopy(self.fixture["candidate"])
            candidate.update(package_indexes=indexes, components={row["id"]: row["selected"] for row in report["components"]})
            path = self.fixture["root"] / "selected-candidate.json"
            path.write_bytes(contract.canonical(candidate))
            report.update(mode=mode, candidate=contract.reference(self.fixture["root"], path))
        return report

    def test_transport_covers_every_active_lock_package_index_url(self):
        observations = self.package_observations()
        active_urls = {row["url"] for row in self.fixture["active"]["package_indexes"]}
        self.assertEqual(len(active_urls), 6)
        self.assertFalse(active_urls - observations.keys(), sorted(active_urls - observations.keys()))
        expected = set()
        for channel in ("trixie", "trixie-security", "trixie-updates"):
            base = "https://security.debian.org/debian-security" if channel == "trixie-security" else "https://deb.debian.org/debian"
            for architecture in ("amd64", "arm64"):
                for suffix in ("gz", "xz"):
                    expected.add(base + "/dists/" + channel + "/main/binary-" + architecture + "/Packages." + suffix)
        self.assertEqual(set(observations), expected)

    def test_compressed_package_bodies_decode_equally_and_bind_exact_hashes(self):
        observations = self.package_observations()
        packages = {row["source"]["package"] for row in self.fixture["registry"]["components"]
                    if row["pin_kind"] == "distribution_package"}
        for index in self.fixture["candidate"]["package_indexes"]:
            prefix = index["url"].rsplit(".", 1)[0]
            decoded, hashes = [], []
            for suffix, decompress in (("gz", gzip.decompress), ("xz", lzma.decompress)):
                with self.subTest(url=prefix, suffix=suffix):
                    observation = observations[prefix + "." + suffix]
                    self.assertEqual(observation["status"], 200)
                    reference = observation["body"]
                    payload = (self.fixture["root"] / reference["path"]).read_bytes()
                    digest = hashlib.sha256(payload).hexdigest()
                    self.assertEqual(reference["sha256"], digest)
                    self.assertEqual(reference["size_bytes"], len(payload))
                    decoded.append(decompress(payload))
                    hashes.append(digest)
            self.assertEqual(decoded[0], decoded[1])
            self.assertNotEqual(hashes[0], hashes[1])
            self.assertEqual(index["sha256"], hashes[0])
            records = [dict(line.split(": ", 1) for line in stanza.splitlines())
                       for stanza in decoded[0].decode().strip().split("\n\n")]
            self.assertEqual({row["Package"] for row in records}, packages)
            self.assertEqual({row["Architecture"] for row in records}, {prefix.split("/binary-", 1)[1].split("/", 1)[0]})

    def test_check_and_update_accept_either_compression_and_active_lock_urls(self):
        active_urls = [row["url"] for row in self.fixture["active"]["package_indexes"]]
        choices = {suffix: [url.rsplit(".", 1)[0] + "." + suffix for url in active_urls] for suffix in ("gz", "xz")}
        choices["active-lock"] = active_urls
        for label, urls in choices.items():
            for mode in ("check", "update"):
                with self.subTest(selection=label, mode=mode):
                    result = self.validate(self.report_with_indexes(urls, mode), mode=mode)
                    self.assertTrue(result["complete"])
                    self.assertEqual(len(result["selected"]), 41)
                    self.assertEqual({row["index_url"] for row in result["bindings"]}, set(urls))
                    self.assertTrue(all(row["compatibility"] == "NOT_RUN" for row in result["selected"].values()))

    def test_selected_index_hash_cannot_use_the_other_compressed_representation(self):
        observations = self.package_observations()
        active_urls = [row["url"] for row in self.fixture["active"]["package_indexes"]]
        for suffix, other in (("gz", "xz"), ("xz", "gz")):
            with self.subTest(suffix=suffix):
                urls = [url.rsplit(".", 1)[0] + "." + suffix for url in active_urls]
                report = self.report_with_indexes(urls)
                self.assertTrue(self.validate(report)["complete"])
                wrong = observations[urls[0].rsplit(".", 1)[0] + "." + other]["body"]["sha256"]
                for row in report["components"]:
                    if "package_indexes" in row["selected"]:
                        row["selected"]["package_indexes"][0]["sha256"] = wrong
                with self.assertRaisesRegex(ValueError, "package index hash differs"):
                    self.validate(report)

    def chart_source_report(self):
        report = copy.deepcopy(self.fixture["report"])
        row = self.row(report, "nats-chart")
        row["sources"].append(row["selected"]["chart_metadata"]["url"])
        return report

    def test_chart_source_is_authenticated_before_checking_report_references(self):
        for mode in ("check", "update"):
            with self.subTest(mode=mode):
                report = self.chart_source_report()
                url = self.row(report, "nats-chart")["selected"]["chart_metadata"]["url"]
                if mode == "update":
                    path = self.fixture["root"] / "chart-candidate.json"
                    path.write_bytes(contract.canonical(self.fixture["candidate"]))
                    report.update(mode=mode, candidate=contract.reference(self.fixture["root"], path))
                result = self.validate(report, mode=mode)
                self.assertTrue(result["complete"])
                self.assertIn(url, result["observations"].used)
                self.assertEqual(len(result["selected"]), 41)

    def test_declared_chart_metadata_must_match_authenticated_bytes(self):
        for field, value in (("version", "0.0.1"), ("app_version", "0.0.1"),
                             ("url", "https://raw.githubusercontent.com/nats-io/k8s/main/Chart.yaml"),
                             ("sha256", "0" * 64)):
            with self.subTest(field=field):
                report = self.chart_source_report()
                self.row(report, "nats-chart")["selected"]["chart_metadata"][field] = value
                with self.assertRaisesRegex(ValueError, "NATS chart is not compatible"):
                    self.validate(report)

    def test_changed_chart_bytes_cannot_bypass_version_and_server_checks_with_new_hashes(self):
        for field in ("version", "app_version", "duplicate_version", "missing_version"):
            with self.subTest(field=field):
                report = self.chart_source_report()
                metadata = self.row(report, "nats-chart")["selected"]["chart_metadata"]
                if field in ("version", "app_version"):
                    metadata[field] = "0.0.1"
                lines = ["name: nats", "version: " + metadata["version"], "appVersion: " + metadata["app_version"]]
                if field == "duplicate_version":
                    lines.append(lines[1])
                if field == "missing_version":
                    lines.pop(1)
                path = self.fixture["root"] / ("changed-chart-" + field + ".yaml")
                path.write_bytes(("\n".join(lines) + "\n").encode())
                observation = next(row for row in report["observations"] if row["url"] == metadata["url"])
                observation["body"] = contract.reference(self.fixture["root"], path)
                metadata["sha256"] = hashlib.sha256(path.read_bytes()).hexdigest()
                message = "not compatible" if field in ("version", "app_version") else "no unique version metadata"
                with self.assertRaisesRegex(ValueError, message):
                    self.validate(report)

    def test_chart_source_requires_present_exact_bytes(self):
        for mutation in ("missing_observation", "missing_file", "forged_body_hash", "forged_bytes"):
            with self.subTest(mutation=mutation):
                report = self.chart_source_report()
                url = self.row(report, "nats-chart")["selected"]["chart_metadata"]["url"]
                observation = next(row for row in report["observations"] if row["url"] == url)
                if mutation == "missing_observation":
                    report["observations"].remove(observation)
                elif mutation == "missing_file":
                    observation["body"]["path"] = "absent-chart.yaml"
                elif mutation == "forged_body_hash":
                    observation["body"]["sha256"] = "0" * 64
                else:
                    path = self.fixture["root"] / "forged-chart.yaml"
                    path.write_bytes(b"forged chart bytes\n")
                    observation["body"]["path"] = path.name
                with self.assertRaises(ValueError):
                    self.validate(report)

    def test_recorded_but_unvalidated_source_cannot_be_claimed_as_provenance(self):
        report = self.chart_source_report()
        chart = self.row(report, "nats-chart")
        url = chart["selected"]["chart_metadata"]["url"].rsplit("/", 1)[0] + "/unvalidated.yaml"
        path = self.fixture["root"] / "unvalidated.yaml"
        path.write_bytes(b"Recorded bytes are not authenticated chart metadata.\n")
        report["observations"].append({"url": url, "status": 200, "headers": {},
                                       "body": contract.reference(self.fixture["root"], path), "observed_at": vectors.FIXTURE_TIME})
        result = self.validate(report)
        self.assertTrue(result["complete"])
        self.assertNotIn(url, result["observations"].used)
        chart["sources"].append(url)
        with self.assertRaisesRegex(ValueError, "source references are missing or unverified"):
            self.validate(report)

    def test_complete_synthetic_check_covers_every_source_kind(self):
        result = self.validate()
        self.assertTrue(result["complete"])
        self.assertEqual(len(result["selected"]), 41)
        self.assertEqual(len(result["bindings"]), 6)
        self.assertGreater(len(result["observations"].used), 100)
        self.assertTrue(self.fixture["transport"].is_file())
        self.assertFalse((self.fixture["root"] / "candidate.json").exists())

    def test_update_candidate_is_separate_exact_and_not_promoted(self):
        report = copy.deepcopy(self.fixture["report"])
        path = self.fixture["root"] / "candidate.json"
        path.write_bytes(contract.canonical(self.fixture["candidate"]))
        report.update(mode="update", candidate=contract.reference(self.fixture["root"], path))
        self.assertTrue(self.validate(report, mode="update")["complete"])
        candidate = copy.deepcopy(self.fixture["candidate"])
        candidate["active_lock_changed"] = True
        path.write_bytes(contract.canonical(candidate))
        report["candidate"] = contract.reference(self.fixture["root"], path)
        with self.assertRaises(ValueError):
            self.validate(report, mode="update")

    def test_current_lock_and_all_component_coverage_are_required(self):
        for mutation in (lambda r: r["components"].pop(), lambda r: r["components"].append(r["components"][0]),
                         lambda r: r.update(active_lock_after="0" * 64), lambda r: r.update(registry_sha256="0" * 64),
                         lambda r: r.update(schema_version=True), lambda r: r["generated_outputs"].pop(),
                         lambda r: r["external_locks"].pop(), lambda r: r["components"][0].update(current="wrong")):
            report = copy.deepcopy(self.fixture["report"])
            mutation(report)
            with self.subTest(report=report), self.assertRaises(ValueError):
                self.validate(report)

    def test_unavailable_source_cannot_claim_complete(self):
        report = copy.deepcopy(self.fixture["report"])
        report["observations"][0]["status"] = 503
        with self.assertRaises(ValueError):
            self.validate(report)
        report.update(complete=False)
        self.row(report, "geth").update(selected=None, blocked_reasons=["Official source returned 503."])
        self.assertFalse(self.validate(report)["complete"])

    def test_all_stable_pins_and_component_image_matches_are_checked(self):
        mutations = [("geth", lambda r: r["selected"].update(version="v999.0.0-rc.1")),
                     ("geth", lambda r: r["selected"].update(commit="f" * 40)),
                     ("geth", lambda r: r.update(latest_stable="latest")),
                     ("protoc-gen-go-grpc", lambda r: r["selected"].update(version="v88.2.0")),
                     ("bor-cometbft", lambda r: r["selected"].update(version="v88.2.0-polygon")),
                     ("heimdall-cosmos-sdk", lambda r: r["selected"].update(owner_component="bor")),
                     ("golang-build-image", lambda r: r["selected"].update(pull_reference="golang:latest")),
                     ("nats-runtime-image", lambda r: r["selected"].update(version="1.0.0-alpine")),
                     ("debian-runtime-image", lambda r: r["selected"]["platform_manifests"].pop("linux/arm64")),
                     ("debian-libzstd-dev", lambda r: r["selected"]["version_by_platform"].update({"linux/arm64": "88.2"})),
                     ("go", lambda r: r["selected"]["artifacts"][0].update(sha256="0" * 64)),
                     ("nats-chart", lambda r: r["selected"]["chart_metadata"].update(app_version="1.0.0"))]
        for identifier, mutation in mutations:
            report = copy.deepcopy(self.fixture["report"])
            mutation(self.row(report, identifier))
            with self.subTest(component=identifier), self.assertRaises(ValueError):
                self.validate(report)

    def test_owner_versions_cannot_be_swapped_between_native_graphs(self):
        report = copy.deepcopy(self.fixture["report"])
        bor = self.row(report, "bor-cometbft")["selected"]
        other = self.row(report, "heimdall-cometbft")["selected"]
        bor.update(version=other["version"], commit=other["commit"])
        with self.assertRaises(ValueError):
            self.validate(report)

    def test_materialized_source_bytes_and_pagination_are_required(self):
        report = copy.deepcopy(self.fixture["report"])
        report["observations"][0]["body"]["sha256"] = "0" * 64
        with self.assertRaises(ValueError):
            self.validate(report)
        report = copy.deepcopy(self.fixture["report"])
        first = report["observations"][0]
        first["headers"]["link"] = '<' + first["url"] + '&page=2>; rel="next"'
        with self.assertRaises(ValueError):
            self.validate(report)
        with self.assertRaises(ValueError):
            self.validate(synthetic=False)

    def test_debian_revision_order_preserves_epoch_tilde_and_security_updates(self):
        for left, right in (("1.0~rc1-1", "1.0-1"), ("1.0-1", "1.0-1+deb13u1"),
                            ("1.0-9", "1.0-10"), ("99.0-1", "1:1.0-1"), ("1.0-1", "1.0-1+b1")):
            self.assertLess(updates.debian_compare(left, right), 0)
            self.assertGreater(updates.debian_compare(right, left), 0)
        self.assertEqual(updates.debian_compare("1.00-01", "1.0-1"), 0)


if __name__ == "__main__":
    unittest.main()
