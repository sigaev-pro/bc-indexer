#!/usr/bin/env python3
"""Print the figures that an evidence section quotes from one retained run.

Usage: evidence_summary.py RUN_DIR [--test-log PATH ...]

RUN_DIR is one retained run directory: a NATS image preparation or fence run
(`preparation.json` or `image-fence.json`), a local native-seam runner run
(`run/run-record.json`) or a family fixture preparation (`preparation.json`).
The tool only reads. It verifies every absolute artifact reference (an object
with `path`, `sha256` and `size_bytes`) in the run proof, in each previous
attempt the proof cites, among the top-level references of each JSON document
(up to 1 MiB) the proof references directly and, for a runner run, in each
child proof. Any missing
file, size or hash mismatch, or conflicting reference fails closed: the tool
prints the problems to standard error, prints no summary and exits 1. Exit 2
means the directory is not a recognized run or its proof is malformed.

`--test-log` adds a test log kept outside the run (for example the output of a
`unittest` or `go test -v` command); the summary reports its SHA256 and the
test counts parsed from it.
"""

import argparse
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys

PROOF_FILES = ("image-fence.json", "preparation.json", "run/run-record.json")
NESTED_LIMIT = 1 << 20
GO_RESULT = re.compile(r"^(?P<indent>\s*)--- (?P<status>PASS|FAIL|SKIP): (?P<name>\S+) \((?P<seconds>[0-9.]+)s\)$")
GO_PACKAGE = re.compile(r"^(?P<status>ok|FAIL)\s+(?P<package>\S+)\s+(?P<seconds>[0-9.]+)s")
EXIT_RECORD = re.compile(r"(?:EXIT=)?(?P<code>-?\d+)")
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
PY_RAN = re.compile(r"^Ran (?P<count>\d+) tests? in (?P<seconds>[0-9.]+)s$")
PY_STATUS = re.compile(r"^(?P<status>OK|FAILED)(?: \((?P<detail>[^)]*)\))?$")


class FormatError(ValueError):
    """The directory is not a recognized run or its proof is malformed."""


class VerificationError(ValueError):
    """At least one reference did not match its retained file."""

    def __init__(self, problems):
        super().__init__("; ".join(problems))
        self.problems = problems


def is_reference(value):
    return (type(value) is dict and type(value.get("path")) is str and type(value.get("sha256")) is str
            and type(value.get("size_bytes")) is int)


def references(value):
    """Return every reference object in document order."""
    found = []
    stack = [value]
    while stack:
        item = stack.pop()
        if is_reference(item):
            found.append(item)
        if type(item) is dict:
            stack.extend(reversed(list(item.values())))
        elif type(item) is list:
            stack.extend(reversed(item))
    return found


def digest(path):
    """Hash one regular file; return (sha256, size) or raise OSError."""
    with open(path, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode):
            raise OSError(f"not a regular file: {path}")
        hasher = hashlib.sha256()
        size = 0
        for block in iter(lambda: stream.read(1 << 20), b""):
            hasher.update(block)
            size += len(block)
    return hasher.hexdigest(), size


class Verifier:
    """Verify references once per path and collect every problem."""

    def __init__(self):
        self.seen = {}
        self.problems = []

    def check(self, reference):
        path = reference["path"]
        claim = (reference["sha256"], reference["size_bytes"])
        if path in self.seen:
            if self.seen[path]["claim"] != claim:
                self.problems.append(f"conflicting references for {path}")
            return self.seen[path]["actual"] == claim
        try:
            actual = digest(path)
        except OSError as error:
            actual = None
            self.problems.append(f"missing or unreadable reference {path}: {error.strerror or error}")
        else:
            if actual[1] != claim[1]:
                self.problems.append(f"size mismatch for {path}: claimed {claim[1]}, found {actual[1]}")
            elif actual[0] != claim[0]:
                self.problems.append(f"SHA256 mismatch for {path}: claimed {claim[0]}, found {actual[0]}")
        self.seen[path] = {"claim": claim, "actual": actual}
        return actual == claim

    def survey(self, document):
        """Verify all absolute references in one document and count them."""
        found = references(document)
        unique = {}
        relative = []
        for reference in found:
            if os.path.isabs(reference["path"]):
                self.check(reference)
                unique.setdefault(reference["path"], reference["size_bytes"])
            else:
                relative.append(reference["path"])
        return {"references": len(found), "unique_absolute": len(unique),
                "bytes": sum(unique.values()), "relative": sorted(set(relative))}


