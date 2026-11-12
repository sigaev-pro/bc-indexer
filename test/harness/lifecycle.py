#!/usr/bin/env python3
"""Check the shared E01-02.lifecycle_seams core, validate its caller map, print its vectors, assemble its test module.

`check` refuses unless `adapters/<family>/lifecycle` holds the same file
names with the same bytes for Geth, BSC and Bor; the `native/` directory of
each family is not compared. `callers` validates `callers.json` against the
map rules of the package README, independently of the Go code, and prints a
per-client summary. `table` prints the caller map as one Markdown table per
client. `vectors` prints the expected digests of the fixed vector inputs,
computed here from the README formulas; the Go tests read the same values
from `vectors.json`, and a portable test checks that they match. `assemble
DIR` lays out source_inputs, admission, decision, lifecycle and the test
glue under `extractor/` in a new Go module named like the native trees; the
Go mutant table of the lifecycle package runs only there.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission  # noqa: E402
import decision  # noqa: E402

ROOT = admission.ROOT
FAMILIES = admission.FAMILIES
FAMILY_DIR = admission.FAMILY_DIR
PACKAGE_FILES = ("lifecycle.go", "finality.go", "callers.json", "lifecycle.mutants.json", "lifecycle_test.go",
                 "vectors.json")

CLIENTS = ("geth", "bsc", "bor")
EFFECTS = ("EXECUTION", "SYSTEM", "CANONICAL", "FINALITY")
ROUTES = ("OBSERVATION", "DECISION", "REFUSE", "READ_ONLY")
TRIGGERS = ("NEW_BLOCK", "NO_NEW_BLOCK", "KNOWN_BRANCH", "SIDE_CHAIN", "REORG", "STARTUP", "SYNC_MODE",
            "SEALING", "RPC", "MANUAL_COMMAND")
STATUSES = ("GUARDED", "PLANNED")
CALLER_FIELDS = ("id", "client", "source", "entry", "effects", "trigger", "route", "kinds", "status", "owner",
                 "patch", "refusal", "note")
ID = re.compile(r"(geth|bsc|bor)\.[a-z0-9]+(?:[._-][a-z0-9]+)*")
SOURCE = re.compile(r"[A-Za-z0-9_./-]+\.go:[A-Za-z_][A-Za-z0-9_.]*")
OWNER = re.compile(r"E[0-9]{2}-[0-9]{2}\.[a-z_]+")
COMMIT = re.compile(r"[0-9a-f]{40}")
ZERO = "0" * 64


def sha(data):
    return hashlib.sha256(data).digest()


def tag(name):
    return admission.tag(name)


def text(value):
    return admission.text(value)


def u64(value):
    return admission.u64(value)


def raw(digest):
    return admission.raw(digest)


def head(value):
    return u64(value["number"]) + raw(value["hash"])


def map_error(caller, message):
    return ValueError(f"caller {caller.get('id')!r}: {message}")


def check_caller(c):
    """Raise ValueError unless the caller follows the README map rules."""
    if sorted(c) != sorted(CALLER_FIELDS):
        raise map_error(c, f"fields {sorted(c)}")
    # The id pattern names one of the three clients, so the prefix check also
    # checks the client.
    if not ID.fullmatch(c["id"]) or not c["id"].startswith(c["client"] + "."):
        raise map_error(c, "id does not match its client")
    if not SOURCE.fullmatch(c["source"]):
        raise map_error(c, f"source {c['source']!r}")
    if not c["entry"] or "" in c["entry"]:
        raise map_error(c, "no entry point")
    if c["trigger"] not in TRIGGERS:
        raise map_error(c, f"trigger {c['trigger']!r}")
    effects, kinds = c["effects"], c["kinds"]
    if len(set(effects)) != len(effects) or len(set(kinds)) != len(kinds):
        raise map_error(c, "repeated effect or kind")
    if any(e not in EFFECTS for e in effects):
        raise map_error(c, f"effects {effects}")
    if c["refusal"] and c["route"] != "REFUSE":
        raise map_error(c, "refusal on a route other than REFUSE")
    route = c["route"]
    if route == "READ_ONLY":
        if effects or kinds:
            raise map_error(c, "READ_ONLY with effects or kinds")
    elif route == "OBSERVATION":
        if "EXECUTION" not in effects or not kinds or any(k not in ("IMPORT", "SIDE", "CANONICAL") for k in kinds):
            raise map_error(c, "OBSERVATION needs EXECUTION and IMPORT, SIDE or CANONICAL kinds")
    elif route == "DECISION":
        wanted = {"CANONICAL": "CANONICAL", "FINALITY": "FINALITY"}
        if not effects or any(e not in wanted for e in effects) or sorted(kinds) != sorted(wanted[e] for e in effects):
            raise map_error(c, "DECISION needs CANONICAL or FINALITY effects and exactly their kinds")
    elif route == "REFUSE":
        if not effects or kinds or not c["refusal"]:
            raise map_error(c, "REFUSE needs effects, a refusal and no kinds")
    else:
        raise map_error(c, f"route {route!r}")
    if c["status"] not in STATUSES:
        raise map_error(c, f"status {c['status']!r}")
    if (c["status"] == "GUARDED") != bool(c["patch"]):
        raise map_error(c, "a patch exactly when GUARDED")
    if not OWNER.fullmatch(c["owner"]):
        raise map_error(c, f"owner {c['owner']!r}")


def check_map(data):
    """Return the per-client summary; raise ValueError unless the map follows every rule."""
    version = data.get("schema_version")
    if sorted(data) != ["callers", "schema_version", "sources"] or type(version) is not int or version != 1:
        raise ValueError("map fields or schema version")
    if sorted(data["sources"]) != sorted(CLIENTS) or not all(COMMIT.fullmatch(v) for v in data["sources"].values()):
        raise ValueError("sources must pin every client to a 40-digit commit")
    seen = set()
    summary = {client: {"callers": 0, "routes": {}, "statuses": {}, "covered": set()} for client in CLIENTS}
    for c in data["callers"]:
        check_caller(c)
        if c["id"] in seen:
            raise map_error(c, "listed twice")
        seen.add(c["id"])
        s = summary[c["client"]]
        s["callers"] += 1
        s["routes"][c["route"]] = s["routes"].get(c["route"], 0) + 1
        s["statuses"][c["status"]] = s["statuses"].get(c["status"], 0) + 1
        if c["route"] in ("OBSERVATION", "DECISION"):
            s["covered"].update(c["effects"])
    for client in CLIENTS:
        missing = [e for e in EFFECTS if e not in summary[client]["covered"]]
        if missing:
            raise ValueError(f"{client} has no routed caller for {missing}")
        summary[client]["covered"] = sorted(summary[client]["covered"])
    return summary


def load_map(root=ROOT):
    return json.loads((root / "adapters" / "geth" / "lifecycle" / "callers.json").read_text())


def table(data):
    lines = []
    for client in CLIENTS:
        lines += [f"### {client}", "",
                  "| Caller | Source | Effects | Trigger | Route (kinds) | Status | Owner |", "|---|---|---|---|---|---|---|"]
        for c in data["callers"]:
            if c["client"] != client:
                continue
            route = c["route"] + (f" ({', '.join(c['kinds'])})" if c["kinds"] else "")
            status = c["status"] + (f": `{c['patch']}`" if c["patch"] else "")
            lines.append(f"| `{c['id']}` | `{c['source']}` | {', '.join(c['effects']) or '-'} | {c['trigger']} "
                         f"| {route} | {status} | {c['owner']} |")
        lines.append("")
    return "\n".join(lines)


def boundary_digest(store_id, producer, number, block, root):
    return sha(tag("extractor-lifecycle-boundary-v1") + text(store_id) + text(producer) + u64(number)
               + raw(block) + raw(root)).hex()


def finality_digest(field, caller, old, new):
    if new["hash"] == ZERO:
        raise ValueError("no-op finality change: zero hash")
    if new["hash"] == old["hash"] and new["number"] != old["number"]:
        raise ValueError("one block at two numbers")
    if new == old:
        raise ValueError("no-op finality change: unchanged")
    return sha(tag("extractor-lifecycle-finality-v1") + text(field) + text(caller) + head(old) + head(new)).hex()


def attestation_digest(status, justified, finalized):
    return sha(tag("extractor-lifecycle-attestation-v1") + text(status) + head(justified) + head(finalized)).hex()


# Fixed vector inputs.
STORE_ID = decision.STORE_ID
HEAD = {"number": 7, "hash": sha(b"head-7").hex()}
BOUNDARY_ROOT = sha(b"root-7").hex()
GENESIS = sha(b"genesis").hex()
OLD_FINALIZED = {"number": 3, "hash": sha(b"block-3").hex()}
NEW_FINALIZED = {"number": 5, "hash": sha(b"block-5").hex()}
NONE = {"number": 0, "hash": ZERO}
FCU_CALLER = "geth.engine-forkchoice"
ATT_SOURCE = {"number": 4, "hash": sha(b"block-4").hex()}
ATT_TARGET = {"number": 5, "hash": sha(b"block-5").hex()}


def vectors():
    base = admission.vectors()
    producer = f"{base['producer']}.{base['run']}"
    genesis = {"number": 0, "hash": GENESIS}
    return {
        "store_id": STORE_ID, "producer": base["producer"], "run": base["run"], "head": HEAD,
        "root": BOUNDARY_ROOT,
        "boundary_key": f"extractor-lifecycle-v1/{producer}/boundary",
        "boundary_digest": boundary_digest(STORE_ID, producer, HEAD["number"], HEAD["hash"], BOUNDARY_ROOT),
        "fcu_caller": FCU_CALLER, "old_finalized": OLD_FINALIZED, "new_finalized": NEW_FINALIZED,
        "finalized_digest": finality_digest("FINALIZED", FCU_CALLER, OLD_FINALIZED, NEW_FINALIZED),
        "first_safe_digest": finality_digest("SAFE", FCU_CALLER, NONE, NEW_FINALIZED),
        "genesis": GENESIS, "source": ATT_SOURCE, "target": ATT_TARGET,
        "attested_digest": attestation_digest("ATTESTED", ATT_TARGET, ATT_SOURCE),
        "fallback_digest": attestation_digest("GENESIS_FALLBACK", genesis, genesis),
    }


def shared_files(root, family):
    base = root / "adapters" / family / "lifecycle"
    if not base.is_dir():
        raise ValueError(f"missing shared package: {base.relative_to(root)}")
    return {path.relative_to(base).as_posix(): path.read_bytes() for path in sorted(base.rglob("*"))
            if path.is_file() and path.relative_to(base).parts[0] != FAMILY_DIR}


def check_identical(root=ROOT):
    """Return the shared file digests; raise ValueError if the copies differ."""
    copies = {family: shared_files(root, family) for family in FAMILIES}
    names = {family: sorted(files) for family, files in copies.items()}
    if not names[FAMILIES[0]]:
        raise ValueError("the shared package has no files")
    for family in FAMILIES[1:]:
        if names[family] != names[FAMILIES[0]]:
            raise ValueError(f"lifecycle file names differ: {names}")
    digests = {}
    for name in names[FAMILIES[0]]:
        values = {family: hashlib.sha256(copies[family][name]).hexdigest() for family in FAMILIES}
        if len(set(values.values())) != 1:
            raise ValueError(f"shared file {name} differs between families: {values}")
        digests[name] = values[FAMILIES[0]]
    return digests


def assemble(target, root=ROOT):
    """Write the test module into the new directory target and return its package directories."""
    target = Path(target)
    check_identical(root)
    # decision.assemble refuses an existing target before it writes.
    packages = decision.assemble(target, root)
    (target / "extractor/lifecycle").mkdir(parents=True)
    for name in PACKAGE_FILES:
        shutil.copyfile(root / "adapters" / "geth" / "lifecycle" / name, target / "extractor/lifecycle" / name)
    return sorted(packages + ["extractor/lifecycle"])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("check", "callers", "table", "vectors", "assemble"))
    parser.add_argument("target", nargs="?", help="new directory for assemble")
    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            output = check_identical(ROOT)
        elif args.command == "callers":
            output = check_map(load_map(ROOT))
        elif args.command == "table":
            data = load_map(ROOT)
            check_map(data)
            print(table(data))
            return 0
        elif args.command == "vectors":
            output = vectors()
        else:
            if not args.target:
                raise ValueError("assemble needs a target directory")
            output = assemble(args.target, ROOT)
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
