#!/usr/bin/env python3
"""Run single-check mutants of the state-provider packages in owned containers.

The table (test/harness/pins.mutants.json) names for every mutant the family
of the native module it runs in. Each mutant replaces exactly one fragment of
a state-provider source inside a fresh copy of the accepted native tree of
its family, must compile, and is KILLED when the compiled tests fail and name
a failing test. A survivor passes only when the table labeled it equivalent,
with a reason, before the run. Before the mutants, the unmutated tests of
every family compile and pass in the same trees.

The run holds the host-wide single-writer lock (pins.owned_attempt), and the
adapter and harness files must equal the committed source. Private local
evidence gives no public-profile credit.

  python3 -I test/harness/pins_mutants.py mutants TOOL.json --sha256 TOOL_PIN --table TABLE \
    --profile-manifest PATH SHA [--profile-manifest PATH SHA ...] --output NEW_DIRECTORY [--only NAME ...]

TOOL.json is the tool of `pins.py prepare`, so the mutants run on the build
and the adapter sources that the pins evidence names. Output paths must be
new; a refused run writes no record.
"""

import argparse
import json
from pathlib import Path
import shutil
import signal
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
import bsc_source_inputs as probe14  # noqa: E402
import pins  # noqa: E402
from artifacts import (Rejected, artifact, check_deadline, load, pinned_json, read_bytes, reference,  # noqa: E402
                       require, source_snapshot, sources_unchanged)

TABLE_LANGUAGE = "go-native-state-provider"
ROW_FIELDS = {"name", "family", "file", "original", "replacement"}
BINARY = "pins-mutant.test"
MUTANT_CAP = 1500
MUTANT_SECONDS = 6 * 3600
HARNESS = ("pins_mutants.py", "pins.py", "artifacts.py", "native.py", "owned.py", "bsc_source_inputs.py",
           "source_inputs.py", "profile_manifest.py")


def load_table(path, adapters=ROOT / "adapters"):
    """Check the mutant table: unique names, a known family and source file per
    mutant, and a fragment that occurs exactly once in that source. `adapters`
    is the adapters directory of the sources; tests pass a copy."""
    table = json.loads(Path(path).read_text())
    require(table.get("schema_version") == 1 and table.get("language") == TABLE_LANGUAGE, "Unsupported mutant table")
    rows = table["mutants"]
    names = [row["name"] for row in rows]
    require(rows and all(isinstance(name, str) and name for name in names) and len(names) == len(set(names)),
            "Mutant names must be unique")
    for row in rows:
        require(ROW_FIELDS <= set(row) <= ROW_FIELDS | {"equivalent"}, f"Mutant {row['name']} has an unsupported shape")
        require(row["family"] in pins.FAMILIES and row["file"] in pins.SOURCES, f"Mutant {row['name']} has no family or file")
        require(row["original"] != row["replacement"], f"Mutant {row['name']} changes nothing")
        source = Path(adapters) / row["family"] / "state_provider" / row["file"]
        require(source.read_text().count(row["original"]) == 1, f"Mutant {row['name']} original must occur exactly once")
        if "equivalent" in row:
            require(isinstance(row["equivalent"], str) and row["equivalent"].strip(), f"Mutant {row['name']} needs a reason")
    return table


def select(table, only):
    """The mutants to run: all, or the named ones; an unknown name is refused."""
    known = {row["name"] for row in table["mutants"]}
    require(not only or set(only) <= known, f"Unknown mutant names: {sorted(set(only) - known)}")
    return [row for row in table["mutants"] if not only or row["name"] in only]


def compile_tests(runner, compiler, mounts):
    """Compile the state-provider tests of the tree; a failure raises Rejected."""
    runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/" + BINARY,
                               pins.PACKAGE], mounts, cap=MUTANT_CAP, memory="6g", cwd="/source", environment=pins.environment())


def run_once(family, entry, fixture, root, runner, deadline):
    """Run the compiled tests of the tree once on the fixture; return their
    standard output. A failed command or a failed test raises Rejected."""
    for name in ("work", "temp"):
        shutil.rmtree(root / name, ignore_errors=True)
    pins.prepare_inputs(family, fixture, root, deadline)
    stdout = pins.run_tests(entry, entry["native_binary"], fixture, root, root / "generated" / BINARY, runner, deadline)
    pins.parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), pins.COMMON_TESTS + pins.FAMILY_TESTS[family])
    return stdout


