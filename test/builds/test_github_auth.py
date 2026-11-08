"""Credential isolation controls using invented tokens and no network access."""

import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import urllib.error
import urllib.request

sys.path[:0] = [str(Path(__file__).resolve().parent),
               str(Path(__file__).resolve().parents[2] / "scripts/versioning"),
               str(Path(__file__).resolve().parents[2] / "build/native")]
from fixtures import OfficialFixture, REGISTRY, LOCK, hash_bytes, packed
import sources
import update

API = "https://api.github.com/repos/example/synthetic/releases"
TOKEN = "ghp_SYNTHETIC_SECRET_1234567890"


class Response(io.BytesIO):
    def __init__(self, body=b"[]", headers=None, status=200):
        super().__init__(body)
        self.headers, self.status = headers or {}, status


class GitHubAuthTests(unittest.TestCase):
    def transport(self, root):
        return sources.Transport(root / "observations")

    def test_environment_precedence_and_exact_origin(self):
        for environment, expected in (({"GH_TOKEN": TOKEN, "GITHUB_TOKEN": "secondary"}, TOKEN),
                                      ({"GITHUB_TOKEN": TOKEN}, TOKEN)):
            with self.subTest(environment=list(environment)), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                with mock.patch.dict(os.environ, environment, clear=True), mock.patch.object(subprocess, "run") as helper:
                    transport = self.transport(root)
                    captured = []
                    def opened(request, **kwargs):
                        captured.append(request)
                        return Response()
                    with mock.patch.object(transport.opener, "open", opened):
                        transport.fetch(API)
                        transport.fetch("https://api.github.com:443/repos/example/other/releases")
                        transport.fetch("https://raw.githubusercontent.com/example/synthetic/commit/go.mod")
                        transport.fetch("https://go.dev/dl/?mode=json")
                    self.assertEqual([r.get_header("Authorization") for r in captured], ["Bearer " + expected, "Bearer " + expected, None, None])
                    helper.assert_not_called()

    def test_existing_helper_is_bounded_and_used_once(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {}, clear=True):
            root = Path(directory).resolve()
            result = subprocess.CompletedProcess([], 0, (TOKEN + "\n").encode(), b"")
            with mock.patch.object(subprocess, "run", return_value=result) as helper:
                transport = self.transport(root)
                with mock.patch.object(transport.opener, "open", side_effect=lambda *a, **k: Response()):
                    transport.fetch(API)
                    transport.fetch(API + "?page=2")
                helper.assert_called_once()
                args, kwargs = helper.call_args
                self.assertEqual(args[0], ["gh", "auth", "token", "--hostname", "github.com"])
                self.assertLessEqual(kwargs["timeout"], 5)
                self.assertEqual(kwargs["stdin"], subprocess.DEVNULL)
                self.assertEqual(kwargs["stdout"], subprocess.PIPE)
                self.assertEqual(kwargs["stderr"], subprocess.PIPE)
                self.assertEqual(kwargs["env"]["GH_PROMPT_DISABLED"], "1")
                self.assertNotIn(TOKEN, str(args) + str(kwargs))

    def test_absent_failed_and_timed_out_helpers_remain_unauthenticated(self):
        outcomes = [FileNotFoundError("missing gh"), subprocess.TimeoutExpired(["gh"], 5, output=TOKEN.encode()),
                    subprocess.CompletedProcess([], 1, TOKEN.encode(), TOKEN.encode()), subprocess.CompletedProcess([], 0, b"", b"")]
        for outcome in outcomes:
            with self.subTest(outcome=type(outcome).__name__), tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {}, clear=True):
                root = Path(directory).resolve()
                settings = {"side_effect": outcome} if isinstance(outcome, BaseException) else {"return_value": outcome}
                stdout, stderr = io.StringIO(), io.StringIO()
                with mock.patch.object(subprocess, "run", **settings), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    transport = self.transport(root)
                    requests = []
                    def opened(request, **kwargs):
                        requests.append(request)
                        return Response(b'{"message":"API limit exceeded"}', status=403)
                    with mock.patch.object(transport.opener, "open", opened), self.assertRaises(ValueError):
                        transport.fetch(API)
                self.assertIsNone(requests[0].get_header("Authorization"))
                retained = b"".join(path.read_bytes() for path in root.rglob("*") if path.is_file())
                self.assertNotIn(TOKEN.encode(), retained)
                self.assertNotIn(TOKEN, stdout.getvalue() + stderr.getvalue() + json.dumps(transport.rows))

    def test_invalid_tokens_cannot_inject_headers(self):
        for token in (" token", "token ", "token\r\nX-Injected: yes", "token\tvalue", "token\x00value", "token\u00a0value"):
            with self.subTest(token=repr(token)), tempfile.TemporaryDirectory() as directory, mock.patch.object(os, "environ", {"GH_TOKEN": token}):
                with mock.patch.object(subprocess, "run") as helper:
                    transport = self.transport(Path(directory).resolve())
                    captured = []
                    with mock.patch.object(transport.opener, "open", side_effect=lambda request, **kwargs: captured.append(request) or Response()):
                        transport.fetch(API)
                    self.assertIsNone(captured[0].get_header("Authorization"))
                    helper.assert_not_called()

    def test_invalid_helper_output_cannot_become_authorization(self):
        for output in (b" token\n", b"token\n\n", b"token\r\nX-Injected: yes\n", b"token\x00value\n", b"x" * 4099, b"\xff\n"):
            with self.subTest(length=len(output)), tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {}, clear=True):
                result = subprocess.CompletedProcess([], 0, output, b"")
                with mock.patch.object(subprocess, "run", return_value=result):
                    transport = self.transport(Path(directory).resolve())
                    captured = []
                    with mock.patch.object(transport.opener, "open", side_effect=lambda request, **kwargs: captured.append(request) or Response()):
                        transport.fetch(API)
                    self.assertIsNone(captured[0].get_header("Authorization"))

    def test_non_api_sources_do_not_query_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            transport = self.transport(Path(directory).resolve())
            with mock.patch.object(sources, "github_credential", side_effect=AssertionError("Non-API source queried credentials")), \
                 mock.patch.object(transport.opener, "open", side_effect=lambda *a, **k: Response()):
                transport.fetch("https://raw.githubusercontent.com/example/synthetic/commit/go.mod")
                transport.fetch("https://go.dev/dl/?mode=json")

    def test_redirect_strips_cross_origin_and_preserves_exact_origin(self):
        handler = sources.Redirect()
        request = urllib.request.Request(API, headers={"Authorization": "Bearer " + TOKEN})
        same = handler.redirect_request(request, None, 302, "Found", {}, "https://api.github.com:443/next")
        self.assertEqual(same.get_header("Authorization"), "Bearer " + TOKEN)
        other = handler.redirect_request(request, None, 302, "Found", {}, "https://raw.githubusercontent.com/example/file")
        self.assertIsNone(other.get_header("Authorization"))
        for url in ("http://api.github.com/next", "https://api.github.com.evil.test/next", "https://api.github.com:444/next", "https://user@api.github.com/next"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                handler.redirect_request(request, None, 302, "Found", {}, url)

    def test_synthetic_update_does_not_access_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                    "--report", str(root / "report.json"), "--output", str(root / "candidate.json"), "--fixture-transport", str(fixture.save())]
            environment_get = os.environ.get
            def guarded_get(key, *args):
                if key in ("GH_TOKEN", "GITHUB_TOKEN"):
                    raise AssertionError("Synthetic mode queried credentials")
                return environment_get(key, *args)
            with mock.patch.object(os.environ, "get", side_effect=guarded_get), \
                 mock.patch.object(subprocess, "run", side_effect=AssertionError("Synthetic mode ran a helper")):
                self.assertEqual(update.main(args), 0)

    def test_transport_errors_and_echoed_credentials_are_not_retained(self):
        for case in ("exception", "header", "body", "split-body"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"GH_TOKEN": TOKEN}, clear=True):
                root = Path(directory).resolve()
                transport = self.transport(root)
                def opened(*args, **kwargs):
                    if case == "exception":
                        raise urllib.error.URLError("request failed: Bearer " + TOKEN)
                    if case == "header":
                        return Response(headers={"x-echo": TOKEN})
                    if case == "split-body":
                        return Response(b"x" * (1024 * 1024 - 5) + TOKEN.encode())
                    return Response(TOKEN.encode())
                stdout, stderr = io.StringIO(), io.StringIO()
                with mock.patch.object(transport.opener, "open", opened), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr), self.assertRaises(ValueError) as failure:
                    transport.fetch(API)
                retained = b"".join(path.read_bytes() for path in root.rglob("*") if path.is_file())
                self.assertNotIn(TOKEN.encode(), retained)
                self.assertNotIn(TOKEN, stdout.getvalue() + stderr.getvalue() + str(failure.exception) + json.dumps(transport.rows))

    def test_mocked_authenticated_discovery_retains_no_credentials(self):
        # Exercise the live code path with invented responses; never use a network.
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"GH_TOKEN": TOKEN}, clear=True):
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            report, candidate = root / "report.json", root / "candidate.json"
            seen = []
            def opened(opener, request, **kwargs):
                url = request.full_url
                row = fixture.rows[url]
                seen.append((url, request.get_header("Authorization")))
                return Response((root / row["body"]["path"]).read_bytes(), row["headers"], row["status"])
            def docker_token(url, **kwargs):
                self.assertTrue(url.startswith("https://auth.docker.io/token?"))
                return Response(packed({"token": "SYNTHETIC_DOCKER_CREDENTIAL"}))
            before, modified = LOCK.read_bytes(), LOCK.stat().st_mtime_ns
            args = ["--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(before), "--report", str(report)]
            with mock.patch.object(urllib.request.OpenerDirector, "open", opened), \
                 mock.patch.object(urllib.request, "urlopen", docker_token), mock.patch.object(subprocess, "run") as helper:
                self.assertEqual(update.main(["check", *args]), 0)
                report.unlink()
                self.assertEqual(update.main(["update", *args, "--output", str(candidate)]), 0)
                helper.assert_not_called()
            self.assertTrue(all(auth == "Bearer " + TOKEN for url, auth in seen if sources.origin(url) == ("https", "api.github.com", 443)))
            self.assertTrue(all(auth != "Bearer " + TOKEN for url, auth in seen if sources.origin(url) != ("https", "api.github.com", 443)))
            for path in root.rglob("*"):
                if path.is_file():
                    self.assertNotIn(TOKEN.encode(), path.read_bytes())
                    self.assertNotIn(b"SYNTHETIC_DOCKER_CREDENTIAL", path.read_bytes())
            self.assertTrue(json.loads(report.read_bytes())["complete"])
            self.assertTrue(candidate.is_file())
            self.assertEqual(LOCK.read_bytes(), before)
            self.assertEqual(LOCK.stat().st_mtime_ns, modified)
