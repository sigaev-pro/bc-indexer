#!/usr/bin/env python3
"""Check the shared E01-02.decision_bridge core, print its vectors, assemble its test module.

`check` refuses unless `adapters/<family>/decision` holds the same file names
with the same bytes for Geth, BSC and Bor; the `native/` directory of each
family is not compared. `vectors` prints the expected digests and marker
bytes of the fixed vector inputs, computed here from the formulas in the
package README, independently of the Go code; the Go tests read the same
values from `vectors.json`, and a portable test checks that they match.
`assemble DIR` lays out source_inputs, admission, decision and the test glue
under `extractor/` in a new Go module named like the native trees.
"""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission  # noqa: E402

ROOT = admission.ROOT
FAMILIES = admission.FAMILIES
FAMILY_DIR = admission.FAMILY_DIR
PACKAGE_FILES = ("decision.go", "decision_test.go", "vectors.json")
GLUE_FILES = ("binding.go", "binding_test.go")


def sha(data):
    return hashlib.sha256(data).digest()


def u64(value):
    return admission.u64(value)


def text(value):
    return admission.text(value)


def raw(digest):
    return admission.raw(digest)


def tag(name):
    return admission.tag(name)


def head(value):
    return u64(value["number"]) + raw(value["hash"])


def intent_digest(record):
    data = (tag("extractor-decision-intent-v1") + text(record["native_store_id"]) + text(record["producer"])
            + u64(record["sequence"]) + text(record["kind"]) + head(record["old_head"]) + head(record["new_head"])
            + u64(len(record["members"])))
    for member in record["members"]:
        data += (text(member["observation"]) + raw(member["block"]) + raw(member["exec_ready_digest"])
                 + raw(member["child_root"]))
    data += raw(record["evidence_digest"]) + (b"\x01" if record["events"] else b"\x00")
    return sha(data).hex()


def decided_digest(intent, delivery):
    return sha(tag("extractor-decision-decided-v1") + raw(intent) + text(delivery)).hex()


def rejected_digest(intent, evidence):
    return sha(tag("extractor-decision-rejected-v1") + raw(intent) + raw(evidence)).hex()


def marker_key(producer, run, sequence):
    return f"extractor-decision-marker-v1/{producer}.{run}/{sequence:016x}"


def marker_value(record):
    """The JSON bytes of the intent record, fields in record order, no whitespace."""
    order = ("native_store_id", "producer", "sequence", "kind", "old_head", "new_head", "members",
             "evidence_digest", "events", "digest")
    members = [{name: member[name] for name in ("observation", "block", "exec_ready_digest", "child_root")}
               for member in record["members"]]
    value = {name: record[name] for name in order}
    value["members"] = members
    value["old_head"] = {"number": record["old_head"]["number"], "hash": record["old_head"]["hash"]}
    value["new_head"] = {"number": record["new_head"]["number"], "hash": record["new_head"]["hash"]}
    return json.dumps(value, separators=(",", ":")).encode()


def intent_record(sequence, kind, old_head, new_head, members, evidence, events):
    record = {"native_store_id": STORE_ID, "producer": f"{admission.PRODUCER}.{admission.RUN}",
              "sequence": sequence, "kind": kind, "old_head": old_head, "new_head": new_head, "members": members,
              "evidence_digest": evidence, "events": events}
    record["digest"] = intent_digest(record)
    return record


# Fixed vector inputs. The admission inputs come from the admission vectors,
# so the member EXEC_READY digest is the independent admission value.
STORE_ID = "native-store-1"
OLD_HEAD = {"number": 6, "hash": admission.CANDIDATE["parent_hash"]}
NEW_HEAD = {"number": 7, "hash": admission.CANDIDATE["hash"]}
CHILD_ROOT = sha(b"root-7").hex()
DECISION_EVIDENCE = sha(b"decision-evidence").hex()
REJECTION_EVIDENCE = sha(b"native-rejection").hex()


def vectors():
    base = admission.vectors()
    member = {"observation": base["key"], "block": base["candidate"]["hash"],
              "exec_ready_digest": base["exec_ready_digest"], "child_root": CHILD_ROOT}
    imported = intent_record(0, "IMPORT", OLD_HEAD, NEW_HEAD, [member], DECISION_EVIDENCE, True)
    finality = intent_record(1, "FINALITY", NEW_HEAD, NEW_HEAD, [], DECISION_EVIDENCE, False)
    decided = decided_digest(imported["digest"], "BATCH")
    rejected = rejected_digest(imported["digest"], REJECTION_EVIDENCE)
    return {
        "store_id": STORE_ID, "producer": base["producer"], "run": base["run"], "candidate": base["candidate"],
        "pin_digest": base["pin_digest"], "capsule_digest": base["capsule_digest"],
        "consumed_count": base["consumed_count"], "consumed_chain": base["consumed_chain"],
        "consumed_digest": base["consumed_digest"], "exec_digest": base["exec_digest"],
        "exec_ready_digest": base["exec_ready_digest"], "old_head": OLD_HEAD, "new_head": NEW_HEAD,
        "child_root": CHILD_ROOT, "decision_evidence": DECISION_EVIDENCE, "rejection_evidence": REJECTION_EVIDENCE,
        "import_intent": imported, "import_marker_key": marker_key(base["producer"], base["run"], 0),
        "import_marker": marker_value(imported).decode(), "import_decided_digest": decided,
        "import_rejected_digest": rejected,
        "member_decided_digest": admission.decided_digest(base["exec_ready_digest"], decided),
        "member_rejected_digest": admission.decided_digest(base["exec_ready_digest"], rejected),
        "finality_intent": finality, "finality_marker": marker_value(finality).decode(),
        "finality_decided_digest": decided_digest(finality["digest"], "CHECKPOINT"),
    }


def shared_files(root, family):
    base = root / "adapters" / family / "decision"
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
            raise ValueError(f"decision file names differ: {names}")
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
    if target.exists():
        raise ValueError(f"target exists: {target}")
    check_identical(root)
    packages = admission.assemble(target, root)
    extra = {
        "extractor/decision": (root / "adapters" / "geth" / "decision", PACKAGE_FILES),
        "extractor/decision_binding": (root / "test" / "harness" / "fixtures" / "decision_core", GLUE_FILES),
    }
    for package, (source, names) in extra.items():
        (target / package).mkdir(parents=True)
        for name in names:
            shutil.copyfile(source / name, target / package / name)
    return sorted(packages + list(extra))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("check", "vectors", "assemble"))
    parser.add_argument("target", nargs="?", help="new directory for assemble")
    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            output = check_identical(ROOT)
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
