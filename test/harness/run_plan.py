#!/usr/bin/env python3
"""Resolve and freeze declared test plans offline; never admit or execute runtime."""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import time

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from artifacts import Rejected, artifact, check_deadline, read_bytes, require
from freeze_output import publish

SPEC = importlib.util.spec_from_file_location("run_plan_evidence", HERE.parent / "evidence/validate.py")
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)
SCHEMA_NAME = "run-plan.schema.json"
JSON_LIMIT = 16 * 1024 * 1024
ARTIFACT_LIMIT = 1024 * 1024 * 1024
MAINNET = {"ethereum-mainnet", "bsc-mainnet", "polygon-pos-mainnet"}


def capture(path, deadline, expected=None):
    """Hash and parse the same bounded buffer with accepted exact-number semantics."""
    raw = read_bytes(path, JSON_LIMIT, deadline)
    sha = hashlib.sha256(raw).hexdigest()
    require(expected is None or sha == expected, "Registry bytes do not match the external trusted pin")
    value = evidence.parse_json(raw.decode("utf-8"))
    check_deadline(deadline)
    return raw, value, {"path": str(path), "sha256": sha, "size_bytes": len(raw)}


def fixture_key(row):
    return tuple(row[key] for key in ("profile", "fork_variant", "fixture_id", "fixture_version"))


def references(value):
    if isinstance(value, dict):
        if set(value) == {"path", "sha256", "size_bytes"}:
            yield value
        else:
            for item in value.values():
                yield from references(item)
    elif isinstance(value, list):
        for item in value:
            yield from references(item)


def check_timing_and_actions(plan, selected):
    caps, stop = plan["deadlines"], plan["stop"]
    measured = caps["measured_seconds"]
    require(plan["mode"] != "standard" or measured <= 1800, "Standard parent measured deadline exceeds 1800 seconds")
    require(plan["mode"] != "extended" or plan["suite"] == "soak", "Extended mode requires an explicit soak suite")
    require(caps["startup_seconds"] <= caps["preparation_seconds"], "Startup must fit the preparation deadline")
    require(caps["event_seconds"] <= measured and caps["comparison_seconds"] <= measured,
            "Event and comparison caps must be inside the parent measured deadline")
    require(plan["volumes"]["outage_seconds"] <= measured and plan["volumes"]["soak_seconds"] <= measured,
            "Outage and soak actions must fit inside measured execution")
    require(stop["duration_seconds"] is None or stop["duration_seconds"] <= measured, "Source stop duration exceeds measured execution")
    if plan["fixture_kind"] == "public":
        require(stop["driver_actions"] is None and (stop["duration_seconds"] is not None or stop["native_decisions"] is not None),
                "Public source needs a finite duration/decision trigger")
    else:
        require(stop["operator"] == "AND" and stop["native_decisions"] is not None and stop["driver_actions"] is not None,
                "Local source must finish both finite actions and native decisions")
        require(stop["driver_actions"] == sum(row["count"] for row in plan["actions"]), "Stop action quota differs from declared actions")
    quotas = [evidence.identity(row["case_identity"]) for row in plan["quotas"]]
    require(len(set(quotas)) == len(quotas) and set(quotas) == selected, "Every selected case requires exactly one fixed quota")


