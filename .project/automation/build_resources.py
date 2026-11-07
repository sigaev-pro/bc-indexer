"""Track and reconcile only Docker containers in one trusted build namespace."""

import json
from pathlib import Path
import re
import subprocess
import time

import evidence


LABEL_PREFIX = "io.extractor.build."
HEX64 = re.compile(r"[a-f0-9]{64}\Z")
IDENTIFIER = re.compile(r"[A-Za-z0-9_.:-]{1,160}\Z")
IMAGE = re.compile(r"[a-z0-9./:_-]+@sha256:[a-f0-9]{64}\Z")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def validate(spec):
    require(type(spec) is dict and set(spec) == {
        "schema_version", "run_id", "task_id", "operation_id", "nonce", "images"},
        "invalid owned Docker request fields")
    require(type(spec["schema_version"]) is int and spec["schema_version"] == 1,
            "invalid owned Docker request version")
    require(spec["task_id"] == "E01-01.builds" and all(
        isinstance(spec[key], str) and IDENTIFIER.fullmatch(spec[key])
        for key in ("run_id", "operation_id")), "invalid owned Docker task identity")
    require(isinstance(spec["nonce"], str) and HEX64.fullmatch(spec["nonce"]),
            "owned Docker namespace requires a full random nonce")
    require(type(spec["images"]) is list and 1 <= len(spec["images"]) <= 4
            and len(set(spec["images"])) == len(spec["images"])
            and all(isinstance(value, str) and IMAGE.fullmatch(value) for value in spec["images"]),
            "owned Docker images must be distinct content references")
    return spec


def labels(spec):
    validate(spec)
    return {LABEL_PREFIX + key: spec[key] for key in ("run_id", "task_id", "operation_id", "nonce")}


def command(arguments, timeout=10):
    result = subprocess.run(["docker", *arguments], capture_output=True, text=True, timeout=timeout)
    require(len(result.stdout.encode()) + len(result.stderr.encode()) <= 1024 * 1024,
            "Docker control output exceeded its bound")
    return result


def read_json(arguments, run=command):
    result = run(arguments)
    require(result.returncode == 0, "Docker control command failed: " + arguments[0])
    try:
        return json.loads(result.stdout)
    except (ValueError, TypeError) as error:
        raise ValueError("Docker control returned invalid JSON") from error


def members(spec, run=command):
    validate(spec)
    result = run(["ps", "--all", "--no-trunc", "--quiet", "--filter",
                  "label=" + LABEL_PREFIX + "nonce=" + spec["nonce"]])
    require(result.returncode == 0, "cannot inspect the owned Docker namespace")
    identifiers = result.stdout.splitlines()
    require(len(identifiers) <= 16 and len(set(identifiers)) == len(identifiers)
            and all(HEX64.fullmatch(value) for value in identifiers),
            "owned Docker namespace has an invalid or unbounded container set")
    return identifiers


def inspect_owned(identifier, spec, run=command):
    require(HEX64.fullmatch(identifier) is not None, "invalid immutable Docker container ID")
    rows = read_json(["container", "inspect", identifier], run)
    require(type(rows) is list and len(rows) == 1 and rows[0].get("Id") == identifier,
            "Docker inspection changed the container identity")
    row = rows[0]
    actual = row.get("Config", {}).get("Labels") or {}
    require(all(actual.get(key) == value for key, value in labels(spec).items()),
            "Docker container has foreign or incomplete ownership labels")
    require(row.get("Config", {}).get("Image") in spec["images"],
            "owned Docker container uses an unexpected image reference")
    require(isinstance(row.get("Image"), str) and re.fullmatch(r"sha256:[a-f0-9]{64}", row["Image"]),
            "owned Docker image has no immutable ID")
    return {"id": identifier, "image_id": row["Image"],
            "image_reference": row["Config"]["Image"], "labels": labels(spec),
            "running": row.get("State", {}).get("Running") is True}


def read_inventory(spec, path):
    path = Path(path)
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 65536,
            "owned Docker creation inventory is missing or unsafe")
    return validate_inventory(spec, json.loads(path.read_text()))


def validate_inventory(spec, value):
    require(type(value) is dict and set(value) == {"schema_version", "nonce", "intents"}
            and type(value["schema_version"]) is int and value["schema_version"] == 1
            and value.get("nonce") == spec["nonce"] and type(value.get("intents")) is list
            and len(value["intents"]) <= 16, "invalid owned Docker creation inventory")
    names, identifiers = set(), set()
    for row in value["intents"]:
        require(type(row) is dict and set(row) == {"name", "image_reference", "container_id"}
                and isinstance(row["name"], str) and re.fullmatch(r"extractor-[a-f0-9]{24}-[a-z0-9-]{1,40}", row["name"])
                and row["name"].startswith("extractor-" + spec["nonce"][:24] + "-")
                and row["name"] not in names and row["image_reference"] in spec["images"]
                and (row["container_id"] is None or isinstance(row["container_id"], str)
                     and HEX64.fullmatch(row["container_id"])), "invalid owned Docker creation intent")
        names.add(row["name"])
        if row["container_id"] is not None:
            require(row["container_id"] not in identifiers, "owned Docker creation inventory repeats an immutable ID")
            identifiers.add(row["container_id"])
    return value