def top_references(proof, verifier):
    """List the proof's top-level references and those of referenced JSON documents."""
    rows = []
    for key, value in proof.items():
        if not is_reference(value):
            continue
        rows.append({"name": key, "sha256": value["sha256"], "bytes": value["size_bytes"], "path": value["path"]})
        if (value["path"].endswith(".json") and value["size_bytes"] <= NESTED_LIMIT
                and os.path.isabs(value["path"]) and verifier.check(value)):
            nested = load_json(value["path"])
            if type(nested) is not dict:
                continue
            for name, inner in nested.items():
                if is_reference(inner) and os.path.isabs(inner["path"]):
                    verifier.check(inner)
                    rows.append({"name": f"{key}.{name}", "sha256": inner["sha256"],
                                 "bytes": inner["size_bytes"], "path": inner["path"]})
    return rows


def load_json(path):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise FormatError(f"cannot read JSON {path}: {error}") from error


def proof_path(run):
    present = [run / name for name in PROOF_FILES if (run / name).is_file()]
    if len(present) != 1:
        raise FormatError(f"expected exactly one of {', '.join(PROOF_FILES)} in {run}, found {len(present)}")
    return present[0]


def test_counts(text):
    """Parse Go `-v` and Python unittest results; return None without results."""
    go = {"methods": {}, "subtests": {}, "packages": {}}
    python = []
    for line in ANSI.sub("", text).splitlines():
        line = line.rstrip()
        match = GO_RESULT.match(line)
        if match:
            bucket = "subtests" if match["indent"] else "methods"
            go[bucket][match["status"]] = go[bucket].get(match["status"], 0) + 1
            continue
        match = GO_PACKAGE.match(line)
        if match:
            go["packages"][match["status"]] = go["packages"].get(match["status"], 0) + 1
            continue
        match = PY_RAN.match(line)
        if match:
            python.append({"tests": int(match["count"]), "seconds": float(match["seconds"]), "status": None})
            continue
        match = PY_STATUS.match(line)
        if match and python and python[-1]["status"] is None:
            python[-1]["status"] = match["status"]
            python[-1]["detail"] = match["detail"]
    result = {}
    if any(go.values()):
        result["go"] = go
    if python:
        result["python"] = python
    return result or None


def describe_tests(counts):
    if not counts:
        return ""
    parts = []
    if "go" in counts:
        go = counts["go"]
        for key, label in (("methods", "Go methods"), ("subtests", "Go subtests")):
            if go[key]:
                parts.append(label + " " + ", ".join(f"{count} {status}" for status, count in sorted(go[key].items())))
        if go["packages"]:
            parts.append("Go packages " + ", ".join(f"{count} {status}" for status, count in sorted(go["packages"].items())))
    for run in counts.get("python", []):
        detail = f" ({run['detail']})" if run.get("detail") else ""
        parts.append(f"Python {run['tests']} tests {run['status'] or 'NO_STATUS'}{detail} in {run['seconds']:.3f} s")
    return "; ".join(parts)


def read_text(reference):
    try:
        return Path(reference["path"]).read_text(errors="replace")
    except OSError:
        return ""


def command_rows(proof, verifier):
    rows = []
    commands = proof.get("commands")
    if commands is None:
        return rows
    if type(commands) is not list:
        raise FormatError("commands is not a list")
    for index, command in enumerate(commands):
        if type(command) is not dict or not is_reference(command.get("result")):
            raise FormatError(f"command {index} has no result reference")
        if not verifier.check(command["result"]):
            continue
        result = load_json(command["result"]["path"])
        if type(result) is not dict:
            raise FormatError(f"command {index} result is not a JSON object")
        text = "".join(read_text(command[name]) for name in ("stdout", "stderr")
                       if is_reference(command.get(name)) and verifier.check(command[name]))
        rows.append({"index": index, "exit_code": result.get("exit_code"),
                     "elapsed_seconds": result.get("elapsed_seconds"),
                     "cleanup_complete": result.get("cleanup_complete"),
                     "process_group_absent": result.get("process_group_absent"),
                     "reason": result.get("reason"), "tests": test_counts(text)})
    return rows


