"""Raw-byte admission for the local harness; no native side effects."""

import hashlib
import json
from pathlib import Path
import re
import stat
import os
import time
from contextlib import contextmanager


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


def check_deadline(deadline):
    require(deadline is None or time.monotonic() < deadline, "Aggregate deadline expired")


@contextmanager
def opened(path, maximum, expected_size=None, deadline=None):
    check_deadline(deadline)
    require(type(maximum) is int and maximum >= 0, "Invalid byte bound")
    descriptor = os.open(regular(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        current = os.fstat(descriptor)
        require(stat.S_ISREG(current.st_mode), "Artifact must be a regular file")
        require(current.st_size <= maximum, "Artifact bytes changed or exceed bound")
        if expected_size is not None:
            require(current.st_size == expected_size, "Artifact bytes changed")
        check_deadline(deadline)
        yield descriptor, current.st_size
    finally:
        os.close(descriptor)


def chunks(descriptor, size, deadline):
    remaining = size
    while remaining:
        check_deadline(deadline)
        require(os.fstat(descriptor).st_size == size, "Artifact bytes changed during read")
        requested = min(1024 * 1024, remaining)
        block = os.read(descriptor, requested)
        require(0 < len(block) <= requested, "Artifact bytes changed during read")
        remaining -= len(block)
        require(os.fstat(descriptor).st_size == size, "Artifact bytes changed during read")
        check_deadline(deadline)
        yield block
    require(os.fstat(descriptor).st_size == size, "Artifact bytes changed during read")
    check_deadline(deadline)


def digest(path, maximum=1024 * 1024 * 1024, expected_size=None, deadline=None):
    hasher = hashlib.sha256()
    with opened(path, maximum, expected_size, deadline) as (descriptor, size):
        for block in chunks(descriptor, size, deadline):
            hasher.update(block)
    return hasher.hexdigest(), size


def reference(path, maximum=1024 * 1024 * 1024, deadline=None):
    sha, size = digest(path, maximum, deadline=deadline)
    return {"path": str(path), "sha256": sha, "size_bytes": size}


def copy_artifact(source, destination, expected, maximum=256 * 1024 * 1024, deadline=None):
    require(type(expected["size_bytes"]) is int and 0 <= expected["size_bytes"] <= maximum,
            "Artifact size exceeds copy bound")
    hasher = hashlib.sha256()
    with opened(source, expected["size_bytes"], expected["size_bytes"], deadline) as (descriptor, size):
        target = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            for block in chunks(descriptor, size, deadline):
                hasher.update(block)
                view = memoryview(block)
                while view:
                    check_deadline(deadline)
                    written = os.write(target, view)
                    require(written > 0, "Artifact copy made no progress")
                    view = view[written:]
            require(hasher.hexdigest() == expected["sha256"], "Input changed while copying")
            check_deadline(deadline)
            os.fsync(target)
            check_deadline(deadline)
        finally:
            os.close(target)
    return {"path": str(destination), "sha256": hasher.hexdigest(), "size_bytes": size}


def source_snapshot(output, names, deadline=None):
    check_deadline(deadline)
    directory = output / "harness-code"
    directory.mkdir()
    origins, retained = [], []
    for name in names:
        source = Path(__file__).with_name(name)
        origins.append(reference(source, 1024 * 1024, deadline))
        target = directory / name
        row = copy_artifact(source, target, origins[-1], 1024 * 1024, deadline)
        retained.append(row)
    return origins, retained


def sources_unchanged(origins, deadline=None):
    require(all(reference(Path(row["path"]), 1024 * 1024, deadline) == row for row in origins),
            "Harness source changed during run")


def artifact(value, maximum=64 * 1024 * 1024, root=None, deadline=None):
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
    actual = digest(path, value["size_bytes"], value["size_bytes"], deadline)
    require(actual == (value["sha256"], value["size_bytes"]), "Artifact bytes changed")
    return path


def read_bytes(path, maximum, deadline=None):
    with opened(path, maximum, deadline=deadline) as (descriptor, size):
        return b"".join(chunks(descriptor, size, deadline))


def load(path, maximum=64 * 1024 * 1024, deadline=None):

    def pairs(rows):
        result = {}
        for key, value in rows:
            require(key not in result, "Duplicate JSON key")
            result[key] = value
        return result

    result = json.loads(read_bytes(path, maximum, deadline).decode("utf-8"), object_pairs_hook=pairs,
                        parse_constant=lambda _: (_ for _ in ()).throw(Rejected("Nonfinite JSON")))
    check_deadline(deadline)
    return result


def build_input(value, profile, deadline=None):
    path = artifact(value, deadline=deadline)
    manifest = load(path, deadline=deadline)
    require(manifest.get("evidence_kind") == "actual-build" and
            manifest.get("task_id") == "E01-01.builds", "An accepted actual build is required")
    build = manifest["builds"]["geth"]
    binary = artifact(build["binary"], 256 * 1024 * 1024, path.parent, deadline)
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


def admit(path, started=None, deadline=None):
    started = time.monotonic() if started is None else started
    deadline = min(started + 1800, deadline) if deadline is not None else started + 1800
    request = load(path, 1024 * 1024, deadline)
    closed(request, {"schema_version", "build_manifest", "profile_association", "fixture",
                     "deadline_seconds"}, "request")
    require(type(request["schema_version"]) is int and request["schema_version"] == 1,
            "Unsupported request version")
    require(type(request["deadline_seconds"]) is int and 1 <= request["deadline_seconds"] <= 1800,
            "Run deadline must be between 1 and 1800 seconds")
    deadline = min(deadline, started + request["deadline_seconds"])
    check_deadline(deadline)
    build_path, manifest, binary, image = build_input(request["build_manifest"], request["profile_association"], deadline)
    fixture_path = artifact(request["fixture"], 1024 * 1024, deadline=deadline)
    fixture = load(fixture_path, deadline=deadline)
    closed(fixture, {"schema_version", "identity", "genesis", "blocks", "full_chain", "expected",
                     "prepared_blocks", "generator"}, "fixture")
    require(type(fixture["schema_version"]) is int and fixture["schema_version"] == 1,
            "Unsupported fixture version")
    paths = {key: artifact(fixture[key], 16 * 1024 * 1024, deadline=deadline) for key in
             ("genesis", "blocks", "full_chain", "expected", "generator")}
    genesis = load(paths["genesis"], deadline=deadline)
    config = genesis["config"]
    identity = fixture["identity"]
    closed(identity, {"kind", "chain_id", "genesis_hash", "config_sha256"}, "fixture identity")
    config_sha = hashlib.sha256(json.dumps(config, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    require(identity["kind"] == "private-local" and type(identity["chain_id"]) is int and
            identity["chain_id"] == config["chainId"] == 31337 and
            identity["config_sha256"] == config_sha and
            re.fullmatch(r"0x[0-9a-f]{64}", identity["genesis_hash"]), "Fixture identity mismatch")
    expected = load(paths["expected"], deadline=deadline)
    require(fixture["prepared_blocks"] and len(fixture["prepared_blocks"]) == 2,
            "Exactly two prepared blocks are required")
    from compare import validate_oracle
    validate_oracle(expected, fixture["prepared_blocks"])
    return {"request": request, "manifest": manifest, "build_path": build_path,
            "binary": binary, "image": image, "fixture": fixture, "paths": paths,
            "expected": expected, "deadline": deadline}
