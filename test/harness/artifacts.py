"""Raw-byte admission for the local harness; no native side effects."""

import hashlib
import json
from pathlib import Path
import re
import stat
import shutil


class Rejected(ValueError):
    pass


def require(value, message):
    if not value:
        raise Rejected(message)


def closed(value, fields, label):
    require(type(value) is dict and set(value) == set(fields), f"Invalid {label} fields")


def regular(path):
    path = Path(path)
    require(path.is_absolute() and ".." not in path.parts, "Artifact path must be absolute")
    for part in [*reversed(path.parents), path]:
        require(not part.is_symlink(), "Symlink artifact path")
    require(stat.S_ISREG(path.stat().st_mode), "Artifact must be a regular file")
    return path


def digest(path):
    hasher = hashlib.sha256()
    size = 0
    with regular(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(block)
            size += len(block)
    return hasher.hexdigest(), size


def reference(path):
    sha, size = digest(path)
    return {"path": str(path), "sha256": sha, "size_bytes": size}


def source_snapshot(output, names):
    directory = output / "harness-code"
    directory.mkdir()
    origins, retained = [], []
    for name in names:
        source = Path(__file__).with_name(name)
        origins.append(reference(source))
        target = directory / name
        shutil.copyfile(source, target)
        row = reference(target)
        require(row["sha256"] == origins[-1]["sha256"] and row["size_bytes"] == origins[-1]["size_bytes"],
                "Harness source changed while copying")
        retained.append(row)
    return origins, retained


def sources_unchanged(origins):
    require(all(reference(Path(row["path"])) == row for row in origins), "Harness source changed during run")


def artifact(value, maximum=64 * 1024 * 1024, root=None):
    closed(value, {"path", "sha256", "size_bytes"}, "artifact")
    require(type(value["path"]) is str, "Invalid artifact path")
    require(type(value["size_bytes"]) is int and 0 <= value["size_bytes"] <= maximum,
            "Artifact size exceeds bound")
    require(type(value["sha256"]) is str and re.fullmatch(r"[0-9a-f]{64}", value["sha256"]),
            "Invalid artifact digest")
    path = Path(value["path"])
    if root is not None:
        require(not path.is_absolute() and ".." not in path.parts, "Unsafe build artifact path")
        path = root / path
    actual = digest(path)
    require(actual == (value["sha256"], value["size_bytes"]), "Artifact bytes changed")
    return path


def load(path, maximum=64 * 1024 * 1024):
    path = regular(path)
    require(path.stat().st_size <= maximum, "JSON exceeds bound")

    def pairs(rows):
        result = {}
        for key, value in rows:
            require(key not in result, "Duplicate JSON key")
            result[key] = value
        return result

    return json.loads(path.read_text(), object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(Rejected("Nonfinite JSON")))


def build_input(value, profile):
    path = artifact(value)
    manifest = load(path)
    require(manifest.get("evidence_kind") == "actual-build" and
            manifest.get("task_id") == "E01-01.builds", "An accepted actual build is required")
    build = manifest["builds"]["geth"]
    binary = artifact(build["binary"], 256 * 1024 * 1024, path.parent)
    require(re.fullmatch(r"[0-9a-f]{40}", build["source_commit"]), "Invalid source commit")
    profiles = [row for row in manifest["profile_bindings"] if row["id"] == profile]
    require(profile == "ethereum-mainnet" and len(profiles) == 1 and
            profiles[0]["native_component"] == "geth" and
            profiles[0]["native_binary_sha256"] == build["binary"]["sha256"],
            "Build profile binding mismatch")
    image = manifest["image_inputs"]["debian-runtime-image"]["reference"]
    require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", image),
            "Runtime image must be pinned")
    return path, manifest, binary, image


def admit(path):
    request = load(path, 1024 * 1024)
    closed(request, {"schema_version", "build_manifest", "profile_association", "fixture",
                     "deadline_seconds"}, "request")
    require(type(request["schema_version"]) is int and request["schema_version"] == 1,
            "Unsupported request version")
    require(type(request["deadline_seconds"]) is int and 1 <= request["deadline_seconds"] <= 1800,
            "Run deadline must be between 1 and 1800 seconds")
    build_path, manifest, binary, image = build_input(request["build_manifest"], request["profile_association"])
    fixture_path = artifact(request["fixture"], 1024 * 1024)
    fixture = load(fixture_path)
    closed(fixture, {"schema_version", "identity", "genesis", "blocks", "full_chain", "expected",
                     "prepared_blocks", "generator"}, "fixture")
    require(type(fixture["schema_version"]) is int and fixture["schema_version"] == 1,
            "Unsupported fixture version")
    paths = {key: artifact(fixture[key], 16 * 1024 * 1024) for key in
             ("genesis", "blocks", "full_chain", "expected", "generator")}
    genesis = load(paths["genesis"])
    config = genesis["config"]
    identity = fixture["identity"]
    closed(identity, {"kind", "chain_id", "genesis_hash", "config_sha256"}, "fixture identity")
    config_sha = hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    require(identity["kind"] == "private-local" and type(identity["chain_id"]) is int and
            identity["chain_id"] == config["chainId"] == 31337 and
            identity["config_sha256"] == config_sha and
            re.fullmatch(r"0x[0-9a-f]{64}", identity["genesis_hash"]), "Fixture identity mismatch")
    expected = load(paths["expected"])
    require(fixture["prepared_blocks"] and len(fixture["prepared_blocks"]) == 2,
            "Exactly two prepared blocks are required")
    from compare import validate_oracle
    validate_oracle(expected, fixture["prepared_blocks"])
    return {"request": request, "manifest": manifest, "build_path": build_path,
            "binary": binary, "image": image, "fixture": fixture, "paths": paths,
            "expected": expected}