def mutants(tool_path, tool_sha, table_path, manifests, output, only=()):
    started = time.monotonic()
    deadline = started + MUTANT_SECONDS
    require(tool_path.is_absolute() and table_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "pins-native-tool" and set(tool["families"]) == set(pins.FAMILIES), "Not a pins tool")
    fixtures = {}
    for path, sha in manifests:
        family, fixture = pins.load_fixture(path, sha, deadline)
        require(family not in fixtures, f"Two ProfileManifests for {family}")
        require(fixture["build_manifest"] == tool["build_manifest"], "Fixture and tool use different builds")
        fixtures[family] = fixture
    require(set(fixtures) == set(pins.FAMILIES), "One ProfileManifest per family is required")
    table = load_table(table_path)
    selected = select(table, only)
    require(selected, "No mutant selected")
    artifact(tool["build_manifest"], deadline=deadline)
    manifest_path = Path(tool["build_manifest"]["path"])
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    proof = {"schema_version": 1, "kind": "pins-mutants", "result": "FAIL", "tool": tool_ref,
             "table": reference(table_path, deadline=deadline), "source_commit": pins.source_commit(),
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "deadline_seconds": MUTANT_SECONDS,
             "public_profile_credit": False, "baselines": {}, "mutants": [], "counts": {}, "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        trees = {}
        for family in pins.FAMILIES:
            if not any(row["family"] == family for row in selected):
                continue
            entry = tool["families"][family]
            root = output / family
            source, _, _, mounts = pins.extract_family(family, manifest, manifest_path, root, deadline)
            run = {"compiler": entry["compiler"], "native_binary": entry["native_binary"]}
            compile_tests(runner, entry["compiler"], mounts)
            run_once(family, run, fixtures[family], root, runner, deadline)
            proof["baselines"][family] = "PASS"
            trees[family] = (source, mounts, run, root)
        for index, row in enumerate(selected):
            check_deadline(deadline)
            source, mounts, run, root = trees[row["family"]]
            target = source / "extractor/state_provider" / row["file"]
            original = target.read_text()
            directory = output / "mutants" / f"{index:03d}"
            directory.mkdir(parents=True)
            result = {"index": index, "name": row["name"], "family": row["family"], "file": row["file"],
                      "equivalent": row.get("equivalent"), "outcome": "INCOMPLETE"}
            owned = pins.Native(directory, deadline)
            try:
                target.write_text(original.replace(row["original"], row["replacement"], 1))
                try:
                    compile_tests(owned, run["compiler"], mounts)
                except Rejected as error:
                    result.update(outcome="BUILD_FAILED", evidence=str(error)[:300])
                else:
                    try:
                        run_once(row["family"], run, fixtures[row["family"]], root, owned, deadline)
                    except Rejected as error:
                        # A kill names the failing Go tests; a failed command
                        # that names none is no evidence.
                        failed, last = [], stdout_of(owned)
                        if last.exists():
                            failed = probe14.failing_tests(read_bytes(last, 8 * 1024 * 1024, deadline).decode())
                        outcome, evidence = probe14.classify(failed, str(error)[:300])
                        result.update(outcome=outcome, killed_by=evidence)
                    else:
                        result["outcome"] = "SURVIVED"
            finally:
                target.write_text(original)
                result["commands"] = owned.commands
                result["cleanup_complete"] = owned.cleanup_complete
                runner.commands.extend(owned.commands)
                runner.cleanup_complete = runner.cleanup_complete and owned.cleanup_complete
                proof["mutants"].append(result)
        sources_unchanged(origins, deadline)
        bad, proof["counts"] = probe14.tally(proof["mutants"])
        require(not bad, f"Mutants not killed: {bad}")

    return pins.owned_attempt(output, started, deadline, proof, "mutants.json", body)


def stdout_of(runner):
    """The standard output of the last recorded command of a runner, or a path that does not exist."""
    if not runner.commands:
        return Path(runner.output / "no-command.stdout")
    return Path(runner.commands[-1]["stdout"]["path"])


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    run_parser = commands.add_parser("mutants")
    run_parser.add_argument("tool", type=Path)
    run_parser.add_argument("--sha256", required=True)
    run_parser.add_argument("--table", type=Path, default=HERE / "pins.mutants.json")
    run_parser.add_argument("--profile-manifest", nargs=2, action="append", metavar=("PATH", "SHA256"), required=True)
    run_parser.add_argument("--output", type=Path, required=True)
    run_parser.add_argument("--only", action="append", default=[])
    args = parser.parse_args()

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    try:
        proof = mutants(args.tool, args.sha256, args.table, [(Path(path), sha) for path, sha in args.profile_manifest],
                        args.output, set(args.only))
    except (Rejected, *pins.FAILURES) as error:
        # Refused before this command created its output: no record exists.
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({"result": proof["result"], "error": proof["error"], "counts": proof.get("counts")}))
    return 0 if proof["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