def check_actions_and_fence(plan, fixtures):
    require((plan["fixture_kind"] == "public") == (plan["suite"] == "public-smoke"), "Public fixtures require the separate public-smoke suite")
    faults = {row["fault_id"]: row for row in plan["faults"]}
    require(len(faults) == len(plan["faults"]), "Duplicate allowed fault")
    require(all(row["maximum_duration_seconds"] <= plan["deadlines"]["measured_seconds"] for row in faults.values()),
            "Fault delays must fit measured execution")
    actions = plan["actions"]
    require(len({row["action_id"] for row in actions}) == len(actions), "Duplicate scenario action")
    require(all(row["profile"] in plan["profiles"] for row in actions), "Action names an unselected profile")
    outcomes = {"native-import": {"applied"}, "observe": {"observed"}, "fault": {"fault-observed"},
                "controlled-transaction": {"included-success", "included-revert", "rejected-before-inclusion"}}
    used_faults, send_profiles = set(), set()
    for row in actions:
        require(row["outcome"] in outcomes[row["kind"]], "Action outcome does not match its kind")
        require(row["kind"] == "observe" or row["input"] is not None, "Scenario action needs bound inputs")
        if row["kind"] == "fault":
            require(row["fault_id"] in faults, "Fault action is not in the allowed fault declaration")
            used_faults.add(row["fault_id"])
        else:
            require(row["fault_id"] is None, "Nonfault action cannot nominate a fault")
        if row["kind"] == "controlled-transaction":
            identities = {fixture["identity"]["chain_id"] for fixture in fixtures.values() if fixture["profile"] == row["profile"]}
            require(len(identities) == 1 and row.get("signer_chain_id") in identities, "Controlled signer chain identity mismatch")
            send_profiles.add(row["profile"])
        else:
            require("signer_chain_id" not in row, "Only controlled transactions declare a signer identity")
        if plan["fixture_kind"] == "public":
            require(row["kind"] != "native-import", "Public plans cannot import private fixture blocks")
            if row["profile"] in MAINNET:
                require(row["kind"] == "observe", "Mainnet public smoke must remain passive")
    require(used_faults == faults.keys(), "Declared faults must have explicit scenario actions")
    if plan["fixture_kind"] == "public":
        require(set(plan["profiles"]) - MAINNET <= send_profiles, "Each public testnet requires controlled transaction outcomes")
    fence = plan["terminal_fence"]
    if fence is not None:
        require(fence["topology"]["kind"] != "standalone" or fence["topology"]["replicas"] == 1,
                "Standalone fence must declare one voting replica")
        expected = {"implementation_sha256": fence["implementation"]["sha256"], "source_commit": fence["source_commit"],
                    "build_sha256": fence["build"]["sha256"], "configuration_sha256": fence["configuration"]["sha256"],
                    "topology_digest": evidence.digest(fence["topology"])}
        require(all(fence["prior_conformance"][key] == value for key, value in expected.items()),
                "Terminal fence declaration uses a different implementation, lock, config or topology")


