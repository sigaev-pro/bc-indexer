"""Byte-level admission controls without Docker, credentials or network access."""

import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest

import nats_inputs


def varint(value):
    data = bytearray()
    while value >= 128:
        data.append((value & 127) | 128)
        value >>= 7
    return bytes(data + bytes([value]))


def executable(settings=None):
    fields = {"GOARCH": "amd64", "GOOS": "linux", "CGO_ENABLED": "0",
              "vcs.revision": "5a815f11a911efa2c88fd1dbfb18ed89a449bfc1", "vcs.modified": "false"}
    fields.update(settings or {})
    module = ("path\tgithub.com/nats-io/nats-server/v2\n"
              "mod\tgithub.com/nats-io/nats-server/v2\tv2.15.2\th1:synthetic\n" +
              "".join("build\t" + key + "=" + value + "\n" for key, value in fields.items())).encode()
    raw = b"x" * 16 + module + b"y" * 16
    header = bytearray(64)
    header[:6] = b"\x7fELF\x02\x01"
    header[18:20] = (62).to_bytes(2, "little")
    info = b"\xff Go buildinf:" + b"\x08\x02" + b"\0" * 16
    return bytes(header) + info + varint(8) + b"go1.27.3" + varint(len(raw)) + raw


def tar_bytes(entries):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w") as archive:
        for name, value, kind in entries:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.size = len(value)
            archive.addfile(member, io.BytesIO(value))
    return data.getvalue()


class NatsInputTests(unittest.TestCase):
    def test_cli_rejects_wrong_external_pin_without_output_mutation(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve() / "manifest.json"
            path.write_bytes(b'{"evidence_kind":"actual-build"}')
            result = subprocess.run([sys.executable, "-I", str(Path(nats_inputs.__file__)),
                                     str(path), "--manifest-sha256", "0" * 64],
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 2)
            self.assertFalse(json.loads(result.stdout)["runtime_admitted"])
            self.assertIn("external trusted pin", result.stdout)
            self.assertEqual(list(path.parent.iterdir()), [path])
            self.assertEqual(path.read_bytes(), b'{"evidence_kind":"actual-build"}')

    def test_inline_native_build_metadata_is_typed_and_exact(self):
        raw = executable()
        result = nats_inputs.build_info(raw)
        self.assertEqual(result["go_version"], "go1.27.3")
        self.assertEqual(result["module_version"], "v2.15.2")
        self.assertEqual(result["source_commit"], "5a815f11a911efa2c88fd1dbfb18ed89a449bfc1")
        for broken in (raw[:-1], raw + raw[64:], b"not an ELF" + raw[64:],
                       executable({"vcs.modified": "true"}), executable({"GOARCH": "arm64"}),
                       executable({"vcs.revision": "0" * 40})):
            with self.subTest(size=len(broken)), self.assertRaises(ValueError):
                nats_inputs.build_info(broken)

    def test_image_layers_are_authenticated_and_later_replacement_rejects(self):
        binary = executable()
        layer = tar_bytes([("usr/local/bin/nats-server", binary, tarfile.REGTYPE)])
        digest = hashlib.sha256(layer).hexdigest()
        config = {"architecture": "amd64", "os": "linux", "rootfs": {"type": "layers", "diff_ids": ["sha256:" + digest]}}
        config_bytes = json.dumps(config).encode()
        config_hash = hashlib.sha256(config_bytes).hexdigest()
        row = {"Config": "blobs/sha256/" + config_hash, "Layers": ["blobs/sha256/" + digest]}
        def archive(layer_bytes=layer, records=None):
            return tar_bytes(records or [
                ("manifest.json", json.dumps([row]).encode(), tarfile.REGTYPE),
                (row["Config"], config_bytes, tarfile.REGTYPE),
                (row["Layers"][0], layer_bytes, tarfile.REGTYPE)])
        actual, layers = nats_inputs.image_executable(archive(), config_bytes)
        self.assertEqual(actual, binary)
        self.assertEqual(layers, [digest])
        with self.assertRaises(ValueError):
            nats_inputs.image_executable(archive(layer + b"changed"), config_bytes)
        duplicate = [("manifest.json", json.dumps([row]).encode(), tarfile.REGTYPE)] * 2
        with self.assertRaises(ValueError):
            nats_inputs.image_executable(archive(records=duplicate), config_bytes)
        def authorized_layers(raw_layers):
            layer_hashes = [hashlib.sha256(raw).hexdigest() for raw in raw_layers]
            selected = dict(config)
            selected["rootfs"] = {"type": "layers", "diff_ids": ["sha256:" + value for value in layer_hashes]}
            selected_bytes = json.dumps(selected).encode()
            selected_hash = hashlib.sha256(selected_bytes).hexdigest()
            selected_row = {"Config": "blobs/sha256/" + selected_hash,
                            "Layers": ["blobs/sha256/" + value for value in layer_hashes]}
            entries = [("manifest.json", json.dumps([selected_row]).encode(), tarfile.REGTYPE),
                       (selected_row["Config"], selected_bytes, tarfile.REGTYPE)]
            entries += [(name, raw, tarfile.REGTYPE) for name, raw in zip(selected_row["Layers"], raw_layers)]
            return tar_bytes(entries), selected_bytes
        for layers, expected in (
            ([tar_bytes([("usr/local/bin/nats-server", b"", tarfile.SYMTYPE)])], "link or special"),
            ([layer, tar_bytes([("usr/local/bin/nats-server", b"replacement", tarfile.REGTYPE)])], "replaced across"),
            ([layer, tar_bytes([("usr/local/bin/.wh.nats-server", b"", tarfile.REGTYPE)])], "removed by"),
        ):
            selected, configuration = authorized_layers(layers)
            with self.subTest(expected=expected), self.assertRaisesRegex(ValueError, expected):
                nats_inputs.image_executable(selected, configuration)
        with self.assertRaisesRegex(ValueError, "deadline"):
            nats_inputs.image_executable(archive(), config_bytes, time.monotonic() - 1)


if __name__ == "__main__":
    unittest.main()