def lineage(proof, verifier):
    """Follow previous_attempt references from the newest attempt backward."""
    chain = []
    seen = set()
    previous = proof.get("previous_attempt")
    while previous is not None:
        if not is_reference(previous):
            raise FormatError("previous_attempt is not a reference")
        if previous["path"] in seen:
            raise FormatError("previous_attempt lineage loops")
        seen.add(previous["path"])
        if not verifier.check(previous):
            break
        document = load_json(previous["path"])
        if type(document) is not dict:
            raise FormatError(f"previous attempt is not a JSON object: {previous['path']}")
        chain.append({"path": previous["path"], "sha256": previous["sha256"], "result": document.get("result"),
                      "attempt_index": attempt_index(document), "correction": document.get("correction")})
        previous = document.get("previous_attempt")
    return chain


def attempt_index(document):
    if type(document.get("attempt")) is dict:
        return document["attempt"].get("attempt_index")
    return document.get("attempt_index")


def seconds_between(started, finished):
    def parse(value):
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    return (parse(finished) - parse(started)).total_seconds()


def phase_rows(proof):
    phases = proof.get("phases")
    if phases is None:
        return []
    if type(phases) is not dict:
        raise FormatError("phases is not an object")
    rows = []
    for name, phase in phases.items():
        try:
            seconds = seconds_between(phase["started_at"], phase["finished_at"])
        except (KeyError, TypeError, ValueError, AttributeError) as error:
            raise FormatError(f"phase {name} has no valid start and finish") from error
        rows.append({"name": name, "seconds": seconds, "deadline_seconds": phase.get("deadline_seconds")})
    return rows


def child_rows(proof, verifier):
    rows = []
    for child in proof.get("children") or []:
        if type(child) is not dict:
            raise FormatError("child record is not an object")
        child_proof = child.get("proof")
        nested = None
        if is_reference(child_proof) and verifier.check(child_proof):
            nested = verifier.survey(load_json(child_proof["path"]))
        rows.append({"profile": child.get("profile"), "slice": child.get("slice"), "result": child.get("result"),
                     "elapsed_seconds": child.get("elapsed_seconds"), "exit_code": child.get("exit_code"),
                     "process_group_absent": child.get("process_group_absent"),
                     "native_owner_nonce": child.get("native_owner_nonce"),
                     "proof_sha256": child_proof.get("sha256") if is_reference(child_proof) else None,
                     "proof_references": nested})
    return rows


def case_rows(proof):
    cases = proof.get("cases")
    if type(cases) is not dict:
        return []
    rows = []
    for name, case in cases.items():
        comparison = case.get("comparison") or {}
        report = case.get("report") if is_reference(case.get("report")) else {}
        rows.append({"name": name, "result": case.get("result"), "report_sha256": report.get("sha256"),
                     "report_bytes": report.get("size_bytes"), "stored_messages": comparison.get("stored_messages"),
                     "lower_sequence": comparison.get("lower_sequence"),
                     "upper_sequence": comparison.get("upper_sequence"), "groups": comparison.get("groups")})
    return rows


def launcher_records(run):
    """Read exit records kept beside the run, never inside its proof."""
    records = []
    candidates = [run.parent / (run.name + ".exit"), run / "run.exit"]
    for path in candidates:
        if path.is_file():
            match = EXIT_RECORD.fullmatch(path.read_text().strip())
            if not match:
                raise FormatError(f"exit record is not an integer or EXIT=integer: {path}")
            records.append({"path": path, "exit_code": int(match["code"])})
    completion = run.parent / (run.name + ".completion.json")
    if completion.is_file():
        document = load_json(completion)
        records.append({"path": completion, "exit_code": document.get("exit_code"),
                        "elapsed_seconds": document.get("elapsed_seconds"),
                        "source_commit": document.get("source_commit")})
    return records