def begin(spec, inventory, run=command):
    """A new request may not adopt an existing namespace by label alone."""
    require(not members(spec, run), "owned Docker namespace is not empty before execution")
    require(not Path(inventory).exists(), "owned Docker inventory already exists")
    evidence.atomic_json(inventory, {"schema_version": 1, "nonce": spec["nonce"], "intents": []}, exclusive=True)
    return {"schema_version": 1, "nonce": spec["nonce"], "empty": True}


def create(spec, inventory, step, image, options, arguments, run=command):
    """Sync intent before asking the daemon to create an immutable container."""
    validate(spec)
    require(isinstance(step, str) and re.fullmatch(r"[a-z0-9-]{1,40}", step), "invalid build container step")
    require(image in spec["images"] and all(isinstance(value, str) for value in options + arguments),
            "invalid build container command")
    value = read_inventory(spec, inventory)
    name = "extractor-" + spec["nonce"][:24] + "-" + step
    require(not any(row["name"] == name for row in value["intents"]) and len(value["intents"]) < 16,
            "build container step was already submitted or exceeds the namespace limit")
    row = {"name": name, "image_reference": image, "container_id": None}
    value["intents"].append(row)
    evidence.atomic_json(inventory, value)
    label_arguments = [part for key, val in labels(spec).items() for part in ("--label", key + "=" + val)]
    result = run(["container", "create", "--name", name, *label_arguments, *options, image, *arguments])
    require(result.returncode == 0 and HEX64.fullmatch(result.stdout.strip()),
            "Docker creation outcome is uncertain; reconcile the retained intent")
    row["container_id"] = result.stdout.strip()
    evidence.atomic_json(inventory, value)
    return row["container_id"]


def cleanup(spec, inventory, run=command):
    """Retain uncertainty on daemon errors or foreign ownership; never prune."""
    validate(spec)
    report = {"schema_version": 1, "nonce": spec["nonce"], "complete": False,
              "resources": [], "remaining": [], "started_at": time.time()}
    try:
        creation = read_inventory(spec, inventory)
        identifiers = members(spec, run)
        # Validate the whole set before mutating any daemon resource.
        owned = [inspect_owned(identifier, spec, run) for identifier in identifiers]
        known = {row["container_id"] for row in creation["intents"] if row["container_id"]}
        pending = [row for row in creation["intents"] if row["container_id"] is None]
        for intent in pending:
            inspected = read_json(["container", "inspect", intent["name"]], run)
            require(type(inspected) is list and len(inspected) == 1
                    and inspected[0].get("Id") in identifiers,
                    "pending Docker creation cannot be reconciled to an observed owned container")
            intent["container_id"] = inspected[0]["Id"]
            known.add(intent["container_id"])
        require(set(identifiers) <= known, "owned Docker namespace contains an unrecorded creation")
        evidence.atomic_json(inventory, creation)
        report["creation_inventory"] = creation
        for row in owned:
            if row["running"]:
                stopped = run(["container", "stop", "--time", "3", row["id"]])
                row["stop_exit"] = stopped.returncode
            removed = run(["container", "rm", "--force", row["id"]])
            row["remove_exit"] = removed.returncode
            report["resources"].append(row)
            require(removed.returncode == 0, "owned Docker container removal failed")
        report["remaining"] = members(spec, run)
        require(not report["remaining"], "owned Docker resources remain after cleanup")
        report["complete"] = True
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        report["error"] = str(error)
    report["finished_at"] = time.time()
    return report


def validate_cleanup(spec, report):
    validate(spec)
    require(type(report) is dict and type(report.get("schema_version")) is int and report["schema_version"] == 1
            and report.get("nonce") == spec["nonce"] and report.get("complete") is True
            and report.get("remaining") == [] and "error" not in report,
            "owned Docker cleanup is incomplete or belongs to another request")
    require(type(report.get("resources")) is list and len(report["resources"]) <= 16,
            "owned Docker cleanup has an invalid resource set")
    inventory = report.get("creation_inventory")
    validate_inventory(spec, inventory)
    require(all(isinstance(row.get("container_id"), str) and HEX64.fullmatch(row["container_id"])
                    for row in inventory["intents"]), "owned Docker cleanup has unresolved creation intents")
    known = {row["container_id"]: row["image_reference"] for row in inventory["intents"]}
    seen = set()
    for row in report["resources"]:
        require(type(row) is dict and isinstance(row.get("id"), str)
                and HEX64.fullmatch(row["id"]) and row["id"] not in seen
                and row["id"] in known and row.get("image_reference") == known[row["id"]]
                and isinstance(row.get("image_id"), str) and re.fullmatch(r"sha256:[a-f0-9]{64}", row["image_id"])
                and type(row.get("running")) is bool
                and row.get("labels") == labels(spec) and row.get("image_reference") in spec["images"]
                and type(row.get("remove_exit")) is int and row["remove_exit"] == 0,
                "owned Docker cleanup has invalid resource evidence")
        seen.add(row["id"])
    return report
