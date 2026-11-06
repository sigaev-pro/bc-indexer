"""Validate the BOOT-08 definition and allocate obligations; never report runtime PASS."""

import argparse
from collections import Counter
import hashlib
import itertools
import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parent
SOURCE_DIGEST = "0a146c2a2796022be3a4ab69eff5826e443c10470d6a69d05779e50d99c4004d"
BASELINE_DIGEST = "522c07b28ef3828c3bd9117751852232a5c6514327a0ad904f496a9ad37b17ff"
ASSIGNMENT_DIGEST = "c358c152bbf4b315be39c75f4b3926532b7407de81b670c8be65c3306fb557e3"
RECIPE_DIGEST = "60c98b11cfbbe770358811bcb4782875c28ba1363aa31438ae61eb58e04a3980"
SUITES = {"quick", "functional", "fault", "soak", "public-smoke"}
MODES = {"document", "harness", "synthetic", "native-seam", "local-e2e", "public"}
PROFILE_IDS = {
    "ethereum-mainnet": ("ETH", 1), "ethereum-sepolia": ("ETH", 11155111),
    "ethereum-hoodi": ("ETH", 560048), "bsc-mainnet": ("BSC", 56),
    "bsc-testnet": ("BSC", 97), "polygon-pos-mainnet": ("POLY", 137),
    "polygon-pos-amoy": ("POLY", 80002),
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON field: {key}")
        result[key] = value
    return result


def read_json(path):
    return json.loads(Path(path).read_text(), object_pairs_hook=unique_object)


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def table_rows(text, prefix):
    return [[part.strip() for part in re.split(r"(?<!\\)\|", line)[1:-1]]
            for line in text.splitlines() if line.startswith("| " + prefix)]


def contract_metadata(root=ROOT):
    text = (root / ".project/docs/ACCEPTANCE_CASES.md").read_text()
    rows = table_rows(text, "AC-")
    row_digest = hashlib.sha256(("\n".join(line for line in text.splitlines()
                                         if line.startswith("| AC-")) + "\n").encode()).hexdigest()
    metadata = {row[0]: {"req": row[1], "gate": row[6]} for row in rows}
    decomposition = (root / ".project/docs/TASK_DECOMPOSITION.md").read_text()
    section = decomposition.split("## REQ/AC", 1)[1].split("## ", 1)[0]
    assigned = {}
    for row in table_rows(section, "REQ-"):
        task = re.search(r"E\d\d-\d\d\.[a-z_]+", row[2]).group()
        for group in row[1].split(","):
            limits = group.strip().split("…")
            if len(limits) == 1:
                ids = limits
            else:
                first, last = limits
                prefix = first.rsplit("-", 1)[0]
                require(last.rsplit("-", 1)[0] == prefix, "mixed AC range")
                ids = [f"{prefix}-{number:02d}" for number in
                       range(int(first.rsplit("-", 1)[1]), int(last.rsplit("-", 1)[1]) + 1)]
            for ac in ids:
                require(ac not in assigned, f"duplicate AC owner: {ac}")
                assigned[ac] = task
    require(set(assigned) == set(metadata), "AC task ownership is incomplete")
    for ac in metadata:
        metadata[ac]["owner"] = assigned[ac]
    return metadata, row_digest


def distinct(values, label):
    require(bool(values) and len(values) == len(set(values)), f"empty or duplicate {label}")


def validate_definition(catalog, sources, root=ROOT):
    metadata, row_digest = contract_metadata(root)
    require(catalog["schema_version"] == sources["schema_version"] == 1, "unknown definition schema")
    require(catalog["decision"] == sources["decision"] == "D90", "wrong definition decision")
    require(catalog["runtime"] == "NOT_RUN", "definition cannot claim runtime results")
    require(catalog["ac_rows_sha256"] == row_digest, "AC contract changed; review the corpus")
    require(digest(sources) == SOURCE_DIGEST, "source projection changed; review pinned source evidence")
    lock_path = root / ".project/bootstrap/components/candidate-lock.json"
    require(hashlib.sha256(lock_path.read_bytes()).hexdigest() == sources["candidate_lock_sha256"],
            "candidate lock changed; regenerate and review the source projection")
    lock = read_json(lock_path)
    pins = {(entry["source_repository"], entry["commit"])
            for entry in lock["components"].values()
            if "source_repository" in entry and "commit" in entry}
    for source in sources["sources"].values():
        require((source["repository"], source["commit"]) in pins, "source not bound to candidate")
        require(re.fullmatch(r"[0-9a-f]{40}", source["blob_sha1"]) is not None, "invalid source blob")
    require(set(catalog["profiles"]) == set(sources["profiles"]) == set(PROFILE_IDS), "profile registry differs")
    distinct(catalog["profiles"], "profiles")
    for profile, values in sources["profiles"].items():
        require((values["family"], values["chain_id"]) == PROFILE_IDS[profile], "profile identity differs")
        distinct([fork["field"] for fork in values["forks"]], f"fork fields: {profile}")
        for fork in values["forks"]:
            require(fork["unit"] in {"block", "timestamp", "total_difficulty"}, "unknown fork clock")
            require(fork["value"] is None or re.fullmatch(r"0|[1-9][0-9]*", fork["value"]) is not None,
                    "fork value must be an exact decimal string or null")
    ids = [case["ac"] for case in catalog["cases"]]
    distinct(ids, "AC assignments")
    require(set(ids) == set(metadata) and len(ids) == 217, "missing or extra AC assignment")
    baseline = "\n".join(f"{case['ac']}/{variant['id']}"
                         for case in catalog["cases"] for variant in case["variants"])
    require(hashlib.sha256(baseline.encode()).hexdigest() == BASELINE_DIGEST,
            "baseline variant set changed; review all affected AC variants")
    require(digest(catalog["cases"]) == ASSIGNMENT_DIGEST,
            "baseline profile/suite/mode assignment changed; review coverage")
    require(digest({key: catalog[key] for key in ["scenarios", "axes", "size_boundaries", "pairwise", "conditions"]})
            == RECIPE_DIGEST, "required recipe or expansion axis changed; review coverage")
    for case in catalog["cases"]:
        distinct(case["profiles"], f"profiles for {case['ac']}")
        require(set(case["profiles"]) <= set(PROFILE_IDS) | {"COMMON"}, "unknown case profile")
        require(case["suite"] in SUITES and case["mode"] in MODES, "unknown suite or evidence mode")
        require(case["oracle"] in catalog["oracles"], "unknown oracle")
        distinct([variant["id"] for variant in case["variants"]], f"variants for {case['ac']}")
        for variant in case["variants"]:
            require(re.fullmatch(r"[a-z][a-z0-9_]*", variant["id"]) is not None, "invalid variant name")
            require(set(variant.get("profiles", case["profiles"])) <= set(case["profiles"]),
                    f"variant broadens case scope: {case['ac']}/{variant['id']}")
            require(variant.get("mode", case["mode"]) in MODES, "invalid variant evidence mode")
            require("condition" not in variant or variant["condition"] in catalog["conditions"],
                    "unknown applicability condition")
    distinct([scenario["id"] for scenario in catalog["scenarios"]], "scenario recipes")
    for scenario in catalog["scenarios"]:
        require(scenario["ac"] in metadata, "scenario has unknown AC")
        require(set(scenario["profiles"]) <= set(PROFILE_IDS), "scenario has unknown profile")
        distinct(scenario["variants"], "scenario variants")
        require(set(scenario["modes"]) <= MODES and set(scenario["suites"]) <= SUITES,
                "invalid scenario mode or suite")
        require(scenario["oracle"] in catalog["oracles"], "unknown scenario oracle")
        require(bool(scenario["stimulus"]) and bool(scenario["expected"]), "scenario lacks a predicate")
    require(set(catalog["gates"]) == {f"E{number:02d}" for number in range(1, 8)}, "missing gate owner")
    require(all(value == "PENDING_E01_E06" for value in sources["runtime_outputs"].values()),
            "source projection cannot contain fabricated runtime artifacts")
    require(catalog["quotas"] == {
        "seed_count": 8, "actions_per_seed": 256, "standard_soak_seconds": 600,
        "standard_soak_actions": 1000, "extended_soak_seconds": 3600,
        "extended_soak_actions": 10000, "standard_total_seconds_max": 1800,
        "backlog_min_batches": 32, "backlog_queue_byte_multiplier": 2,
        "backlog_min_bytes": 67108864,
    }, "acceptance floor changed; review D90")
    return metadata


def activation_groups(profile, sources):
    """Keep clock domains separate; these source groups are not runtime interval hashes."""
    groups = {}
    for fork in sources["profiles"][profile]["forks"]:
        key = (fork["unit"], fork["value"])
        groups.setdefault(key, []).append(fork["field"])
    return [{"id": f"{unit}:{value if value is not None else 'disabled'}",
             "unit": unit, "value": value, "fields": sorted(fields)}
            for (unit, value), fields in sorted(groups.items(), key=lambda item: str(item[0]))]


def intervals(profile, sources):
    return ["current", "genesis"] + [f"after:{group['id']}" for group in activation_groups(profile, sources)
                          if group["value"] is not None]


def pairwise_rows(factors):
    names = sorted(factors)
    candidates = [dict(zip(names, values)) for values in itertools.product(*(factors[name] for name in names))]

    def pairs(row):
        return {(left, row[left], right, row[right]) for left, right in itertools.combinations(names, 2)}

    uncovered = set().union(*(pairs(row) for row in candidates))
    selected = []
    while uncovered:
        best = max(candidates, key=lambda row: len(pairs(row) & uncovered))
        selected.append(best)
        uncovered -= pairs(best)
        candidates.remove(best)
    return selected


def scenario_dimensions(scenario, profile, mode, variant, catalog, sources):
    axis = scenario["axis"]
    if axis == "each-execution-interval":
        return intervals(profile, sources) if mode == "native-seam" else ["current"]
    if axis == "each-size-boundary":
        return sorted(catalog["size_boundaries"])
    if axis == "each-pairwise-row":
        return [";".join(f"{key}={value}" for key, value in sorted(row.items()))
                for row in pairwise_rows(catalog["pairwise"]["factors"])]
    if axis == "each-topology":
        if variant == "single_profile":
            return [profile]
        if variant in {"all_seven", "shared_nats_outage"}:
            return ["+".join(sorted(PROFILE_IDS))]
        if variant == "one_profile_stopped":
            return ["all_seven:stopped=" + profile]
        pairs = []
        for first, second in itertools.combinations(sorted(PROFILE_IDS), 2):
            if profile not in {first, second}:
                continue
            different = PROFILE_IDS[first][0] != PROFILE_IDS[second][0]
            mainnet_testnet = first.endswith("mainnet") != second.endswith("mainnet")
            if ((variant == "different_family_pair" and different) or
                    (variant == "same_family_mainnet_testnet" and not different and mainnet_testnet)):
                pairs.append(first + "+" + second)
        return pairs
    require(axis == "current", "unknown expansion axis")
    return ["current"]


def expand(catalog, sources, metadata):
    cells = []

    def cell(ac, profile, suite, mode, recipe, variant, dimension="current", oracle=None, condition=None):
        key = "/".join([ac, recipe, variant, profile, suite, mode, dimension])
        cells.append({"cell_id": key, "ac": ac, **metadata[ac], "profile": profile,
                      "suite": suite, "mode": mode, "recipe": recipe, "variant": variant,
                      "dimension": dimension, "oracle": oracle or "native-and-contract",
                      "condition": condition, "runtime": "NOT_RUN", "resolution": "PENDING_E01_E06"})

    for case in catalog["cases"]:
        for variant in case["variants"]:
            for profile in variant.get("profiles", case["profiles"]):
                cell(case["ac"], profile, case["suite"], variant.get("mode", case["mode"]),
                     "baseline", variant["id"], oracle=case["oracle"], condition=variant.get("condition"))
    for scenario in catalog["scenarios"]:
        for profile, suite, mode in itertools.product(scenario["profiles"], scenario["suites"], scenario["modes"]):
            for variant in scenario["variants"]:
                for dimension in scenario_dimensions(scenario, profile, mode, variant, catalog, sources):
                    cell(scenario["ac"], profile, suite, mode, scenario["id"], variant, dimension,
                         scenario["oracle"], "source_rule_resolution")
    for profile, values in sources["profiles"].items():
        family = values["family"]
        axis = catalog["axes"]["fork_boundary"]
        groups = activation_groups(profile, sources)
        if profile in sources["consensus_forks"]:
            groups += [{"id": f"epoch:{name}:{epoch if epoch is not None else 'disabled'}",
                        "value": epoch} for name, epoch in sources["consensus_forks"][profile]["epochs"].items()]
        for group in groups:
            variants = (axis["disabled_variants"] if group["value"] is None else
                        axis["genesis_variants"] if group["value"] == "0" else axis["variants"])
            for variant in variants:
                cell(axis["ac"], profile, "fault" if variant == "restart" else "functional",
                     "native-seam", "fork-boundary", variant, group["id"], axis["oracle"])
        axis = catalog["axes"]["transaction_types"]
        for mode in axis["modes"]:
            dimensions = intervals(profile, sources) if mode == "native-seam" else ["current"]
            for native_type, dimension, variant in itertools.product(
                    sources["transaction_types"][family]["types"], dimensions, axis["variants"]):
                cell(axis["ac"], profile, "functional", mode, "transaction-type", variant,
                     f"{dimension}:{native_type}", axis["oracle"], "native_type_admission")
        axis = catalog["axes"]["precompiles"]
        for native_map in sources["precompile_maps"][family]:
            for entry in native_map["entries"]:
                address = "".join(f"{int(byte, 0):02x}" for byte in entry["address_bytes"])
                for mode, variant in itertools.product(axis["modes"], axis["variants"]):
                    cell(axis["ac"], profile, "functional", mode, "precompile", variant,
                         f"{native_map['name']}:{address}:{entry['implementation']}", axis["oracle"],
                         "native_map_selection")
        if family == "BSC":
            axis = catalog["axes"]["cas20"]
            for route, methods in sources["cas20"]["routes"].items():
                for method, mode, variant in itertools.product(methods, axis["modes"], axis["variants"]):
                    cell(axis["ac"], profile, "functional", mode, "cas20", variant,
                         f"Jenner:{route}:{method['selector_name']}", axis["oracle"], "route_feature_and_privilege")
    axis = catalog["axes"]["candidate_deltas"]
    for delta in sources["deltas"]:
        for profile, mode, variant in itertools.product(delta["profiles"], axis["modes"], delta["variants"]):
            cell(axis["ac"], profile, "functional", mode, delta["id"], variant,
                 oracle=axis["oracle"], condition="candidate_delta_resolution")
    distinct([item["cell_id"] for item in cells], "expanded cells")
    return cells


def positive_integer(value, name):
    require(type(value) is int and value > 0, f"{name} must be a positive integer")


def validate_run(run, catalog, by_id, diagnostic=False):
    require(run["suite"] in SUITES, "unknown run suite")
    require(run["duration_mode"] in {"standard", "extended"}, "unknown duration mode")
    positive_integer(run["total_seconds"], "total_seconds")
    require(run["duration_mode"] != "standard" or run["total_seconds"] <= 1800,
            "standard run exceeds 1800 seconds")
    require(run["duration_mode"] != "extended" or run["suite"] == "soak",
            "extended budget is reserved for explicitly selected soak")
    distinct(run["profiles"], "run profiles")
    require(set(run["profiles"]) <= set(PROFILE_IDS) | {"COMMON"}, "unknown run profile")
    distinct(run["cells"], "run cells")
    require(set(run["cells"]) <= set(by_id), "unknown allocated cell")
    for identity in run["cells"]:
        value = by_id[identity]
        require(value["suite"] == run["suite"] and value["profile"] in run["profiles"],
                "cell suite/profile differs from run")
    resources = run["resources"]
    for name in ["cpu_count", "ram_bytes", "disk_bytes", "queue_bytes", "parallelism"]:
        positive_integer(resources[name], name)
    require(resources["queue_bytes"] <= resources["ram_bytes"], "queue exceeds RAM")
    phases = run["phase_seconds"]
    required_phases = {"startup", "scenario", "shutdown_drain", "stream_fence", "reader_compare"}
    require(set(phases) == required_phases, "measured phases are incomplete")
    for name, seconds in phases.items():
        positive_integer(seconds, name)
    require(sum(phases.values()) <= run["total_seconds"], "phase budgets exceed the parent run")
    require(run["preparation_in_measured_run"] is False, "preparation cannot count as scenario execution")
    require(run["runtime"] == "NOT_RUN", "allocation is not a CaseResult")
    if run["suite"] == "public-smoke":
        require(set(run["signing"]) == set(run["profiles"]), "missing per-profile signer policy")
        for profile in run["profiles"]:
            signing = run["signing"][profile]
            require(signing in {"none", "testnet_only"}, "invalid public signer policy")
            require(not profile.endswith("mainnet") or signing == "none", "public mainnet must be passive")
            owned = any(by_id[identity]["recipe"] == "public-owned-contract" and
                        by_id[identity]["profile"] == profile for identity in run["cells"])
            require(not owned or signing == "testnet_only", "owned testnet corpus requires a testnet signer")
    if run["suite"] == "soak":
        quota = run["quotas"]
        floor = catalog["quotas"]
        for name in ["seed_count", "actions_per_seed", "soak_seconds", "soak_actions"]:
            positive_integer(quota[name], name)
        prefix = run["duration_mode"]
        if not diagnostic:
            require(quota["seed_count"] >= floor["seed_count"], "seed floor reduced")
            require(quota["actions_per_seed"] >= floor["actions_per_seed"], "seed action floor reduced")
            require(quota["soak_seconds"] >= floor[prefix + "_soak_seconds"], "soak duration floor reduced")
            require(quota["soak_actions"] >= floor[prefix + "_soak_actions"], "soak action floor reduced")
        require(quota["soak_seconds"] <= phases["scenario"], "soak cannot fit the scenario budget")
    backlog = any(by_id[identity]["variant"] in {"backlog_recovery", "backlog_drain"}
                  for identity in run["cells"])
    if backlog:
        values = run["backlog"]
        required_bytes = max(catalog["quotas"]["backlog_min_bytes"],
                             catalog["quotas"]["backlog_queue_byte_multiplier"] * resources["queue_bytes"])
        positive_integer(values["pending_batches"], "pending_batches")
        positive_integer(values["pending_bytes"], "pending_bytes")
        if not diagnostic:
            require(values["pending_batches"] >= catalog["quotas"]["backlog_min_batches"], "backlog batch floor reduced")
            require(values["pending_bytes"] >= required_bytes, "backlog does not exceed the RAM queue floor")
        require(values["pending_bytes"] <= resources["disk_bytes"], "backlog exceeds declared disk")
        require(values["plan_exact_bytes_ref"], "backlog has no exact publication-plan reference")
    return set(run["cells"])


def validate_pairwise(rows, factors, excluded_pairs):
    required = set()
    for first, second in itertools.combinations(sorted(factors), 2):
        for left, right in itertools.product(factors[first], factors[second]):
            required.add((first, left, second, right))
    excluded = set()
    for exclusion in excluded_pairs:
        pair = tuple(exclusion["pair"])
        require(pair in required and pair not in excluded, "invalid pairwise exclusion")
        require(exclusion["source_proof_ref"] and exclusion["review_ref"], "unproved pairwise exclusion")
        excluded.add(pair)
    covered = set()
    for row in rows:
        require(set(row) == set(factors), "pairwise row factors differ")
        require(all(row[name] in factors[name] for name in factors), "unknown pairwise value")
        for first, second in itertools.combinations(sorted(factors), 2):
            covered.add((first, row[first], second, row[second]))
    require(not (required - excluded - covered), "pairwise value pairs are missing")


def validate_campaign(plan, catalog, cells):
    """Check a static allocation, not E01's fixture readiness or runtime evidence."""
    require(plan["schema_version"] == 1 and plan["catalog_sha256"] == digest(catalog), "stale campaign definition")
    require(plan["purpose"] in {"acceptance", "diagnostic"}, "unknown campaign purpose")
    require(plan["runtime"] == "NOT_RUN", "a plan cannot claim runtime PASS")
    distinct(plan["profiles"], "campaign profiles")
    require(set(plan["profiles"]) <= set(PROFILE_IDS) | {"COMMON"}, "unknown campaign profile")
    distinct(plan["suites"], "campaign suites")
    require(set(plan["suites"]) <= SUITES, "unknown campaign suite")
    require(plan["runs"], "empty campaign")
    distinct([run["run_id"] for run in plan["runs"]], "run IDs")
    required = {cell["cell_id"] for cell in cells
                if cell["profile"] in plan["profiles"] and cell["suite"] in plan["suites"]}
    by_id = {cell["cell_id"]: cell for cell in cells}
    covered = set()
    for run in plan["runs"]:
        require(set(run["profiles"]) <= set(plan["profiles"]) and run["suite"] in plan["suites"],
                "run broadens campaign selection")
        covered |= validate_run(run, catalog, by_id, plan["purpose"] == "diagnostic")
    missing = sorted(required - covered)
    if plan["purpose"] == "acceptance":
        require(not missing, f"unassigned required cells: {len(missing)}")
    if "soak" in plan["suites"]:
        for profile in plan["profiles"]:
            if profile != "COMMON":
                item = plan["pairwise"][profile]
                validate_pairwise(item["rows"], catalog["pairwise"]["factors"], item["excluded_pairs"])
    return {"allocation": "PARTIAL" if missing or plan["purpose"] == "diagnostic" else "COMPLETE",
            "missing_cells": missing, "runtime": "NOT_RUN", "ready": False,
            "remaining": "E01/E06 must resolve fixtures, source applicability, compilers, oracles, and resources."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expand", action="store_true", help="Print every definition obligation as NOT_RUN.")
    parser.add_argument("--check-plan", type=Path, help="Check a static campaign allocation; never grant READY.")
    args = parser.parse_args()
    try:
        catalog = read_json(HERE / "catalog.json")
        sources = read_json(HERE / "candidate-sources.json")
        metadata = validate_definition(catalog, sources)
        cells = expand(catalog, sources, metadata)
        if args.expand:
            print(json.dumps(cells, indent=2))
        elif args.check_plan:
            print(json.dumps(validate_campaign(read_json(args.check_plan), catalog, cells), indent=2))
        else:
            print(json.dumps({"definition": "VALID", "runtime": "NOT_RUN", "profiles": len(PROFILE_IDS),
                              "acceptance_cases": len(metadata), "baseline_variants": 677,
                              "expanded_cells": len(cells), "by_mode": dict(Counter(cell["mode"] for cell in cells)),
                              "catalog_sha256": digest(catalog), "candidate_sources_sha256": digest(sources)}, indent=2))
        return 0
    except (OSError, ValueError, KeyError, TypeError, IndexError) as error:
        print(json.dumps({"definition": "INVALID", "runtime": "NOT_RUN", "error": str(error)}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