def summarize(run, test_logs=()):
    """Return the summary data or raise FormatError/VerificationError."""
    run = Path(run).resolve()
    if not run.is_dir():
        raise FormatError(f"not a directory: {run}")
    path = proof_path(run)
    proof = load_json(path)
    if type(proof) is not dict or type(proof.get("kind")) is not str:
        raise FormatError(f"proof has no kind: {path}")
    verifier = Verifier()
    proof_sha256, proof_bytes = digest(path)
    survey = verifier.survey(proof)
    top = top_references(proof, verifier)
    summary = {
        "run": run, "proof": path, "proof_sha256": proof_sha256, "proof_bytes": proof_bytes,
        "kind": proof["kind"], "result": proof.get("result"), "run_id": proof.get("run_id"),
        "error": proof.get("error"), "attempt_index": attempt_index(proof), "correction": proof.get("correction"),
        "elapsed_seconds": proof.get("elapsed_seconds"), "deadline_seconds": proof.get("deadline_seconds"),
        "cleanup_complete": proof.get("cleanup_complete"), "native_owner_nonce": proof.get("native_owner_nonce"),
        "runtime_admitted": proof.get("runtime_admitted"), "public_profiles": proof.get("public_profiles"),
        "resolution_sha256": proof.get("resolution_sha256"), "teardown": proof.get("teardown"),
        "top_references": top, "references": survey, "lineage": lineage(proof, verifier),
        "commands": command_rows(proof, verifier), "phases": phase_rows(proof),
        "children": child_rows(proof, verifier), "cases": case_rows(proof),
        "launchers": launcher_records(run), "test_logs": [],
    }
    for log in test_logs:
        log = Path(log).resolve()
        try:
            sha256, size = digest(log)
        except OSError as error:
            raise FormatError(f"cannot read test log {log}: {error}") from error
        summary["test_logs"].append({"path": log, "sha256": sha256, "bytes": size,
                                     "tests": test_counts(log.read_text(errors="replace"))})
    if verifier.problems:
        raise VerificationError(verifier.problems)
    return summary


def number(value, digits=3):
    if type(value) in (int, float) and type(value) is not bool:
        return f"{value:.{digits}f}"
    return "n/a"


def flag(value):
    return {True: "true", False: "false", None: "n/a"}.get(value, str(value))


def sequence_range(row):
    """Render an exclusive lower frontier and an inclusive upper sequence as a range."""
    lower, upper = row["lower_sequence"], row["upper_sequence"]
    if type(lower) is not int or type(upper) is not int:
        return "n/a"
    if upper <= lower:
        return "none"
    return f"{lower + 1}–{upper}"


