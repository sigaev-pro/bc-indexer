#!/usr/bin/env python3
"""Verify one test-only local ProfileManifest against its accepted family preparation."""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import time

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from artifacts import artifact, check_deadline, json_artifact, read_bytes, require

SPEC = importlib.util.spec_from_file_location("profile_manifest_evidence", HERE.parent / "evidence/validate.py")
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)
SCHEMA = "profile-manifest.schema.json"
JSON_LIMIT = 16 * 1024 * 1024
ARTIFACT_LIMIT = 1024 * 1024 * 1024

# Each accepted slice binds one family, one preparation proof kind, one request
# kind and one private identity. A manifest cannot move a fixture to another slice.
SLICES = {
    "geth-import": {"family": "ETH", "preparation": "fixture-preparation", "request": None, "fixture": None, "tool": None},
    "bsc-parlia-import": {"family": "BSC", "preparation": "bsc-fixture-preparation", "request": "bsc-native-import",
                          "fixture": "bsc-parlia", "tool": "bsc-native-observer"},
    "bor-import": {"family": "POLY", "preparation": "bor-fixture-preparation", "request": "bor-native-import",
                   "fixture": "bor-private", "tool": "bor-native-observer"},
}


def validator():
    checker = evidence.SchemaValidator()
    for name in (SCHEMA, "run-plan.schema.json"):
        schema = evidence.parse_json((HERE / name).read_text(encoding="utf-8"))
        checker.check_vocabulary(schema)
        checker.documents[name] = schema
    checker.documents["../evidence/evidence-definitions.schema.json"] = checker.documents["evidence-definitions.schema.json"]
    return checker


def family_of(profile):
    registry = evidence.parse_json((HERE.parent / "evidence/profile-registry.json").read_text(encoding="utf-8"))
    return registry["profiles"][profile]


def verify(manifest, deadline):
    """Return the canonical digest after checking every binding; raise on any mismatch."""
    validator().validate(manifest, SCHEMA)
    slice_rules = SLICES[manifest["runner"]["slice"]]
    require(manifest["family"] == slice_rules["family"] == family_of(manifest["profile"]),
            "Family, profile and runner slice disagree")
    for ref in (manifest["build_manifest"], manifest["oracle"], *manifest["inputs"]):
        artifact(ref, ARTIFACT_LIMIT, deadline=deadline)
    _, preparation = json_artifact(manifest["preparation"], JSON_LIMIT, deadline=deadline)
    # The accepted Geth preparation predates the cleanup field; BSC and Bor proofs must record it.
    cleanup = preparation.get("cleanup_complete", True) if slice_rules["family"] == "ETH" else preparation.get("cleanup_complete")
    require(preparation.get("kind") == slice_rules["preparation"] and preparation.get("result") == "PASS" and
            cleanup is True, "Preparation is not an accepted passing proof for this slice")
    require(preparation.get("request") == manifest["runner"]["request"], "Runner request is not the one the preparation produced")
    _, request = json_artifact(manifest["runner"]["request"], JSON_LIMIT, deadline=deadline)
    require(request.get("kind") == slice_rules["request"] and request.get("profile_association") == manifest["profile"],
            "Request kind or profile association differs from the manifest")
    require(request.get("build_manifest") == manifest["build_manifest"], "Request and manifest use different builds")
    require(preparation.get("fixture") == request.get("fixture"), "Preparation and request bind different fixtures")
    _, fixture = json_artifact(request["fixture"], JSON_LIMIT, deadline=deadline)
    require(fixture.get("kind") == slice_rules["fixture"], "Fixture kind differs from the runner slice")
    identity = fixture.get("identity")
    require(type(identity) is dict and identity.get("kind") == "private-local", "Fixture is not a private local identity")
    require(type(identity.get("chain_id")) is int and str(identity["chain_id"]) == manifest["identity"]["chain_id"] and
            identity.get("genesis_hash") == manifest["identity"]["genesis_hash"] and
            identity.get("config_sha256") == manifest["identity"]["native_config_sha256"],
            "Test-only identity differs from the prepared fixture")
    require(sorted(json.dumps(ref, sort_keys=True) for ref in manifest["inputs"]) ==
            sorted(json.dumps(fixture.get(name), sort_keys=True) for name in ("genesis", "blocks", "full_chain")),
            "Manifest inputs differ from the prepared genesis and blocks")
    require(manifest["oracle"] == fixture.get("expected"), "Manifest oracle differs from the independent expected file")
    tool = manifest["runner"]["observer_tool"]
    if slice_rules["tool"] is None:
        require(tool is None, "This slice has no separate observer tool")
    else:
        require(tool is not None and tool == preparation.get("observer_tool"), "Observer tool differs from the preparation")
        _, tool_value = json_artifact(tool, JSON_LIMIT, deadline=deadline)
        require(tool_value.get("kind") == slice_rules["tool"] and tool_value.get("build_manifest") == manifest["build_manifest"],
                "Observer tool kind or build differs")
    check_deadline(deadline)
    return evidence.digest(manifest)


def load_pinned(path, expected_sha256, deadline):
    raw = read_bytes(path, JSON_LIMIT, deadline)
    require(hashlib.sha256(raw).hexdigest() == expected_sha256, "ProfileManifest bytes do not match the external pin")
    return evidence.parse_json(raw.decode("utf-8"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--sha256", required=True)
    args = parser.parse_args()
    deadline = time.monotonic() + 60
    try:
        require(args.manifest.is_absolute(), "ProfileManifest path must be absolute")
        value = load_pinned(args.manifest, args.sha256, deadline)
        digest = verify(value, deadline)
        print(json.dumps({"valid": True, "profile": value["profile"], "profile_manifest_digest": digest,
                          "identity": "test-only", "public_profile_credit": False}))
        return 0
    except (OSError, ValueError, KeyError, TypeError, RecursionError) as error:
        print(json.dumps({"valid": False, "public_profile_credit": False, "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
