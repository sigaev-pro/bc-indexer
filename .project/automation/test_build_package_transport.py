"""Deterministic package transport faults; no live fetch, Docker, or build."""

import copy
import gzip
import hashlib
import http.client
import json
from pathlib import Path
import ssl
import tempfile
import time
import unittest
import urllib.error
from unittest.mock import patch

import build_contract as contract
import build_execution as execution
import build_materials as materials
import evidence


URL = "https://deb.debian.org/debian/pool/main/n/nettle/libnettle8t64_3.10.1-1_amd64.deb"
BODY = b"!<arch>\nsynthetic package bytes"
DIGEST = hashlib.sha256(BODY).hexdigest()


def eof():
    error = ssl.SSLEOFError(ssl.SSL_ERROR_EOF, "not retained: private diagnostic")
    error.reason = "UNEXPECTED_EOF_WHILE_READING"
    return error


class Response:
    def __init__(self, parts=None, status=200, url=URL):
        self.parts = list(parts if parts is not None else [BODY, b""])
        self.status, self.url = status, url
        self.headers = {"ETag": "original", "Authorization": "must not be recorded"}

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False

    def read(self, maximum):
        part = self.parts.pop(0)
        if isinstance(part, BaseException):
            raise part
        return part


class PackageTransportTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.runner = execution.Runner.__new__(execution.Runner)
        self.runner.root = self.root
        self.runner.plan = {"limits": dict(contract.DEFAULT_LIMITS)}
        self.runner.deadline = time.time() + 30
        self.runner.monotonic_deadline = time.monotonic() + 30
        self.runner.request = {"stop_file": str(self.root / "stop")}
        self.runner.fetches, self.runner.package_fetches = [], []
        self.runner.package_journal_reference = None
        self.runner.package_fetches_sealed = False
        self.output = self.root / "mirror/package.deb"

    def fetch(self, **kwargs):
        return self.runner.fetch(URL, self.output, expected_sha256=DIGEST, maximum=len(BODY), retry_package_archive=True, **kwargs)

    def journal(self):
        return json.loads((self.root / "package-fetch-journal.json").read_bytes())

    def closure(self):
        directory = self.root / "os"
        directory.mkdir(exist_ok=True)
        index_url = "https://deb.debian.org/debian/dists/trixie/main/binary-amd64/Packages.gz"
        packages = directory / "Packages.gz"
        packages.write_bytes(gzip.compress(("Package: libnettle8t64\nVersion: 3.10.1-1\nArchitecture: amd64\nFilename: "
            + URL.split("/debian/", 1)[1] + "\nSize: " + str(len(BODY)) + "\nSHA256: " + DIGEST + "\n\n").encode()))
        index = {"url": index_url, "packages": contract.reference(self.root, packages)}
        for name in ("inrelease", "release", "keyring", "verifier"):
            path = directory / name
            path.write_bytes(b"synthetic signature fixture")
            if name == "release":
                path.write_text("Origin: Debian\nCodename: trixie\nDate: Sat, 01 Jan 2000 00:00:00 UTC\nSHA256:\n "
                    + index["packages"]["sha256"] + " " + str(packages.stat().st_size) + " main/binary-amd64/Packages.gz\n")
            index[name] = contract.reference(self.root, path)
        self.runner.commands = [{"argv": ["docker", "container", "exec", "synthetic-container", "/usr/bin/sqv",
            "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp", "--output=/proof/os/release", "--cleartext", "/proof/os/inrelease"], "exit_code": 0}]
        status = {"tool": "sqv", "exit_code": 0, "command_index": 0, "acquisition": {"mode": "live-fetch"},
                  **{key + "_sha256": index[key]["sha256"] for key in ("verifier", "keyring", "inrelease", "release")}}
        path = directory / "signature-original.json"
        path.write_bytes(contract.canonical(status))
        index["signature_log"] = contract.reference(self.root, path)
        installed = directory / "installed"
        installed.write_text("libnettle8t64\t3.10.1-1\tamd64\n")
        return {"signed_indexes": [index], "installed": contract.reference(self.root, installed),
                "packages": [{"name": "libnettle8t64", "version": "3.10.1-1", "architecture": "amd64", "index_url": index_url,
                              "archive": contract.reference(self.root, self.output)}]}

    def test_typed_wrapped_eof_is_retained_before_one_successful_retry(self):
        calls = []
        def response(request, timeout):
            request_record = self.journal()["requests"][-1]
            self.assertEqual(request_record["attempts"][-1]["state"], "STARTED")
            calls.append(timeout)
            if len(calls) == 1:
                raise urllib.error.URLError(eof())
            return Response()
        with patch.object(execution.urllib.request, "urlopen", side_effect=response):
            self.assertEqual(self.fetch(), self.output)
        request = self.journal()["requests"][0]
        self.assertEqual(request["outcome"], "RECOVERED_TRANSPORT")
        self.assertEqual([row["state"] for row in request["attempts"]], ["FAILED", "SUCCEEDED"])
        self.assertEqual([row["ordinal"] for row in request["attempts"]], [1, 2])
        self.assertLessEqual(calls[1], calls[0])
        self.assertEqual(self.output.read_bytes(), BODY)
        self.assertEqual(set(self.runner.fetches[0]), {"url", "status", "headers", "body", "started_at", "finished_at"})
        self.assertNotIn("private diagnostic", json.dumps(self.journal()))
        self.assertNotIn("must not be recorded", json.dumps(self.journal()))

    def test_partial_bytes_remain_separate_and_retry_restarts_from_zero(self):
        with patch.object(execution.urllib.request, "urlopen", side_effect=[Response([b"partial", eof()]), Response()]):
            self.fetch()
        first, second = self.journal()["requests"][0]["attempts"]
        self.assertEqual(contract.verify_reference(self.root, first["body"]).read_bytes(), b"partial")
        self.assertEqual(contract.verify_reference(self.root, second["body"]).read_bytes(), BODY)
        self.assertNotEqual(first["body"]["path"], second["body"]["path"])
        self.assertEqual(first["headers"], {"etag": "original"})

    def test_exception_provided_bytes_are_retained_bounded_without_retry(self):
        for partial, exception in ((b"available", http.client.IncompleteRead), (BODY * 3, ValueError)):
            self.output = self.root / ("partial-" + str(len(partial)) + ".deb")
            with patch.object(execution.urllib.request, "urlopen", side_effect=[Response([b"written-", http.client.IncompleteRead(partial, 7)]), Response()]) as opened:
                with self.assertRaises(exception):
                    self.fetch()
                self.assertEqual(opened.call_count, 1)
            attempt = self.journal()["requests"][-1]["attempts"][0]
            retained = partial[:len(BODY) - len(b"written-")]
            self.assertEqual(contract.verify_reference(self.root, attempt["body"]).read_bytes(), b"written-" + retained)
            self.assertEqual(attempt["exception_partial_size"], len(partial))
            self.assertEqual(attempt["retained_exception_partial_size"], len(retained))
            self.assertFalse(attempt["error"]["retryable"])
            self.assertFalse(self.output.exists())

    def test_two_eofs_stop_without_a_third_attempt_or_success(self):
        with patch.object(execution.urllib.request, "urlopen", side_effect=[eof(), eof(), Response()]) as opened:
            with self.assertRaises(ssl.SSLEOFError):
                self.fetch()
        self.assertEqual(opened.call_count, 2)
        self.assertEqual(self.journal()["requests"][0]["outcome"], "FAILED")
        self.assertFalse(self.output.exists())
        self.assertEqual(self.runner.fetches, [])

    def test_other_transport_types_and_statuses_never_retry(self):
        wrong_reason = eof()
        wrong_reason.reason = "OTHER_REASON"
        for failure in (wrong_reason, ssl.SSLCertVerificationError("certificate rejected"), ssl.SSLError("UNEXPECTED_EOF_WHILE_READING"),
                        urllib.error.URLError("UNEXPECTED_EOF_WHILE_READING"), TimeoutError("timeout"),
                        ConnectionResetError("reset"), urllib.error.HTTPError(URL, 403, "forbidden", {}, None),
                        urllib.error.HTTPError(URL, 403, eof(), {}, None),
                        Response(status=503), Response(status="200"), Response(url="https://example.org/package.deb")):
            self.output = self.root / ("output-" + str(len(self.runner.package_fetches)) + ".deb")
            with self.subTest(failure=failure), patch.object(execution.urllib.request, "urlopen", side_effect=[failure, Response()]) as opened:
                with self.assertRaises((ValueError, OSError)):
                    self.fetch()
                self.assertEqual(opened.call_count, 1)

    def test_hash_size_and_filesystem_failures_never_retry(self):
        for payload in (BODY + b"extra", BODY[:-1], b"?" + BODY[1:]):
            self.output = self.root / ("bad-" + str(len(self.runner.package_fetches)) + ".deb")
            with patch.object(execution.urllib.request, "urlopen", side_effect=[Response([payload, b""]), Response()]) as opened:
                with self.assertRaises(ValueError):
                    self.fetch()
                self.assertEqual(opened.call_count, 1)
        self.output = self.root / "existing.deb"
        self.output.write_bytes(b"preserve")
        with patch.object(execution.urllib.request, "urlopen") as opened:
            with self.assertRaises((ValueError, OSError)):
                self.fetch()
            opened.assert_not_called()
        self.assertEqual(self.output.read_bytes(), b"preserve")
        self.output = self.root / "cannot-publish.deb"
        with patch.object(execution.urllib.request, "urlopen", side_effect=[Response(), Response()]) as opened, \
                patch.object(execution.os, "link", side_effect=OSError("local publication failed")):
            with self.assertRaisesRegex(OSError, "local publication failed"):
                self.fetch()
            self.assertEqual(opened.call_count, 1)

    def test_stop_and_deadline_prevent_the_second_attempt(self):
        for stopped in (True, False):
            self.output = self.root / ("stopped-" + str(stopped) + ".deb")
            def failure(*args, **kwargs):
                if stopped:
                    Path(self.runner.request["stop_file"]).touch()
                else:
                    self.runner.monotonic_deadline = time.monotonic() - 1
                raise eof()
            with patch.object(execution.urllib.request, "urlopen", side_effect=failure) as opened:
                with self.assertRaises(ValueError):
                    self.fetch()
                self.assertEqual(opened.call_count, 1)
            self.assertEqual(self.journal()["requests"][-1]["attempts"][0]["state"], "FAILED")
            Path(self.runner.request["stop_file"]).unlink(missing_ok=True)

    def test_journal_write_failure_forbids_further_dispatch(self):
        original = evidence.atomic_json
        for failure_write in (1, 2, 3):
            self.runner.package_fetches = []
            self.runner.package_journal_reference = None
            (self.root / "package-fetch-journal.json").unlink(missing_ok=True)
            self.output = self.root / ("journal-" + str(failure_write) + ".deb")
            writes = []
            def save(path, value):
                writes.append(path)
                if len(writes) == failure_write:
                    raise OSError("journal unavailable")
                return original(path, value)
            with patch.object(execution.evidence, "atomic_json", side_effect=save), patch.object(execution.urllib.request, "urlopen", side_effect=[eof(), Response()]) as opened:
                with self.assertRaises(OSError):
                    self.fetch()
                self.assertEqual(opened.call_count, 0 if failure_write == 1 else 1)

    def test_stop_or_deadline_during_intent_persistence_prevents_dispatch(self):
        original = self.runner.write_package_journal
        for stopped in (True, False):
            self.output = self.root / ("admission-" + str(stopped) + ".deb")
            def persist():
                original()
                if self.runner.package_fetches[-1]["attempts"][-1]["state"] == "STARTED":
                    if stopped:
                        Path(self.runner.request["stop_file"]).touch()
                    else:
                        self.runner.monotonic_deadline = time.monotonic() - 1
            with patch.object(self.runner, "write_package_journal", side_effect=persist), \
                    patch.object(execution.urllib.request, "urlopen") as opened:
                with self.assertRaisesRegex(ValueError, "deadline or stop"):
                    self.fetch()
                opened.assert_not_called()
            self.assertEqual(self.journal()["requests"][-1]["attempts"][0]["state"], "FAILED")
            Path(self.runner.request["stop_file"]).unlink(missing_ok=True)

    def test_opt_in_is_required_and_limited_to_pinned_package_archives(self):
        with patch.object(execution.urllib.request, "urlopen", side_effect=[eof(), Response()]) as opened:
            with self.assertRaises(ssl.SSLEOFError):
                self.runner.fetch(URL, self.output, expected_sha256=DIGEST, maximum=len(BODY))
            self.assertEqual(opened.call_count, 1)
        for url, digest in ((URL.replace("pool/main/n/nettle/libnettle8t64_3.10.1-1_amd64.deb", "dists/trixie/InRelease"), DIGEST), (URL, None)):
            with patch.object(execution.urllib.request, "urlopen") as opened:
                with self.assertRaises(ValueError):
                    self.runner.fetch(url, self.output, expected_sha256=digest, maximum=len(BODY), retry_package_archive=True)
                opened.assert_not_called()

    def test_immutable_seal_binds_recovery_without_rewriting_signature_logs(self):
        with patch.object(execution.urllib.request, "urlopen", side_effect=[Response([b"partial", eof()]), Response()]):
            self.fetch()
        closure = self.closure()
        original = copy.deepcopy(closure["signed_indexes"][0]["signature_log"])
        with self.assertRaisesRegex(ValueError, "no sealed package transport"):
            materials.package_transports([closure], self.root)
        reference = self.runner.seal_package_fetches([closure])
        contract.verify_reference(self.root, original)
        self.assertNotEqual(original, closure["signed_indexes"][0]["signature_log"])
        self.assertEqual(materials.package_transports([closure], self.root), {"requests": 1, "recovered": 1})
        plan = {"platform": "linux/amd64", "package_indexes": [{"url": closure["signed_indexes"][0]["url"],
                "sha256": closure["signed_indexes"][0]["packages"]["sha256"]}]}
        self.assertEqual(len(materials.os_closure(closure, self.root, plan, time.time(), self.runner.commands)), 1)
        self.assertEqual(reference["sha256"], contract.digest(self.journal()))
        self.output = self.root / "after-seal.deb"
        with patch.object(execution.urllib.request, "urlopen") as opened:
            with self.assertRaises(ValueError):
                self.fetch()
            opened.assert_not_called()

    def test_tampered_original_log_journal_or_partial_cannot_be_sealed(self):
        with patch.object(execution.urllib.request, "urlopen", side_effect=[Response([b"partial", eof()]), Response()]):
            self.fetch()
        closure = self.closure()
        paths = [self.root / closure["signed_indexes"][0]["signature_log"]["path"], self.root / "package-fetch-journal.json",
                 self.root / self.journal()["requests"][0]["attempts"][0]["body"]["path"]]
        for path in paths:
            original = path.read_bytes()
            path.write_bytes(original + b"changed")
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.runner.seal_package_fetches([closure])
            self.assertFalse(self.runner.package_fetches_sealed)
            self.assertFalse((self.root / "package-fetches/sealed.json").exists())
            path.write_bytes(original)
        # Even a newly computed descriptor cannot replace the trusted memory expectation.
        journal = self.root / "package-fetch-journal.json"
        changed = self.journal()
        changed["requests"][0]["url"] = URL.replace("libnettle", "other")
        journal.write_bytes(contract.canonical(changed))
        self.runner.package_journal_reference = contract.reference(self.root, journal)
        with self.assertRaisesRegex(ValueError, "trusted memory"):
            self.runner.seal_package_fetches([closure])

    def test_sealed_replay_rejects_missing_failure_and_wrong_source_bindings(self):
        with patch.object(execution.urllib.request, "urlopen", side_effect=[eof(), Response()]):
            self.fetch()
        original = self.journal()
        for mutate in (lambda value: value["requests"][0]["attempts"].pop(0),
                       lambda value: value["requests"][0]["attempts"][0]["error"].update(type="TimeoutError"),
                       lambda value: value["requests"][0].update(outcome="FIRST_ATTEMPT_SUCCESS"),
                       lambda value: value["requests"][0]["attempts"].append(value["requests"][0]["attempts"][-1])):
            value = copy.deepcopy(original)
            mutate(value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                materials.package_fetch_journal(value, self.root)
        closure = self.closure()
        self.runner.seal_package_fetches([closure])
        closure["packages"][0]["index_url"] = "https://security.debian.org/debian-security/dists/trixie-security/main/binary-amd64/Packages.gz"
        closure["signed_indexes"][0]["url"] = closure["packages"][0]["index_url"]
        with self.assertRaisesRegex(ValueError, "signed source"):
            materials.package_transports([closure], self.root)


if __name__ == "__main__":
    unittest.main()
