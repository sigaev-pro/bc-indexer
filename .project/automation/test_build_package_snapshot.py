"""Disposable retained-source controls; no real signature or native build claim."""

import copy
import gzip
import hashlib
import json
import lzma
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import build_contract as contract
import build_materials as materials
import build_os


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.old = self.root / "old"
        self.old.mkdir()
        self.proof = self.root / "proof"
        self.proof.mkdir()
        self.now = time.time()
        self.report = {"schema_version": 1, "contract": "component-update-v1", "mode": "check",
                       "synthetic": False, "complete": False, "components": [{"id": "docker-engine", "selected": "wrong"}],
                       "observations": []}
        self.pins, self.releases, self.payloads = [], {}, {}
        for number, channel in enumerate(("trixie", "trixie-security", "trixie-updates")):
            base = "https://security.debian.org/debian-security" if channel == "trixie-security" else "https://deb.debian.org/debian"
            suffix = "gz" if channel == "trixie" else "xz"
            url = base + "/dists/" + channel + "/main/binary-amd64/Packages." + suffix
            data = ("Package: synthetic-" + str(number) + "\nVersion: 1\nArchitecture: amd64\nFilename: pool/main/s/synthetic.deb\n"
                    "Size: 8\nSHA256: " + hashlib.sha256(b"!<arch>\n").hexdigest() + "\n\n").encode()
            data = gzip.compress(data) if suffix == "gz" else lzma.compress(data)
            digest = hashlib.sha256(data).hexdigest()
            release = ("Origin: Debian\nCodename: " + channel + "\nDate: Thu, 01 Jan 1970 00:00:01 UTC\n"
                       + ("" if channel == "trixie" else "Valid-Until: Fri, 01 Jan 2100 00:00:00 UTC\n")
                       + "SHA256:\n " + digest + " " + str(len(data)) + " main/binary-amd64/Packages." + suffix + "\n")
            signed = b"SYNTHETIC SIGNATURE TEST INPUT\n" + release.encode()
            inrelease_url = base + "/dists/" + channel + "/InRelease"
            self.releases[signed] = release
            self.payloads.update({url: data, inrelease_url: signed})
            self.pins.append({"url": url, "sha256": digest})
            for index, (address, payload) in enumerate(((url, data), (inrelease_url, signed))):
                path = self.old / (str(number) + "-" + str(index))
                path.write_bytes(payload)
                self.report["observations"].append({"url": address, "status": 200, "headers": {"etag": "original"},
                    "body": contract.reference(self.old, path), "observed_at": 946684800 + number})
        # Unrelated historical report references need not exist or be accepted.
        self.report["observations"].append({"url": "https://api.github.com/repos/moby/moby/releases", "status": 503,
            "headers": {}, "body": {"path": "absent-unrelated", "sha256": "a" * 64, "size_bytes": 1}, "observed_at": 946684800})
        self.path = self.old / "report.json"
        self.save_report()
        self.runner = SimpleNamespace(root=self.proof, plan={"platform": "linux/amd64", "package_indexes": self.pins},
            commands=[], fetches=[], remaining=lambda *args: 30, fetch=self.no_fetch, run=self.fake_run, inside=self.inside)
        self.signature_failure = False
        self.attempt = 0

    def save_report(self):
        self.path.write_bytes(contract.canonical(self.report))
        self.digest = contract.file_hash(self.path)

    def no_fetch(self, *args, **kwargs):
        self.fail("retained package acquisition fetched mutable metadata")

    def fake_run(self, argv, **kwargs):
        self.assertEqual(argv[:3], ["docker", "container", "cp"])
        Path(argv[-1]).write_bytes(b"synthetic image-owned tool")

    def inside(self, container, argv, **kwargs):
        self.assertEqual(argv[:2], ["/usr/bin/sqv", "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp"])
        self.assertEqual(argv[3], "--cleartext")
        if self.signature_failure:
            raise ValueError("synthetic signature rejection")
        signed = (self.proof / argv[4].removeprefix("/proof/")).read_bytes()
        output = self.proof / argv[2].removeprefix("--output=/proof/")
        output.write_text(self.releases[signed])
        self.runner.commands.append({"argv": ["docker", "container", "exec", container, *argv], "exit_code": 0})

    def acquire(self):
        self.attempt += 1
        self.proof = self.root / ("proof-" + str(self.attempt))
        self.proof.mkdir()
        self.runner.root = self.proof
        packages = build_os.Packages(self.runner)
        packages.acquire(self.path, self.digest)
        return packages

    def closure(self, packages):
        signed = packages.authenticate("synthetic-container")
        installed = self.proof / "installed.txt"
        installed.write_text("".join("synthetic-" + str(n) + "\t1\tamd64\n" for n in range(3)))
        archive = self.proof / "synthetic.deb"
        archive.write_bytes(b"!<arch>\n")
        return {"installed": contract.reference(self.proof, installed), "signed_indexes": signed,
                "packages": [{"name": "synthetic-" + str(n), "version": "1", "architecture": "amd64", "index_url": pin["url"],
                              "archive": contract.reference(self.proof, archive)} for n, pin in enumerate(self.pins)]}

    def verify_closure(self, closure):
        return materials.os_closure(closure, self.proof, self.runner.plan, self.now, self.runner.commands)

    def test_exact_retained_inputs_are_copied_and_reauthenticated_without_old_pass(self):
        packages = self.acquire()
        self.assertIsNone(packages.signed)
        shutil.rmtree(self.old)
        closure = self.closure(packages)
        self.assertEqual(len(self.verify_closure(closure)), 3)
        self.assertEqual(len(self.runner.commands), 3)
        self.assertEqual(self.runner.fetches, [])
        for index, expected in zip(closure["signed_indexes"], self.report["observations"][::2]):
            status = json.loads(contract.verify_reference(self.proof, index["signature_log"]).read_bytes())
            acquisition = status["acquisition"]
            self.assertEqual(acquisition["mode"], "retained-report")
            self.assertEqual(acquisition["report"]["sha256"], self.digest)
            self.assertEqual(acquisition["index_observation"], expected)
            self.assertEqual(index["packages"]["sha256"], expected["body"]["sha256"])
            self.assertNotEqual(index["packages"]["path"], expected["body"]["path"])

    def test_report_hash_and_selected_body_hash_cannot_change(self):
        for target in (self.path, self.old / self.report["observations"][0]["body"]["path"]):
            original = target.read_bytes()
            target.write_bytes(original + b"changed")
            with self.subTest(target=target), self.assertRaises(ValueError):
                self.acquire()
            target.write_bytes(original)

    def test_wrong_scope_synthetic_duplicates_missing_and_failed_observations_fail(self):
        controls = [lambda r: r.update(synthetic=True), lambda r: r.update(contract="other"),
                    lambda r: r.update(schema_version=True), lambda r: r["observations"].append(r["observations"][0]),
                    lambda r: r["observations"].pop(1), lambda r: r["observations"][0].update(status=503),
                    lambda r: r["observations"].pop(4), lambda r: r["observations"][0]["body"].update(sha256="f" * 64),
                    lambda r: r["observations"][0].update(url=r["observations"][0]["url"].replace("amd64", "arm64")),
                    lambda r: r["observations"][0]["body"].update(path="../outside"),
                    lambda r: r["observations"][0]["body"].update(path="/outside"),
                    lambda r: r["observations"][-1].update(url="https://example.org/unrelated"),
                    lambda r: r["observations"][0].update(observed_at=float("nan"))]
        original = copy.deepcopy(self.report)
        for mutate in controls:
            self.report = copy.deepcopy(original)
            mutate(self.report)
            if self.report["observations"][0]["observed_at"] != self.report["observations"][0]["observed_at"]:
                self.path.write_text(json.dumps(self.report))
                self.digest = contract.file_hash(self.path)
            else:
                self.save_report()
            with self.subTest(report=self.report), self.assertRaises(ValueError):
                self.acquire()

    def test_symlinks_in_report_or_body_ancestors_are_rejected(self):
        original = self.path
        alias = self.root / "alias"
        alias.symlink_to(self.old, target_is_directory=True)
        self.path = alias / "report.json"
        with self.assertRaises((ValueError, OSError)):
            self.acquire()
        self.path = original
        report_alias = self.old / "report-alias.json"
        report_alias.symlink_to(original)
        self.path = report_alias
        with self.assertRaises((ValueError, OSError)):
            self.acquire()
        self.path = original
        body = self.old / self.report["observations"][0]["body"]["path"]
        moved = self.root / "outside"
        body.rename(moved)
        body.symlink_to(moved)
        with self.assertRaises((ValueError, OSError)):
            self.acquire()
        body.unlink()
        moved.rename(body)
        directory_alias = self.old / "linked-directory"
        directory_alias.symlink_to(self.old, target_is_directory=True)
        self.report["observations"][0]["body"]["path"] = "linked-directory/" + body.name
        self.save_report()
        with self.assertRaises((ValueError, OSError)):
            self.acquire()

    def test_fifo_is_rejected_without_reading_it(self):
        body = self.old / self.report["observations"][0]["body"]["path"]
        body.unlink()
        os.mkfifo(body)
        with self.assertRaises((ValueError, OSError)):
            self.acquire()

    def test_input_bounds_are_checked_before_copying(self):
        for constant in ("MAX_SOURCE_REPORT", "MAX_PACKAGE_INDEX", "MAX_INRELEASE"):
            with self.subTest(constant=constant), patch.object(build_os, constant, 1):
                with self.assertRaises(ValueError):
                    self.acquire()

    def test_signature_failure_never_authorizes_installation(self):
        packages = self.acquire()
        self.signature_failure = True
        with self.assertRaisesRegex(ValueError, "signature rejection"):
            packages.authenticate("synthetic-container")
        self.assertIsNone(packages.signed)
        with self.assertRaisesRegex(ValueError, "prior cryptographic"):
            packages.install("synthetic-container", self.proof, [])

    def test_mixed_publication_expiry_and_channel_fail_after_signature_step(self):
        for change in (lambda text: text.replace(self.pins[1]["sha256"], "f" * 64),
                       lambda text: text.replace("2100", "2000"),
                       lambda text: text.replace("Date: Thu, 01 Jan 1970", "Date: Fri, 01 Jan 2100"),
                       lambda text: text.replace("Codename: trixie-security", "Codename: trixie-updates")):
            with self.subTest(change=change):
                packages = self.acquire()
                original = dict(self.releases)
                self.releases = {signed: change(text) for signed, text in original.items()}
                with self.assertRaises(ValueError):
                    packages.authenticate("synthetic-container")
                self.assertIsNone(packages.signed)
                self.releases = original
                shutil.rmtree(self.proof)
                self.proof.mkdir()

    def test_provenance_tampering_fails_even_with_new_signature_log_hash(self):
        closure = self.closure(self.acquire())
        index = closure["signed_indexes"][0]
        path = contract.verify_reference(self.proof, index["signature_log"])
        status = json.loads(path.read_bytes())
        for mutate in (lambda a: a["index_observation"].update(observed_at=self.now),
                       lambda a: a["inrelease_observation"].update(url=self.pins[1]["url"]),
                       lambda a: a["index_observation"]["body"].update(sha256="f" * 64),
                       lambda a: a.update(mode="live-fetch")):
            wrong = copy.deepcopy(status)
            mutate(wrong["acquisition"])
            path.write_bytes(contract.canonical(wrong))
            index["signature_log"] = contract.reference(self.proof, path)
            with self.subTest(status=wrong), self.assertRaises(ValueError):
                self.verify_closure(closure)

    def test_copied_report_and_original_body_binding_remain_required(self):
        closure = self.closure(self.acquire())
        index = closure["signed_indexes"][0]
        log_path = contract.verify_reference(self.proof, index["signature_log"])
        status = json.loads(log_path.read_bytes())
        report_path = contract.verify_reference(self.proof, status["acquisition"]["report"])
        original = report_path.read_bytes()
        report_path.write_bytes(original + b" ")
        with self.assertRaisesRegex(ValueError, "artifact bytes differ"):
            self.verify_closure(closure)
        report_path.write_bytes(original)
        for field in ("packages", "inrelease"):
            copied = contract.verify_reference(self.proof, index[field])
            data = copied.read_bytes()
            copied.write_bytes(data + b"changed")
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "artifact bytes differ"):
                self.verify_closure(closure)
            copied.write_bytes(data)
        status["acquisition"]["inrelease_observation"]["body"]["size_bytes"] += 1
        log_path.write_bytes(contract.canonical(status))
        index["signature_log"] = contract.reference(self.proof, log_path)
        with self.assertRaisesRegex(ValueError, "observation metadata changed"):
            self.verify_closure(closure)

    def test_ordinary_fetch_still_requires_current_exact_bytes_and_new_signature(self):
        fetched = []
        def fetch(url, path, expected_sha256=None, maximum=None):
            fetched.append(url)
            data = self.payloads[url]
            if expected_sha256 is not None:
                self.assertEqual(hashlib.sha256(data).hexdigest(), expected_sha256)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            return path
        self.runner.fetch = fetch
        packages = build_os.Packages(self.runner)
        packages.acquire()
        self.assertEqual(len(fetched), 6)
        closure = self.closure(packages)
        self.assertEqual(len(self.verify_closure(closure)), 3)
        for index in closure["signed_indexes"]:
            status = json.loads(contract.verify_reference(self.proof, index["signature_log"]).read_bytes())
            self.assertEqual(status["acquisition"], {"mode": "live-fetch"})

    def test_stop_or_copy_interruption_never_enables_authentication(self):
        packages = build_os.Packages(self.runner)
        with patch.object(self.runner, "remaining", side_effect=ValueError("stopped")):
            with self.assertRaisesRegex(ValueError, "stopped"):
                packages.acquire(self.path, self.digest)
        self.assertEqual(packages.indexes, [])
        self.assertIsNone(packages.signed)
        with self.assertRaisesRegex(ValueError, "acquisition is incomplete"):
            packages.authenticate("synthetic-container")
        self.assertEqual(self.runner.commands, [])
        with patch.object(build_os, "copy_source", side_effect=OSError("copy interrupted")):
            with self.assertRaisesRegex(OSError, "copy interrupted"):
                packages.acquire(self.path, self.digest)
        self.assertEqual(packages.indexes, [])
        self.assertIsNone(packages.signed)

    def test_cli_requires_both_input_options_before_execution(self):
        script = Path(build_os.__file__).with_name("build_task_verifier.py")
        for option, value in (("--package-source-report", str(self.path)), ("--package-source-report-sha256", self.digest)):
            process = subprocess.run([sys.executable, "-I", str(script), "--candidate", str(self.root / "missing"),
                                      "--request", str(self.root / "missing.json"), option, value],
                                     capture_output=True, text=True, timeout=10)
            self.assertEqual(process.returncode, 2)
            self.assertIn("must be supplied together", process.stderr)


if __name__ == "__main__":
    unittest.main()
