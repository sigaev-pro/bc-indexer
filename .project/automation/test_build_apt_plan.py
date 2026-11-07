"""Recorded APT row shapes with disposable bytes; no real package installation."""

import hashlib
from pathlib import Path
import shlex
import tempfile
from types import SimpleNamespace
import unittest
import urllib.parse

import build_os


# Exact rows from actual operation 0973d8349b6f10a74b7e6c5ebb2a8b4b, command 241.
ZLIB = ("'file:/proof/mirror/repo-1/pool/updates/main/z/zlib/zlib1g_1.3.dfsg%2breally1.3.1-1%2bdeb13u1_amd64.deb' "
        "zlib1g_1%3a1.3.dfsg+really1.3.1-1+deb13u1_amd64.deb 88720 ")
BZIP = ("'file:/proof/mirror/repo-0/pool/main/b/bzip2/bzip2-doc_1.0.8-6_all.deb' "
        "bzip2-doc_1.0.8-6_all.deb 505172 MD5Sum:8e1f207ff45991794235922e0539029b")


class AptPlanTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.cache = self.root / "cache"
        self.cache.mkdir()
        self.packages = build_os.Packages(SimpleNamespace(root=self.root))
        self.packages.indexes = [{"prefix": "mirror/repo-0"}, {"prefix": "mirror/repo-1"}]
        self.archives = {}
        for line in (ZLIB, BZIP):
            uri, target, size, *unused = shlex.split(line)
            local = urllib.parse.unquote(uri.removeprefix("file:/proof/"))
            prefix, filename = local.split("/pool/", 1)
            number = int(prefix.removeprefix("mirror/repo-"))
            payload = b"!<arch>\n" + b"x" * (int(size) - 8)
            path = self.root / local
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(payload)
            record = {"Filename": "pool/" + filename, "Size": size, "SHA256": hashlib.sha256(payload).hexdigest()}
            self.packages.by_filename[(number, record["Filename"])] = {"index": number, "record": record}
            self.archives[target] = path

    def test_actual_three_and_four_field_rows_copy_exact_authenticated_archives(self):
        self.packages.download_plan("Reading package lists...\n" + ZLIB + "\n" + BZIP, self.cache)
        for target, archive in self.archives.items():
            self.assertEqual((self.cache / target).read_bytes(), archive.read_bytes())
        # APT's destination basename preserves its encoded epoch; it is not a URI.
        self.assertTrue((self.cache / shlex.split(ZLIB)[1]).exists())
        self.assertFalse((self.cache / urllib.parse.unquote(shlex.split(ZLIB)[1])).exists())

    def test_missing_optional_digest_never_relaxes_signed_size_or_membership(self):
        for line in (ZLIB.replace("88720", "88721"), ZLIB.replace("repo-1/", "repo-9/"),
                     ZLIB.replace("pool/updates/", "pool/other/")):
            with self.subTest(line=line), self.assertRaises(ValueError):
                self.packages.download_plan(line, self.cache)
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_archive_hash_remains_required_with_or_without_optional_digest(self):
        for line in (ZLIB, BZIP):
            target = shlex.split(line)[1]
            path = self.archives[target]
            original = path.read_bytes()
            path.write_bytes(b"?" + original[1:])
            with self.subTest(line=line), self.assertRaisesRegex(ValueError, "retained Debian package changed"):
                self.packages.download_plan(line, self.cache)
            path.write_bytes(original)
            self.assertFalse((self.cache / target).exists())

    def test_malformed_rows_and_uri_or_destination_escapes_fail_closed(self):
        uri, target, size = shlex.split(ZLIB)
        rows = [f"'{uri}' {target}", f"'{uri}' {target} {size} extra extra", f"'{uri}' {target} {size} not-a-digest",
                f"'{uri}' {target} {size} MD5Sum:abc", f"'{uri}' {target} -1", f"'{uri}' ../{target} {size}",
                f"'{uri}' . {size}", f"'{uri}' .. {size}", f"'{uri}?alternate=true' {target} {size}",
                f"'{uri}#fragment' {target} {size}", f"'{uri.replace('file:', 'https:')}' {target} {size}",
                f"'{uri.replace('/pool/', '/pool/%2e%2e/')}' {target} {size}",
                f"'{uri.replace('/pool/', '/pool//')}' {target} {size}", f"'{uri.replace('%2b', '%ZZ')}' {target} {size}",
                f"'{uri.replace('file:', 'file://foreign')}' {target} {size}", "'unterminated"]
        for line in rows:
            with self.subTest(line=line), self.assertRaises(ValueError):
                self.packages.download_plan(line, self.cache)
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_optional_digest_does_not_replace_signed_sha256(self):
        selected = self.packages.by_filename[(1, "pool/" + urllib.parse.unquote(shlex.split(ZLIB)[0]).split("/pool/", 1)[1])]
        line = ZLIB.rstrip() + " SHA256:" + selected["record"]["SHA256"]
        self.packages.download_plan(line, self.cache)
        target = self.cache / shlex.split(ZLIB)[1]
        target.write_bytes(b"different cached bytes")
        with self.assertRaisesRegex(ValueError, "APT cache contains another archive"):
            self.packages.download_plan(line, self.cache)

    def test_cache_symlinks_are_not_followed(self):
        target = self.cache / shlex.split(ZLIB)[1]
        for destination in (self.archives[target.name], self.root / "missing"):
            target.symlink_to(destination)
            with self.subTest(destination=destination), self.assertRaisesRegex(ValueError, "cache target is a symlink"):
                self.packages.download_plan(ZLIB, self.cache)
            target.unlink()


if __name__ == "__main__":
    unittest.main()
