"""Read-only exact-lock NATS admission; never starts or prepares a native server."""

import argparse
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import artifact, check_deadline, decode_json, pinned_json, read_artifact, require

SOURCE = "5a815f11a911efa2c88fd1dbfb18ed89a449bfc1"
VERSION = "v2.15.2"
IMAGE_INDEX = "afae2fdbe2e326c65e1246cb83b9d05d319188734f3bd577ac8072daae78a10f"
IMAGE_PLATFORM = "89c5c61b5969261e05106a98e465ccb85c7f4bf4e94c1e70786c189759d79e7e"
COMPILER_IMAGE = "docker.io/library/golang@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80"
BINARY = "33f3fe3fed80b2b229bd9fe4b735257ad8583cd2349452a0c74f3d768b593e66"
SOURCE_ARCHIVE = "a2ebd49e9ec3286163e22b9653b6f250f4177dcc933a1dab213a5f32f7f3395c"
MAXIMUM = 128 * 1024 * 1024


def sha(data):
    return hashlib.sha256(data).hexdigest()


def inline_string(data, position):
    size = 0
    for shift in range(0, 70, 7):
        require(position < len(data), "Truncated Go build-info length")
        byte = data[position]
        position += 1
        size |= (byte & 127) << shift
        if byte < 128:
            require(size <= 1024 * 1024 and position + size <= len(data), "Invalid Go build-info string length")
            return data[position:position + size], position + size
    raise ValueError("Oversized Go build-info length")


def build_info(data):
    """Decode the supported ELF/Go inline format, without executing the binary."""
    require(len(data) >= 64 and data[:6] == b"\x7fELF\x02\x01" and
            int.from_bytes(data[18:20], "little") == 62, "Expected linux/amd64 ELF")
    magic = b"\xff Go buildinf:"
    position = data.find(magic)
    require(position >= 0 and position % 16 == 0 and data.find(magic, position + 1) == -1,
            "Missing or ambiguous Go build info")
    require(data[position + 14:position + 32] == b"\x08\x02" + b"\0" * 16,
            "Unsupported Go build-info encoding")
    version, following = inline_string(data, position + 32)
    framed, _ = inline_string(data, following)
    require(len(framed) >= 33 and framed[-17] == 10, "Invalid Go module framing")
    module = framed[16:-16]
    lines = module.decode("utf-8", "strict").splitlines()
    paths, modules, settings = [], [], {}
    for line in lines:
        fields = line.split("\t")
        if fields[0] == "path":
            require(len(fields) == 2, "Invalid build package path")
            paths.append(fields[1])
        elif fields[0] == "mod":
            require(len(fields) in (3, 4), "Invalid build module")
            modules.append(fields[1:3])
        elif fields[0] == "build":
            require(len(fields) == 2 and "=" in fields[1], "Invalid Go build setting")
            key, value = fields[1].split("=", 1)
            require(key not in settings, "Repeated Go build setting")
            settings[key] = value
        else:
            require(fields[0] == "dep" and len(fields) in (3, 4), "Unsupported build metadata or replacement")
    require(paths == ["github.com/nats-io/nats-server/v2"] and
            modules == [["github.com/nats-io/nats-server/v2", VERSION]], "Wrong NATS module build")
    expected = {"GOARCH": "amd64", "GOOS": "linux", "CGO_ENABLED": "0",
                "vcs.revision": SOURCE, "vcs.modified": "false"}
    require(version == b"go1.27.3" and all(settings.get(key) == value for key, value in expected.items()),
            "NATS source, compiler or platform mismatch")
    return {"go_version": version.decode(), "module_version": VERSION,
            "source_commit": SOURCE, "settings": settings, "module_text_sha256": sha(module),
            "framed_module_sha256": sha(framed)}


def tar_members(data, deadline=None):
    require(len(data) <= MAXIMUM, "Archive exceeds admission byte limit")
    result = {}
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
        for member in archive:
            check_deadline(deadline)
            name = member.name.rstrip("/")
            parts = PurePosixPath(name).parts
            require(parts and not name.startswith("/") and ".." not in parts and
                    str(PurePosixPath(name)) == name and name not in result and len(result) < 10000,
                    "Unsafe, duplicate or oversized archive inventory")
            require(0 <= member.size <= MAXIMUM, "Archive member exceeds admission limit")
            result[name] = member
        return result


