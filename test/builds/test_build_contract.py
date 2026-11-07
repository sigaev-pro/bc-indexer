"""Independent product protocol checks; no native build or live-source claims."""

import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from fixtures import Files, OfficialFixture, PROJECT, REGISTRY, LOCK, hash_bytes, manifest, packed


def call(script, value=None, args=()):
    return subprocess.run([sys.executable, "-I", str(PROJECT / script), *map(str, args)], input=packed(value) if value is not None else None,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=40, cwd="/")


class BuildTests(unittest.TestCase):
    def request(self, platform="linux/amd64"):
        return dict(contract="build-artifacts-v1", kind="BuildPlan", registry_path=str(REGISTRY), lock_path=str(LOCK), platform=platform,
                    limits=dict(cpus=4, memory_bytes=6 * 1024**3, pids=256, command_seconds=3600))

    def test_plan_coverage_both_platforms(self):
        for platform in ("linux/amd64", "linux/arm64"):
            result = call("build/native/plan.py", self.request(platform))
            self.assertEqual(result.returncode, 0, result.stderr)
            plan = json.loads(result.stdout)
            self.assertEqual(plan["platform"], platform)
            self.assertEqual(len(plan["sources"]), 10)
            self.assertEqual(len(plan["profiles"]), 7)
            self.assertEqual(len(plan["native_components"]), 5)
            self.assertEqual(len(plan["package_indexes"]), 3)
            self.assertEqual(plan["runtime"], "NOT_RUN")
            self.assertEqual(plan["lock_sha256"], hash_bytes(LOCK.read_bytes()))
            self.assertNotEqual(plan["sources"]["bsc"]["commit"], plan["sources"]["geth"]["commit"])
            l1 = [p["supporting_l1"] for p in plan["profiles"] if p["supporting_l1"]]
            self.assertEqual({p["profile"] for p in l1}, {"ethereum-mainnet", "ethereum-sepolia"})

    def test_plan_closed_limits_and_identity(self):
        mutations = [lambda r: r.update(extra=True), lambda r: r.update(platform="linux/riscv64"),
                     lambda r: r["limits"].update(cpus=True), lambda r: r["limits"].update(command_seconds=3601),
                     lambda r: r["limits"].update(memory_bytes=1), lambda r: r.update(registry_path="relative.json")]
        for mutation in mutations:
            request = self.request()
            mutation(request)
            self.assertEqual(call("build/native/plan.py", request).returncode, 2)

    def test_duplicate_nonfinite_json_rejected(self):
        for data in (b'{"contract":1,"contract":2}', b'{"x":NaN}', b'{"x":1e999}'):
            result = subprocess.run([sys.executable, "-I", str(PROJECT / "build/native/plan.py")], input=data, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 2)

    def test_preflight_materialized_controls(self):
        plan = json.loads(call("build/native/plan.py", self.request()).stdout)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            files = Files(root)
            value = manifest(plan, files)
            request = dict(contract="build-artifacts-v1", kind="VerifyManifest", synthetic=True, root=str(root), manifest_path="manifest.json",
                           profile_id="polygon-pos-amoy", registry_path=str(REGISTRY), lock_path=str(LOCK), identity=value["identity"])
            def verify(candidate, changes=None):
                data = packed(candidate)
                (root / "manifest.json").write_bytes(data)
                current = request | dict(manifest_sha256=hash_bytes(data))
                current.update(changes or {})
                return call("build/native/verify.py", current)
            self.assertEqual(verify(value).returncode, 0)
            self.assertEqual(verify(value, {"synthetic": False}).returncode, 2)
            self.assertEqual(verify(value, {"profile_id": "polygon-other"}).returncode, 2)
            self.assertEqual(verify(value, {"manifest_sha256": "0" * 64}).returncode, 2)
            mutations = [lambda m: m.update(schema_version=True), lambda m: m.update(extra=1), lambda m: m["builds"].pop("prysm"),
                         lambda m: m["module_closures"].pop("nats-go"), lambda m: m["identity"].update(head_sha="a" * 40),
                         lambda m: m["profile_bindings"][-1].update(native_binary_sha256="9" * 64),
                         lambda m: m["commands"][0].update(exit_code=1), lambda m: m["commands"][0].update(started_at=200),
                         lambda m: m["builds"]["geth"]["binary"].update(path="../escape"), lambda m: m["updater"].update(active_lock_after="0" * 64)]
            for mutation in mutations:
                candidate = copy.deepcopy(value)
                mutation(candidate)
                result = verify(candidate)
                self.assertEqual(result.returncode, 2, result.stdout)
                self.assertFalse(json.loads(result.stdout)["valid"])
            target = root / value["builds"]["geth"]["binary"]["path"]
            saved = target.read_bytes()
            target.write_bytes(b"changed")
            self.assertEqual(verify(value).returncode, 2)
            target.unlink()
            replacement = root / "replacement"
            replacement.write_bytes(saved)
            target.symlink_to(replacement)
            self.assertEqual(verify(value).returncode, 2)


class UpdateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.fixture = OfficialFixture(self.root)
        self.before = LOCK.read_bytes()
        self.before_stat = LOCK.stat()

    def invoke(self, mode="check", extra=()):
        args = [mode, "--registry", REGISTRY, "--active-lock", LOCK, "--expected-lock-sha256", hash_bytes(self.before),
                "--report", self.root / "report.json", "--fixture-transport", self.fixture.save()]
        if mode == "update":
            args += ["--output", self.root / "candidate.json"]
        return call("scripts/refresh-versions", args=args + list(extra))

    def test_complete_check_and_update(self):
        for mode in ("check", "update"):
            with self.subTest(mode=mode):
                result = self.invoke(mode)
                self.assertEqual(result.returncode, 0, result.stderr)
                report = json.loads((self.root / "report.json").read_bytes())
                self.assertTrue(report["complete"])
                self.assertTrue(report["synthetic"])
                self.assertEqual(len(report["components"]), 41)
                self.assertTrue(all(r["compatibility"] == "NOT_RUN" and r["sources"] for r in report["components"]))
                self.assertEqual(LOCK.read_bytes(), self.before)
                self.assertEqual(LOCK.stat().st_mtime_ns, self.before_stat.st_mtime_ns)
                if mode == "check":
                    self.assertIsNone(report["candidate"])
                    self.assertFalse((self.root / "candidate.json").exists())
                    (self.root / "report.json").unlink()
                else:
                    candidate = json.loads((self.root / "candidate.json").read_bytes())
                    self.assertEqual(candidate["status"], "PREPARED_CANDIDATE")
                    self.assertFalse(candidate["release_ready"])
                    self.assertTrue(all(p["build"] == p["runtime"] == "NOT_RUN" for p in candidate["profiles"]))

    def test_unavailable_source_no_candidate(self):
        self.fixture.rows["https://go.dev/dl/?mode=json"]["status"] = 503
        result = self.invoke("update")
        self.assertEqual(result.returncode, 1, result.stderr)
        report = json.loads((self.root / "report.json").read_bytes())
        self.assertFalse(report["complete"])
        self.assertTrue(next(r for r in report["components"] if r["id"] == "go")["blocked_reasons"])
        self.assertFalse((self.root / "candidate.json").exists())

    def test_missing_source_no_candidate(self):
        del self.fixture.rows["https://go.dev/dl/?mode=json"]
        self.assertEqual(self.invoke("update").returncode, 1)
        self.assertFalse((self.root / "candidate.json").exists())

    def test_changed_fixture_bytes_rejected(self):
        row = self.fixture.rows["https://go.dev/dl/?mode=json"]
        (self.root / row["body"]["path"]).write_bytes(b"[]")
        self.assertEqual(self.invoke("update").returncode, 2)
        self.assertFalse((self.root / "candidate.json").exists())

    def test_stale_lock_and_active_overwrite_rejected(self):
        self.assertEqual(self.invoke(extra=["--expected-lock-sha256", "0" * 64]).returncode, 2)
        self.assertEqual(self.invoke("update", extra=["--output", LOCK]).returncode, 2)
        self.assertEqual(self.invoke(extra=["--report", LOCK]).returncode, 2)
        self.assertEqual(LOCK.read_bytes(), self.before)
        self.assertEqual(LOCK.stat().st_mtime_ns, self.before_stat.st_mtime_ns)

    def test_tag_is_not_commit(self):
        url = "https://api.github.com/repos/ethereum/go-ethereum/git/tags/" + "a" * 40
        self.fixture.add(url, {"object": {"type": "tree", "sha": "b" * 40}})
        self.assertEqual(self.invoke("update").returncode, 1)
        self.assertFalse((self.root / "candidate.json").exists())

    def test_image_component_version_mismatch(self):
        url = "https://registry-1.docker.io/v2/library/golang/tags/list"
        self.fixture.add(url, dict(name="library/golang", tags=["latest", "1.0.0-trixie"]))
        self.assertEqual(self.invoke("update").returncode, 1)

    def test_pagination_cycle(self):
        url = "https://api.github.com/repos/ethereum/go-ethereum/releases?per_page=100"
        self.fixture.rows[url]["headers"]["link"] = '<' + url + '>; rel="next"'
        self.assertEqual(self.invoke("update").returncode, 1)

    def test_unstable_only_release(self):
        url = "https://api.github.com/repos/nats-io/nats-server/releases?per_page=100"
        self.fixture.add(url, [dict(tag_name="v9.0.0-beta", draft=False, prerelease=True)])
        self.assertEqual(self.invoke("update").returncode, 1)

    def test_download_checksum_mismatch(self):
        self.fixture.add("https://go.dev/dl/go3.2.1.linux-arm64.tar.gz", b"changed archive")
        result = self.invoke("update")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertFalse((self.root / "candidate.json").exists())

    def test_image_platform_bytes_mismatch(self):
        url = next(url for url in self.fixture.rows if "/library/golang/manifests/sha256:" in url)
        self.fixture.add(url, packed(dict(schemaVersion=2, changed=True)))
        self.assertEqual(self.invoke("update").returncode, 1)

    def test_fixture_unknown_fields_and_boolean_status(self):
        row = self.fixture.rows["https://go.dev/dl/?mode=json"]
        row["extra"] = True
        self.assertEqual(self.invoke().returncode, 2)
        del row["extra"]
        row["status"] = True
        self.assertEqual(self.invoke().returncode, 2)

    def test_native_bundle_pseudo_version_binding(self):
        native = next(e for e in self.fixture.registry["components"] if e["id"] == "bor")["source"]
        url = next(url for url in self.fixture.rows if url.startswith("https://raw.githubusercontent.com/" + native["repository"] + "/") and url.endswith("/go.mod"))
        body = (self.root / self.fixture.rows[url]["body"]["path"]).read_bytes()
        short = "123456789abc"
        pseudo = "v0.1.0-20200102030405-" + short
        body = body.replace(b"github.com/0xPolygon/cometbft v3.2.1", ("github.com/0xPolygon/cometbft " + pseudo).encode())
        self.fixture.add(url, body)
        self.fixture.add("https://api.github.com/repos/0xPolygon/cometbft/commits/" + short,
                         dict(sha=short + "d" * 28, commit=dict(committer=dict(date="2020-01-02T03:04:05Z"))))
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads((self.root / "report.json").read_bytes())
        selected = next(r["selected"] for r in report["components"] if r["id"] == "bor-cometbft")
        self.assertEqual(selected["version"], pseudo)
        self.assertEqual(selected["owner_component"], "bor")
        (self.root / "report.json").unlink()
        self.fixture.add("https://api.github.com/repos/0xPolygon/cometbft/commits/" + short,
                         dict(sha=short + "d" * 28, commit=dict(committer=dict(date="2020-01-02T03:04:06Z"))))
        self.assertEqual(self.invoke("update").returncode, 1)

    def test_debian_epoch_tilde_and_revision_order(self):
        # Import only product-owned helpers; no trusted test or oracle imports.
        sys.path[:0] = [str(PROJECT / "scripts/versioning"), str(PROJECT / "build/native")]
        from sources import debian_order
        for lower, higher in (("1.0~rc1-1", "1.0-1"), ("1.0-9", "1.0-10"), ("99.0-1", "1:1.0-1"), ("1.0-1", "1.0-1+b1")):
            self.assertLess(debian_order(lower, higher), 0)
            self.assertGreater(debian_order(higher, lower), 0)
        self.assertEqual(debian_order("1.01-1", "1.1-1"), 0)


if __name__ == "__main__":
    unittest.main()