def resolve(plan_path, registry_path, registry_sha256, output=None):
    deadline = time.monotonic() + 30
    raw, plan, plan_ref = capture(plan_path, deadline)
    registry_raw, bundle, registry_ref = capture(registry_path, deadline, registry_sha256)
    require(isinstance(bundle, dict) and bundle.get("kind") == "CaseRegistry", "Expected the existing CaseRegistry validation bundle")
    evidence.validate_bundle(bundle)
    schema_raw, schema, schema_ref = capture(HERE / SCHEMA_NAME, deadline)
    validator = evidence.SchemaValidator()
    validator.check_vocabulary(schema)
    validator.documents[SCHEMA_NAME] = schema
    validator.documents["../evidence/evidence-definitions.schema.json"] = validator.documents["evidence-definitions.schema.json"]
    validator.validate(plan, SCHEMA_NAME)
    require(type(plan["schema_version"]) is int and plan["schema_version"] == 1, "Unsupported RunPlan version")
    registry = bundle["document"]
    known = {evidence.identity(case): case for case in registry["cases"]}
    profiles = set(plan["profiles"])
    selected = {evidence.identity(case) for case in plan["case_identities"]}
    require(len(selected) == len(plan["case_identities"]), "Duplicate selected case identity")
    require(selected <= known.keys(), "Unknown or stale selected case identity")
    matching = {key: case for key, case in known.items() if case["profile"] in profiles and
                case["suite"] == plan["suite"] and case["fixture_kind"] == plan["fixture_kind"]}
    require(selected <= matching.keys(), "Selected case differs from plan profile, suite or fixture kind")
    require(all(known[key]["applicability"] == "APPLICABLE" for key in selected), "Excluded cases cannot be selected for execution")
    applicable = {key for key, case in matching.items() if case["applicability"] == "APPLICABLE"}
    omitted = applicable - selected
    require(plan["coverage"] == "PARTIAL" or not omitted, "Complete selection omits applicable cases")
    require({known[key]["profile"] for key in selected} == profiles, "A selected profile has no selected applicable case")
    check_timing_and_actions(plan, selected)
    fixtures = {fixture_key(row): row for row in plan["fixtures"]}
    require(len(fixtures) == len(plan["fixtures"]), "Duplicate fixture binding")
    required_fixtures = {fixture_key(known[key]) for key in selected}
    require(fixtures.keys() == required_fixtures, "Missing or unsupported fixture version/fork binding")
    sources = {fixture_key(row): row for row in plan["sources"]}
    require(len(sources) == len(plan["sources"]) and sources.keys() == fixtures.keys(), "Source start conditions must bind every fixture exactly once")
    for key, fixture in fixtures.items():
        start, identity = sources[key]["start"], fixture["identity"]
        require(int(identity["chain_id"]) < 2 ** 256, "Chain ID exceeds the native integer bound")
        if start["kind"] == "prepared-genesis":
            require(start["height"] == 0 and start["block_hash"] == identity["genesis_hash"], "Prepared genesis start identity mismatch")
        if plan["fixture_kind"] == "public":
            require(identity["kind"] == "public" and start["kind"] == "synced-head", "Private fixture or unsynced start cannot claim a public run")
        else:
            require(start["kind"] != "synced-head", "Local plans cannot hide public synchronization")
    check_actions_and_fence(plan, fixtures)
    profile_rows = {row["profile"]: row for row in plan["profile_manifests"]}
    require(len(profile_rows) == len(plan["profile_manifests"]) and profile_rows.keys() <= profiles, "Duplicate or foreign ProfileManifest declaration")
    for key in selected:
        row = profile_rows.get(known[key]["profile"])
        require(row is None or row["profile_manifest_digest"] == known[key]["profile_manifest_digest"], "ProfileManifest declaration differs from case binding")

    # Every ref is byte-authenticated only. These checks do not establish native,
    # ProfileManifest, oracle independence, resource or fence conformance truth.
    resolved = {}
    for ref in references(plan):
        path = Path(ref["path"])
        require(".." not in path.parts, "Artifact path traversal is forbidden")
        actual = artifact(ref, ARTIFACT_LIMIT, None if path.is_absolute() else plan_path.parent, deadline)
        bound = {**ref, "path": str(actual)}
        identity = (bound["path"], bound["sha256"], bound["size_bytes"])
        resolved[identity] = bound

    blockers = ["runtime_execution_unimplemented", "profile_manifest_verification_unimplemented",
                "native_and_supporting_readiness_unverified", "resource_availability_unverified",
                "build_fixture_compatibility_unverified", "independent_oracle_review_required"]
    if profile_rows.keys() != profiles:
        blockers.append("profile_manifest_missing")
    if any(row["identity"]["kind"] == "test-only" for row in fixtures.values()):
        blockers.append("private_identity_has_no_public_profile_credit")
        if any(row["public_correspondence"] is None for row in fixtures.values()):
            blockers.append("public_correspondence_missing")
        else:
            blockers.append("public_correspondence_unverified")
    if plan["fixture_kind"] != "native-seam":
        blockers.append("terminal_fence_missing" if plan["terminal_fence"] is None else "terminal_fence_conformance_unverified")
        blockers.append("delivery_reader_boundary_unimplemented")
    rows = [{"identity": {field: case[field] for field in evidence.IDENTITY_FIELDS},
             "selected": key in selected, "applicability": case["applicability"], "execution": "NOT_RUN"}
            for key, case in sorted(matching.items())]
    report = {"schema_version": 1, "kind": "offline-run-plan-resolution", "plan_valid": True,
              "runtime_admitted": False, "plan": plan_ref, "registry_bundle": registry_ref,
              "registry_version": registry["version"], "registry_digest": evidence.digest(registry),
              "schema": schema_ref, "artifact_root": str(plan_path.parent),
              "coverage": {"declaration": plan["coverage"], "selected": len(selected), "applicable": len(applicable),
                           "omitted_applicable": len(omitted), "excluded": len(matching) - len(applicable)},
              "cases": rows, "blockers": blockers, "artifacts": list(resolved.values())}
    report["resolution_digest"] = evidence.digest(report)
    for ref in [plan_ref, registry_ref, schema_ref, *resolved.values()]:
        artifact(ref, max(JSON_LIMIT, ARTIFACT_LIMIT), deadline=deadline)
    if output is not None:
        def verify_authority():
            for ref in [plan_ref, registry_ref, schema_ref, *resolved.values()]:
                artifact(ref, max(JSON_LIMIT, ARTIFACT_LIMIT), deadline=deadline)
        publish(output, {"plan.json": raw, "registry.json": registry_raw, SCHEMA_NAME: schema_raw},
                evidence.canonical(report), verify_authority, deadline)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("check", "freeze"))
    parser.add_argument("plan", type=Path)
    parser.add_argument("--registry-bundle", type=Path, required=True)
    parser.add_argument("--registry-sha256", required=True)
    parser.add_argument("--output", type=Path,
                        help="new absolute directory; caller must exclusively own it and its parent through publication and later consumption")
    args = parser.parse_args()
    try:
        require(args.plan.is_absolute() and args.registry_bundle.is_absolute(), "Plan and registry paths must be absolute")
        require((args.mode == "freeze") == (args.output is not None), "Only freeze requires an output directory")
        report = resolve(args.plan, args.registry_bundle, args.registry_sha256, args.output)
        print(evidence.canonical(report).decode(), end="")
        return 0
    except (OSError, ValueError, KeyError, TypeError, RecursionError) as error:
        print(json.dumps({"plan_valid": False, "runtime_admitted": False, "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