def image_executable(data, config_bytes, deadline=None):
    """Bind uncompressed Docker-save layers to the pinned OCI config diff IDs."""
    config = decode_json(config_bytes, deadline)
    require(config.get("architecture") == "amd64" and config.get("os") == "linux" and
            config.get("rootfs", {}).get("type") == "layers", "Wrong NATS image platform")
    diff_ids = config["rootfs"]["diff_ids"]
    require(isinstance(diff_ids, list) and 1 <= len(diff_ids) <= 16 and
            all(isinstance(value, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", value) for value in diff_ids),
            "Invalid image layer inventory")
    members = tar_members(data, deadline)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
        def contents(name):
            require(name in members and members[name].isreg(), "Missing regular image member")
            with archive.extractfile(members[name]) as stream:
                value = stream.read(members[name].size + 1)
            require(len(value) == members[name].size, "Image member size mismatch")
            return value
        rows = decode_json(contents("manifest.json"), deadline)
        require(isinstance(rows, list) and len(rows) == 1, "Ambiguous image archive")
        row = rows[0]
        expected_config = "blobs/sha256/" + sha(config_bytes)
        require(row.get("Config") == expected_config and contents(expected_config) == config_bytes,
                "Image archive config mismatch")
        layers = [value.removeprefix("sha256:") for value in diff_ids]
        require(row.get("Layers") == ["blobs/sha256/" + value for value in layers], "Image layer order mismatch")
        binary = None
        target = "usr/local/bin/nats-server"
        protected = {target, "usr", "usr/local", "usr/local/bin"}
        whiteouts = {".wh.usr", "usr/.wh.local", "usr/local/.wh.bin", "usr/local/bin/.wh.nats-server",
                     ".wh..wh..opq", "usr/.wh..wh..opq", "usr/local/.wh..wh..opq", "usr/local/bin/.wh..wh..opq"}
        for digest in layers:
            raw = contents("blobs/sha256/" + digest)
            require(sha(raw) == digest, "Image layer bytes changed")
            entries = tar_members(raw, deadline)
            require(not whiteouts.intersection(entries), "Executable path is removed by a layer")
            for name in protected.intersection(entries):
                require(entries[name].isreg() if name == target else entries[name].isdir(),
                        "Executable path contains a link or special file")
            if target in entries:
                require(binary is None, "Executable is replaced across layers")
                with tarfile.open(fileobj=io.BytesIO(raw), mode="r:") as layer:
                    with layer.extractfile(entries[target]) as stream:
                        binary = stream.read(entries[target].size + 1)
                require(len(binary) == entries[target].size, "Executable size mismatch")
        require(binary is not None, "NATS executable missing from image")
        check_deadline(deadline)
        return binary, layers


def inspect_build(path, expected_sha):
    deadline = time.monotonic() + 120
    manifest_ref, manifest = pinned_json(path, expected_sha, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "Caller-pinned accepted actual build required")
    closure = manifest["module_closures"]["nats-server"]
    require(closure["source_commit"] == SOURCE and closure["source_archive"]["sha256"] == SOURCE_ARCHIVE and
            manifest["plan"]["sources"]["nats-server"]["commit"] == SOURCE and
            manifest["plan"]["compiler"]["version"] == "go1.27.3", "Unsupported NATS source or compiler lock")
    refs = []
    def verify(ref, maximum=MAXIMUM):
        resolved = artifact(ref, maximum, path.parent, deadline)
        require(resolved.is_relative_to(path.parent), "Build reference escapes accepted artifact root")
        refs.append((ref, maximum))
    for key in ("source_archive", "downloads", "modules", "local_modules", "verification"):
        verify(closure[key])
    seen = set()
    for row in closure["artifacts"]:
        identity = (row["module"], row["version"])
        require(identity not in seen, "Duplicate module artifact")
        seen.add(identity)
        for key in ("info", "mod", "zip"): verify(row[key])
    require(("github.com/nats-io/nats.go", "v1.53.1") in seen, "Native test-client closure missing")
    compiler = manifest["image_inputs"]["golang-build-image"]
    require(compiler["reference"] == COMPILER_IMAGE and manifest["plan"]["platform"] == "linux/amd64",
            "Unsupported compiler image or platform")
    for key in ("index", "manifest", "config", "archive", "inspection"):
        verify(compiler[key], 1024 * 1024 * 1024)
    image = manifest["image_inputs"]["nats-runtime-image"]
    require(image["reference"] == "docker.io/library/nats@sha256:" + IMAGE_INDEX and
            image["platform_digest"] == "sha256:" + IMAGE_PLATFORM, "Unsupported NATS image lock")
    buffers = {}
    for key in ("index", "manifest", "config", "archive", "inspection"):
        verify(image[key])
        _, buffers[key] = read_artifact(image[key], MAXIMUM, path.parent, deadline)
    require(sha(buffers["index"]) == IMAGE_INDEX and sha(buffers["manifest"]) == IMAGE_PLATFORM,
            "Image descriptor mismatch")
    index, platform = decode_json(buffers["index"], deadline), decode_json(buffers["manifest"], deadline)
    matches = [row for row in index["manifests"] if row.get("platform", {}).get("os") == "linux" and
               row.get("platform", {}).get("architecture") == "amd64"]
    require(len(matches) == 1 and matches[0]["digest"] == "sha256:" + IMAGE_PLATFORM and
            matches[0]["size"] == len(buffers["manifest"]), "Image platform selection mismatch")
    require(platform["config"]["digest"] == "sha256:" + sha(buffers["config"]) and
            platform["config"]["size"] == len(buffers["config"]), "Image config descriptor mismatch")
    binary, layers = image_executable(buffers["archive"], buffers["config"], deadline)
    require(sha(binary) == BINARY, "Unsupported NATS executable bytes")
    metadata = build_info(binary)
    for ref, maximum in refs: artifact(ref, maximum, path.parent, deadline)
    artifact(manifest_ref, 64 * 1024 * 1024, deadline=deadline)
    return {"schema_version": 1, "kind": "nats-fence-input-inspection", "build_manifest": manifest_ref,
            "source_commit": SOURCE, "source_archive": closure["source_archive"],
            "compiler_image": COMPILER_IMAGE,
            "image": image["reference"], "platform_digest": image["platform_digest"],
            "binary_sha256": BINARY, "binary_size_bytes": len(binary), "build_info": metadata,
            "image_diff_ids": layers, "verified_references": len(refs),
            "runtime_admitted": False, "source_conformance": "NOT_RUN", "process_fence": "NOT_RUN"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--manifest-sha256", required=True)
    args = parser.parse_args()
    try:
        require(args.manifest.is_absolute(), "BuildManifest path must be absolute")
        print(json.dumps(inspect_build(args.manifest, args.manifest_sha256), sort_keys=True))
        return 0
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError) as error:
        print(json.dumps({"runtime_admitted": False, "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