def render(summary):
    run = summary["run"]
    refs = summary["references"]
    lines = [f"### Evidence summary: `{run.name}`", "",
             "Generated by `test/harness/evidence_summary.py`; every figure is read from the retained files, "
             "and every absolute reference in the tool's documented scope matched its size and SHA256.", ""]
    lines.append(f"- Kind `{summary['kind']}`; result {summary['result']}; run ID `{summary['run_id']}`")
    if summary["error"] is not None:
        lines.append(f"- Error: {summary['error']}")
    lines.append(f"- Proof `{summary['proof'].relative_to(run)}` SHA256 `{summary['proof_sha256']}` "
                 f"({summary['proof_bytes']:,} bytes)")
    if summary["elapsed_seconds"] is not None:
        deadline = f" (deadline {summary['deadline_seconds']} s)" if summary["deadline_seconds"] is not None else ""
        lines.append(f"- Elapsed {number(summary['elapsed_seconds'])} seconds{deadline}")
    chain = summary["lineage"]
    if chain:
        history = "; ".join(f"`{Path(entry['path']).parent.name}` {entry['result']} attempt {entry['attempt_index']} "
                            f"(`{entry['sha256']}`)" for entry in chain)
        lines.append(f"- Attempt index {summary['attempt_index']}; previous attempts, newest first: {history}")
    else:
        lines.append(f"- Attempt index {summary['attempt_index']}; new lineage (no previous attempt)")
    if summary["correction"] is not None:
        lines.append(f"- Correction: {summary['correction']}")
    for key, label in (("cleanup_complete", "Cleanup complete"), ("runtime_admitted", "Runtime admitted"),
                       ("public_profiles", "Public profiles"), ("native_owner_nonce", "Native owner nonce"),
                       ("resolution_sha256", "Resolution SHA256")):
        if summary[key] is not None:
            value = summary[key]
            lines.append(f"- {label}: " + (f"`{value}`" if type(value) is str and key != "public_profiles" else flag(value)))
    for record in summary["launchers"]:
        extra = ""
        if record.get("elapsed_seconds") is not None:
            extra = f", {number(record['elapsed_seconds'])} seconds, source `{record.get('source_commit')}`"
        lines.append(f"- Launcher record `{record['path'].name}`: exit {record['exit_code']}{extra}")
    lines.append(f"- Artifact references: {refs['references']} in the proof, {refs['unique_absolute']} unique absolute "
                 f"({refs['bytes']:,} bytes), 0 mismatches")
    if refs["relative"]:
        lines.append("- Relative references (not verified): " + ", ".join(f"`{path}`" for path in refs["relative"]))
    if summary["top_references"]:
        lines += ["", "| Reference | SHA256 | Bytes |", "|---|---|---|"]
        for row in summary["top_references"]:
            lines.append(f"| `{row['name']}` | `{row['sha256']}` | {row['bytes']:,} |")
    if summary["phases"]:
        lines += ["", "| Phase | Seconds | Deadline |", "|---|---|---|"]
        for row in summary["phases"]:
            lines.append(f"| {row['name']} | {number(row['seconds'])} | {row['deadline_seconds']} |")
    if summary["cases"]:
        # lower_sequence is an exclusive frontier: messages 1..5 have lower 0, upper 5.
        lines += ["", "| Case | Result | Native report SHA256 | Report bytes | Stored | Sequences | Groups |",
                  "|---|---|---|---|---|---|---|"]
        for row in summary["cases"]:
            groups = ", ".join(f"`{group}`" for group in row["groups"] or []) or "none"
            report_bytes = f"{row['report_bytes']:,}" if row["report_bytes"] is not None else "n/a"
            lines.append(f"| {row['name']} | {row['result']} | `{row['report_sha256']}` | {report_bytes} | "
                         f"{row['stored_messages']} | {sequence_range(row)} | {groups} |")
    if summary["commands"]:
        lines += ["", "| Command | Exit | Seconds | Cleanup complete | Process group absent | Reason | Tests |",
                  "|---|---|---|---|---|---|---|"]
        for row in summary["commands"]:
            lines.append(f"| {row['index']:03d} | {row['exit_code']} | {number(row['elapsed_seconds'])} | "
                         f"{flag(row['cleanup_complete'])} | {flag(row['process_group_absent'])} | "
                         f"{row['reason'] if row['reason'] is not None else 'none'} | {describe_tests(row['tests']) or 'none'} |")
    if summary["children"]:
        lines += ["", "| Profile | Slice | Result | Child seconds | Exit | Process group absent | Native owner nonce | "
                  "Proof SHA256 | Proof references |", "|---|---|---|---|---|---|---|---|---|"]
        for row in summary["children"]:
            nested = row["proof_references"]
            nested_text = (f"{nested['unique_absolute']} unique ({nested['bytes']:,} bytes)" if nested else "n/a")
            lines.append(f"| {row['profile']} | {row['slice']} | {row['result']} | {number(row['elapsed_seconds'])} | "
                         f"{row['exit_code']} | {flag(row['process_group_absent'])} | `{row['native_owner_nonce']}` | "
                         f"`{row['proof_sha256']}` | {nested_text} |")
    teardown = summary["teardown"]
    if type(teardown) is dict:
        flags = ", ".join(f"{key} {flag(value) if not isinstance(value, list) else len(value)}"
                          for key, value in teardown.items())
        lines += ["", f"Teardown: {flags}."]
    for log in summary["test_logs"]:
        lines += ["", f"Test log `{log['path'].name}` SHA256 `{log['sha256']}` ({log['bytes']:,} bytes): "
                      f"{describe_tests(log['tests']) or 'no test results found'}."]
    return "\n".join(lines) + "\n"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("run", type=Path, help="retained run directory")
    parser.add_argument("--test-log", action="append", default=[], type=Path,
                        help="test log kept outside the run; may repeat")
    arguments = parser.parse_args(argv)
    try:
        summary = summarize(arguments.run, arguments.test_log)
    except VerificationError as error:
        for problem in error.problems:
            print(f"MISMATCH: {problem}", file=sys.stderr)
        print(f"FAIL: {len(error.problems)} reference problem(s); no summary printed", file=sys.stderr)
        return 1
    except (FormatError, OSError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 2
    sys.stdout.write(render(summary))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
