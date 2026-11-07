"""Exact offline redirect and output-path race regressions from review round 2."""

import contextlib
import email.message
import errno
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock
import urllib.parse
import urllib.request
import urllib.response

sys.path[:0] = [str(Path(__file__).resolve().parent),
               str(Path(__file__).resolve().parents[2] / "scripts/versioning"),
               str(Path(__file__).resolve().parents[2] / "build/native")]
from fixtures import OfficialFixture, REGISTRY, LOCK, hash_bytes
import sources
import update

TOKEN = "ghp_ROUND_TWO_SYNTHETIC_SECRET_123"
API = "https://api.github.com/repos/example/project/releases"


class OfflineHTTPS(urllib.request.HTTPSHandler):
    handler_order = 100

    def __init__(self, responses, requests):
        super().__init__()
        self.responses, self.requests = responses, requests
        self.opened = []

    def https_open(self, request):
        self.requests.append((request.full_url, request.get_header("Authorization")))
        status, headers, body = self.responses[request.full_url]
        message = email.message.Message()
        for key, value in headers.items():
            message[key] = value
        response = urllib.response.addinfourl(io.BytesIO(body), message, request.full_url, status)
        response.msg = "Found" if status == 302 else "OK"
        self.opened.append(response)
        return response


class RedirectControls(unittest.TestCase):
    def test_real_opener_never_follows_a_credential_echo(self):
        encoded = "".join("%%%02x" % ord(char) for char in TOKEN)
        cases = [("location", TOKEN), ("location", encoded), ("location", encoded.replace("%", "%25")),
                 ("header", TOKEN), ("header", encoded)]
        for place, echo in cases:
            with self.subTest(place=place, encoded=echo != TOKEN), tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"GH_TOKEN": TOKEN}, clear=True):
                root = Path(directory).resolve()
                transport = sources.Transport(root / "sources")
                destination = "https://raw.githubusercontent.com/example/data"
                headers = {"Location": destination + ("?credential=" + echo if place == "location" else "")}
                if place == "header":
                    headers["X-Credential-Echo"] = echo
                requests = []
                responses = {API: (302, headers, b""), headers["Location"]: (200, {}, b"[]")}
                handler = OfflineHTTPS(responses, requests)
                transport.opener.add_handler(handler)
                with self.assertRaises(ValueError) as error:
                    transport.fetch(API)
                self.assertEqual([url for url, _ in requests], [API])
                self.assertTrue(all(response.closed for response in handler.opened))
                self.assertNotIn(TOKEN, str(error.exception))
                self.assertNotIn(echo, json.dumps(transport.rows))
                for path in root.rglob("*"):
                    if path.is_file():
                        self.assertNotIn(TOKEN.encode(), path.read_bytes())
                        self.assertNotIn(encoded.encode(), path.read_bytes())
                        self.assertNotIn(echo.encode(), path.read_bytes())

    def test_real_opener_preserves_safe_redirects_and_header_scope(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.dict(os.environ, {"GH_TOKEN": TOKEN}, clear=True):
            transport = sources.Transport(Path(directory).resolve() / "sources")
            same = "https://api.github.com/repos/example/project/releases?page=2"
            other = "https://raw.githubusercontent.com/example/data"
            requests = []
            responses = {API: (302, {"Location": same}, b""), same: (302, {"Location": other}, b""), other: (200, {}, b"[]")}
            transport.opener.add_handler(OfflineHTTPS(responses, requests))
            self.assertEqual(transport.data(API), b"[]")
            self.assertEqual(requests, [(API, "Bearer " + TOKEN), (same, "Bearer " + TOKEN), (other, None)])


class FinalPathRace(unittest.TestCase):
    def test_replacement_after_lstat_is_never_deleted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            candidate, report = root / "candidate.json", root / "report.json"
            replacement = root / "foreign.data"
            replacement.write_bytes(b"foreign writer")
            original_link, original_lstat, original_unlink = os.link, Path.lstat, Path.unlink
            report_failed, deleted = False, []
            def link(source, destination, *args, **kwargs):
                nonlocal report_failed
                if Path(destination) == report:
                    report_failed = True
                    if not candidate.exists():
                        os.replace(replacement, candidate)
                    raise OSError(errno.ENOSPC, "Synthetic report publication failure")
                return original_link(source, destination, *args, **kwargs)
            def lstat(path, *args, **kwargs):
                result = original_lstat(path, *args, **kwargs)
                if path == candidate and report_failed and replacement.exists():
                    os.replace(replacement, candidate)
                return result
            def unlink(path, *args, **kwargs):
                if path in (candidate, report):
                    deleted.append(path)
                return original_unlink(path, *args, **kwargs)
            args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                    "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
            with mock.patch.object(os, "link", link), mock.patch.object(Path, "lstat", lstat), mock.patch.object(Path, "unlink", unlink), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(update.main(args), 2)
            self.assertEqual(deleted, [])
            self.assertEqual(candidate.read_bytes(), b"foreign writer")
