"""Independently authored small data controls; never native build evidence."""

import copy
import gzip
import hashlib
import io
from pathlib import Path
import struct
import tarfile
import tempfile
import unittest

import build_contract as contract
import build_materials as materials


class MaterialTests(unittest.TestCase):
    def test_go_json_stream_is_complete_and_strict(self):
        self.assertEqual(materials.json_stream('{"a":1}\n {"b":2}\n'), [{"a": 1}, {"b": 2}])
        for data in ('{"a":1,"a":2}', '{"a":NaN}', '{}\ntruncated', '[]'):
            with self.subTest(data=data), self.assertRaises(ValueError):
                materials.json_stream(data)

    def test_resolved_graph_and_downloads_match_one_root(self):
        graph = [{"Path": "synthetic/native", "Main": True}, {"Path": "synthetic/dep", "Version": "v1.2.3"}]
        downloads = [{"Path": "synthetic/dep", "Version": "v1.2.3", "Sum": "h1:" + "A" * 43 + "=",
                      "GoModSum": "h1:" + "B" * 43 + "=", "Info": "/proof/cache/geth/dep.info",
                      "GoMod": "/proof/cache/geth/dep.mod", "Zip": "/proof/cache/geth/dep.zip"}]
        self.assertEqual(set(materials.modules(graph, downloads, "synthetic/native")), {("synthetic/dep", "v1.2.3")})
        for changes in ({"Version": "v1.2.4"}, {"Sum": "unknown"}, {"Zip": "/another/root/dep.zip"}, {"Error": "unavailable"}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                materials.modules(graph, [dict(downloads[0], **changes)], "synthetic/native")
        with self.assertRaises(ValueError):
            materials.modules(graph, downloads, "other/native")
        with self.assertRaises(ValueError):
            materials.modules(graph, [], "synthetic/native")
        replaced = copy.deepcopy(graph)
        replaced[1]["Replace"] = {"Path": "../other-native"}
        with self.assertRaises(ValueError):
            materials.modules(replaced, downloads, "synthetic/native")

    def test_import_graph_rejects_nats_and_shared_native_caches(self):
        rows = [{"ImportPath": "fmt", "Standard": True},
                {"ImportPath": "synthetic/native/cmd", "Module": {"Path": "synthetic/native", "Main": True, "Dir": "/source"}},
                {"ImportPath": "synthetic/dep", "Module": {"Path": "synthetic/dep", "Dir": "/proof/cache/geth/mod/dep"}}]
        materials.imported_packages(rows, "synthetic/native", "geth")
        mutations = [lambda r: r[2]["Module"].update(Dir="/proof/cache/bsc/mod/dep"),
                     lambda r: r[1]["Module"].update(Path="synthetic/other-native"),
                     lambda r: r[2].update(ImportPath="github.com/nats-io/nats.go"),
                     lambda r: r[2]["Module"].update(Path="github.com/nats-io/nats.go"),
                     lambda r: r[2].update(Incomplete=True), lambda r: r.append(r[0])]
        for mutation in mutations:
            value = copy.deepcopy(rows)
            mutation(value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                materials.imported_packages(value, "synthetic/native", "geth")

    def test_binary_header_and_compiler_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "synthetic-elf"
            header = bytearray(64)
            header[:6] = b"\x7fELF\x02\x01"
            struct.pack_into("<H", header, 18, 62)
            path.write_bytes(header)
            materials.elf(path, "linux/amd64")
            with self.assertRaises(ValueError):
                materials.elf(path, "linux/arm64")
            path.write_text("a version string is not an ELF binary")
            with self.assertRaises(ValueError):
                materials.elf(path, "linux/amd64")
        commit = "a" * 40
        info = "synthetic: go1.27.3\n\tmod\tsynthetic/native\t(devel)\n"
        info += "\tbuild\tGOOS=linux\n\tbuild\tGOARCH=amd64\n\tbuild\tCGO_ENABLED=1\n\tbuild\t-trimpath=true\n"
        info += "\tbuild\t-ldflags=-X synthetic.version=" + commit + "\n"
        materials.build_info(info, "go1.27.3", "synthetic/native", commit, "linux/amd64")
        for value in (info.replace("go1.27.3", "go1.27.2"), info.replace(commit, "b" * 40),
                      info.replace("GOARCH=amd64", "GOARCH=arm64"), info + "\tdep\tgithub.com/nats-io/nats.go\tv1.0.0\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                materials.build_info(value, "go1.27.3", "synthetic/native", commit, "linux/amd64")

    def test_source_archive_binds_original_dependency_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "source.tar"
            payload = b"module synthetic/native\n"
            with tarfile.open(path, "w") as archive:
                entry = tarfile.TarInfo("go.mod")
                entry.size = len(payload)
                archive.addfile(entry, io.BytesIO(payload))
            expected = [{"path": "go.mod", "sha256": hashlib.sha256(payload).hexdigest()}]
            materials.source_archive(path, expected)
            with self.assertRaises(ValueError):
                materials.source_archive(path, [{"path": "go.mod", "sha256": "0" * 64}])

    def test_release_binds_exact_index_and_expiration(self):
        url = "https://deb.debian.org/debian/dists/trixie/main/binary-amd64/Packages.gz"
        release = "Origin: Debian\nCodename: trixie\nDate: Thu, 01 Jan 1970 00:00:01 UTC\nValid-Until: Thu, 01 Jan 1970 00:16:40 UTC\nSHA256:\n "
        release += "a" * 64 + " 42 main/binary-amd64/Packages.gz\n"
        materials.release(release, url, "a" * 64, 42, 500)
        for value, now in ((release, 1001), (release.replace("a" * 64, "b" * 64), 500),
                           (release.replace("Origin: Debian", "Origin: Other"), 500),
                           (release.replace(" 42 ", " 43 "), 500)):
            with self.subTest(now=now, value=value), self.assertRaises(ValueError):
                materials.release(value, url, "a" * 64, 42, now)

    def test_package_index_matches_full_debian_revision(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "Packages.gz"
            value = "Package: synthetic\nVersion: 1.2-3+deb13u1\nArchitecture: amd64\nSize: 8\nSHA256: "
            value += "a" * 64 + "\nFilename: pool/main/s/synthetic/synthetic.deb\n\n"
            with gzip.open(path, "wt") as stream:
                stream.write(value)
            key = ("synthetic", "1.2-3+deb13u1", "amd64")
            self.assertEqual(set(materials.package_index(path, {key})), {key})
            self.assertEqual(materials.package_index(path, {("synthetic", "1.2", "amd64")}), {})
            with gzip.open(path, "wt") as stream:
                stream.write(value.replace("pool/main/s/synthetic/synthetic.deb", "pool/../outside.deb"))
            with self.assertRaises(ValueError):
                materials.package_index(path, {key})

    def test_installed_closure_is_not_an_unresolved_package_list(self):
        self.assertEqual(materials.installed("synthetic\t1.2-3\tamd64\n", "amd64"), {("synthetic", "1.2-3", "amd64")})
        for value in ("", "synthetic\n", "synthetic\t1.2-3\tarm64\n", "a\t1\tall\na\t1\tall\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                materials.installed(value, "amd64")


if __name__ == "__main__":
    unittest.main()
