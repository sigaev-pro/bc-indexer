#!/usr/bin/env python3
"""Check the E01-04.early_faults catalog and judge one native faults report.

`check` validates faults.json. `judge REPORT [--output FILE]` compares one
family's report with the catalog and prints the verdicts as JSON. The judge
never changes the catalog from a report.
"""

import argparse
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
CATALOG = HERE / "faults.json"

FAMILIES = ("eth", "bsc", "poly")
VARIANTS = ("crash", "power_loss")
SEAMS = ("H", "admission", "pins", "EXEC_READY", "decision")
H_RULES = ("kept", "kept_if_present_else_new")
HANDOFF_KINDS = ("fault", "judge", "doc")
HANDOFF_VERDICTS = ("PASS", "FAIL", "NOT_RUN")
STATE_FIELDS = ("mode", "admission", "pin", "decision")
STORAGE_MODEL = "pebble-strict-mem"
HEX64 = re.compile(r"[0-9a-f]{64}")


class CatalogError(ValueError):
    pass


def load_json(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def is_int(value):
    return isinstance(value, int) and not isinstance(value, bool)


def check_catalog(catalog):
    """Raise CatalogError when the catalog is malformed; return the points by id."""
    if catalog.get("schema_version") != 1 or catalog.get("kind") != "native-faults-catalog":
        raise CatalogError("catalog: wrong schema_version or kind")
    if tuple(catalog.get("families", ())) != FAMILIES:
        raise CatalogError("catalog: families must be eth, bsc, poly")
    if tuple(sorted(catalog.get("variants", {}))) != tuple(sorted(VARIANTS)):
        raise CatalogError("catalog: variants must be crash and power_loss")
    states = catalog.get("states", {})
    if tuple(sorted(states)) != tuple(sorted(STATE_FIELDS)):
        raise CatalogError("catalog: states must name mode, admission, pin, decision")
    points = {}
    for point in catalog.get("points", []):
        pid = point.get("id")
        if not isinstance(pid, str) or not pid:
            raise CatalogError("catalog: point without id")
        if pid in points:
            raise CatalogError(f"catalog: duplicate point {pid}")
        if point.get("seam") not in SEAMS:
            raise CatalogError(f"{pid}: unknown seam")
        if not isinstance(point.get("pending"), bool):
            raise CatalogError(f"{pid}: pending must be true or false")
        if point.get("h_rule") not in H_RULES:
            raise CatalogError(f"{pid}: unknown h_rule")
        if not point.get("boundary") or not point.get("source"):
            raise CatalogError(f"{pid}: boundary and source are required")
        if not isinstance(point.get("ac"), list):
            raise CatalogError(f"{pid}: ac must be a list")
        after = point.get("after_restart", {})
        if tuple(sorted(after)) != tuple(sorted(VARIANTS)):
            raise CatalogError(f"{pid}: after_restart needs crash and power_loss")
        for variant, expected in after.items():
            if "boundary" not in expected:
                raise CatalogError(f"{pid}/{variant}: boundary expectation is required")
            for field, allowed in expected.items():
                vocabulary = ["absent", "present"] if field == "boundary" else states.get(field)
                if vocabulary is None:
                    raise CatalogError(f"{pid}/{variant}: unknown field {field}")
                if not allowed or any(value not in vocabulary for value in allowed):
                    raise CatalogError(f"{pid}/{variant}: bad values for {field}")
            if point["h_rule"] == "kept" and expected["boundary"] != ["present"]:
                raise CatalogError(f"{pid}/{variant}: h_rule kept needs a present boundary")
        points[pid] = point
    if not points:
        raise CatalogError("catalog: no points")
    if not any("AC-SCP-03-01" in point["ac"] for point in points.values()):
        raise CatalogError("catalog: no point carries AC-SCP-03-01")
    handoffs = set()
    for handoff in catalog.get("handoffs", []):
        hid = handoff.get("id")
        if not hid or hid in handoffs:
            raise CatalogError(f"catalog: missing or duplicate handoff {hid}")
        if handoff.get("block") not in FAMILIES or handoff.get("kind") not in HANDOFF_KINDS:
            raise CatalogError(f"{hid}: unknown block or kind")
        if not handoff.get("item") or not handoff.get("source"):
            raise CatalogError(f"{hid}: item and source are required")
        handoffs.add(hid)
    return points


def boundary_of(value, where, failures):
    """Return (number, hash, root) of a boundary object, or None when absent."""
    if value is None:
        return None
    if not isinstance(value, dict) or not is_int(value.get("number")) or value["number"] < 0 \
            or not HEX64.fullmatch(str(value.get("hash", ""))) \
            or not HEX64.fullmatch(str(value.get("root", ""))):
        failures.append(f"{where}: malformed boundary")
        return None
    return (value["number"], value["hash"], value["root"])


def judge_case(case, point, states):
    """Return the failures of one case against its catalog point."""
    failures = []
    tag = f"{case.get('point')}/{case.get('variant')}"
    variant = case.get("variant")
    fault = case.get("fault", {})
    if fault.get("hit") is not True or not is_int(fault.get("block")) or fault["block"] < 0:
        failures.append(f"{tag}: the fault point was not reached")
        return failures
    storage = case.get("storage", {})
    dropped = storage.get("unsynced_dropped")
    if not is_int(dropped) or dropped < 0:
        failures.append(f"{tag}: unsynced_dropped must be a count")
        dropped = 0
    if variant == "power_loss":
        if storage.get("reset_to_synced") is not True:
            failures.append(f"{tag}: power loss must reset the store to its synced state")
        if point["pending"] and dropped < 1:
            failures.append(f"{tag}: power loss dropped no pending write")
    elif storage.get("reset_to_synced") is not False or dropped != 0:
        failures.append(f"{tag}: a crash keeps every write")

    after = case.get("after_restart", {})
    restart_h = boundary_of(after.get("boundary"), f"{tag} after_restart", failures)
    expected = point["after_restart"][variant]
    presence = "present" if restart_h else "absent"
    if presence not in expected["boundary"]:
        failures.append(f"{tag}: boundary {presence} after restart, expected {expected['boundary']}")
    for field in STATE_FIELDS:
        value = after.get(field)
        if value not in states[field]:
            failures.append(f"{tag}: unknown {field} {value!r}")
        elif field in expected and value not in expected[field]:
            failures.append(f"{tag}: {field} {value} after restart, expected {expected[field]}")
    mode = after.get("mode")
    if (restart_h is None) != (mode == "BOOTSTRAP"):
        failures.append(f"{tag}: mode {mode} does not match the boundary")

    final = case.get("final", {})
    final_h = boundary_of(final.get("boundary"), f"{tag} final", failures)
    if final_h is None:
        failures.append(f"{tag}: no durable boundary at the end")
        return failures
    number = final_h[0]
    head = final.get("head")
    if not is_int(head) or head < number:
        failures.append(f"{tag}: final head below H")
        return failures
    exported = final.get("exported")
    if not isinstance(exported, list) or not all(is_int(item) for item in exported):
        failures.append(f"{tag}: exported must list block numbers")
        exported = []
    if any(item <= number for item in exported):
        failures.append(f"{tag}: a block at or below H was exported (initial sync)")
    if head > number and number + 1 not in exported:
        failures.append(f"{tag}: block H+1 was not exported (loss window)")
    if sorted(exported) != list(range(number + 1, head + 1)):
        failures.append(f"{tag}: exported blocks are not exactly H+1 to the head")
    if final.get("duplicates") != 0:
        failures.append(f"{tag}: a block was exported more than once")
    if final.get("corpus") != "PASS":
        failures.append(f"{tag}: the native result does not match the smoke corpus")

    rule = point["h_rule"]
    if restart_h is not None and restart_h != final_h:
        failures.append(f"{tag}: H changed after the boundary was durable")
    if rule == "kept" and restart_h is None:
        failures.append(f"{tag}: H was not kept")
    if restart_h is None and number < fault["block"]:
        failures.append(f"{tag}: the new H is below the fault point")
    if point["seam"] == "H" and restart_h is not None and restart_h[0] != fault["block"]:
        failures.append(f"{tag}: the kept H is not the head of the fault")
    if point["seam"] != "H" and not number < fault["block"] <= head:
        failures.append(f"{tag}: the fault block is not a live block")
    return failures


def judge(report, catalog):
    """Return the verdict document for one family's report."""
    points = check_catalog(catalog)
    states = catalog["states"]
    failures = []
    if report.get("schema_version") != 1 or report.get("kind") != "native-faults-report":
        return {"result": "FAIL", "failures": ["report: wrong schema_version or kind"]}
    family = report.get("family")
    if family not in FAMILIES:
        return {"result": "FAIL", "failures": ["report: unknown family"]}
    if report.get("storage_model") != STORAGE_MODEL:
        failures.append(f"report: storage_model must be {STORAGE_MODEL}")
    verdicts = {}
    for case in report.get("cases", []):
        key = (case.get("point"), case.get("variant"))
        tag = f"{key[0]}/{key[1]}"
        if key[0] not in points or key[1] not in VARIANTS:
            failures.append(f"{tag}: unknown point or variant")
            continue
        if tag in verdicts:
            failures.append(f"{tag}: reported twice")
            continue
        case_failures = judge_case(case, points[key[0]], states)
        failures.extend(case_failures)
        verdicts[tag] = "FAIL" if case_failures else "PASS"
    for pid in points:
        for variant in VARIANTS:
            verdicts.setdefault(f"{pid}/{variant}", "NOT_RUN")
    handoffs = {}
    wanted = [h["id"] for h in catalog.get("handoffs", []) if h["block"] == family]
    for item in report.get("handoffs", []):
        hid = item.get("id")
        if hid not in wanted:
            failures.append(f"handoff {hid}: not a handoff of {family}")
        elif item.get("verdict") not in HANDOFF_VERDICTS or not item.get("evidence"):
            failures.append(f"handoff {hid}: needs a verdict and evidence")
        else:
            handoffs[hid] = item["verdict"]
            if item["verdict"] == "FAIL":
                failures.append(f"handoff {hid}: FAIL")
    for hid in wanted:
        handoffs.setdefault(hid, "NOT_RUN")
    h_cases = [f"{pid}/{v}" for pid, p in points.items() if "AC-SCP-03-01" in p["ac"] for v in VARIANTS]
    h_values = {verdicts[tag] for tag in h_cases}
    ac = "FAIL" if "FAIL" in h_values else ("NOT_RUN" if "NOT_RUN" in h_values else "PASS")
    values = set(verdicts.values()) | set(handoffs.values())
    if failures:
        result = "FAIL"
    elif "NOT_RUN" in values:
        result = "INCOMPLETE"
    else:
        result = "PASS"
    return {"result": result, "family": family, "ac": {"AC-SCP-03-01": ac},
            "cases": dict(sorted(verdicts.items())), "handoffs": dict(sorted(handoffs.items())),
            "failures": failures}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("check", help="validate faults.json")
    run = sub.add_parser("judge", help="judge one family's report")
    run.add_argument("report")
    run.add_argument("--output")
    args = parser.parse_args(argv)
    catalog = load_json(CATALOG)
    if args.command == "check":
        points = check_catalog(catalog)
        print(f"catalog OK: {len(points)} points, {len(catalog['handoffs'])} handoffs")
        return 0
    verdict = judge(load_json(args.report), catalog)
    text = json.dumps(verdict, indent=2, sort_keys=True) + "\n"
    if args.output:
        Path(args.output).write_text(text, encoding="utf-8")
    sys.stdout.write(text)
    return 0 if verdict["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
