#!/usr/bin/env python3
"""Run single-check mutants of Python or Go code from a mutant table.

Usage: mutate.py TABLE --output RUN_DIR [--only NAME ...] [--jobs N]

TABLE is a JSON file kept next to the code it targets. Each mutant replaces
exactly one source fragment. For every mutant the tool copies the table's root
into a fresh temporary directory, applies the mutant, and first proves that the
mutant compiles (`go vet` for Go; `py_compile` plus an import for Python). A
mutant that does not compile is BUILD_FAILED and fails the tool: a build
failure is never a kill. The tool then runs the table's test command. A mutant
is KILLED only when the tests fail and the output names at least one failing
test or control; that evidence (every `*_test.go:` line, every failing test
name and every single-line assertion or error line) is kept in the report, and
the full output is kept in the run's `logs` directory. A survivor passes only
when the table labeled it equivalent, with a reason, before the run.

Before the mutants, the unmutated copy must compile and pass its tests.
RUN_DIR must not exist; the tool creates it and writes `mutants.json` and one
log per mutant. Exit 0 means every mutant applied, compiled and was killed or
was a pre-labeled equivalent survivor; exit 1 means any other outcome; exit 2
means a usage or table error.

Table fields:
  schema_version  1
  language        "go" or "python"
  root            directory to copy, relative to the table (default ".")
  copy            glob patterns relative to root (default: every file)
  test            test command argv run in the copy root; "{python}" is the
                  current interpreter
  go_module       for Go: module name for `go mod init` when the copy has no go.mod
  import          for Python: module names to import after compiling
                  (default: the mutated file's module name)
  timeout_seconds per command (default 300)

A command that exceeds its timeout is stopped with its whole process tree,
including children that started their own session; a mutant whose tree cannot
be removed is CLEANUP_FAILED and fails the tool. A process that detaches and
is orphaned before its command ends normally is outside this tracking.
  mutants         list of {name, file, original, replacement, equivalent?};
                  an empty replacement deletes the fragment
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import contextlib
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

KILL_LINE = re.compile(
    r"^\s*(?:--- FAIL: .*"                      # Go failing test or subtest
    r"|[\w./-]+_test\.go:\d+: .*"               # Go test assertion line
    r"|panic: .*"                               # Go panic
    r"|FAIL\s+\S+.*"                            # Go package failure
    r"|(?:FAIL|ERROR): \S+ \(.*\)"              # Python unittest failure
    r"|File \"[^\"]*test_[^\"]*\.py\", line \d+.*"  # Python test frame
    r"|\w*(?:Error|Exception|Failure)(?::.*)?"  # Python assertion or error line
    r")$")
GO_FAILED_TEST = re.compile(r"^\s*--- FAIL: (\S+)")
PY_FAILED_TEST = re.compile(r"^(?:FAIL|ERROR): (\S+) \(([^)]*)\)")
REQUIRED = {"schema_version", "language", "test", "mutants"}
OPTIONAL = {"root", "copy", "go_module", "import", "timeout_seconds", "description"}
MUTANT_FIELDS = {"name", "file", "original", "replacement"}
FAILING = ("BUILD_FAILED", "NOT_APPLIED", "SURVIVED", "UNNAMED_FAILURE", "CLEANUP_FAILED")
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


class TableError(ValueError):
    """The mutant table or the command line is invalid."""


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def load_table(path):
    path = Path(path).resolve()
    try:
        table = json.loads(path.read_text())
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise TableError(f"cannot read table {path}: {error}") from error
    if type(table) is not dict:
        raise TableError("table is not an object")
    missing, unknown = REQUIRED - set(table), set(table) - REQUIRED - OPTIONAL
    if missing or unknown:
        raise TableError(f"table fields missing {sorted(missing)} or unknown {sorted(unknown)}")
    if table["schema_version"] != 1 or table["language"] not in ("go", "python"):
        raise TableError("table needs schema_version 1 and language go or python")
    if type(table["test"]) is not list or not table["test"] or not all(type(item) is str for item in table["test"]):
        raise TableError("test must be a nonempty list of strings")
    timeout = table.get("timeout_seconds", 300)
    if type(timeout) not in (int, float) or not 0 < timeout <= 3600:
        raise TableError("timeout_seconds must be in (0, 3600]")
    if type(table["mutants"]) is not list or not table["mutants"]:
        raise TableError("mutants must be a nonempty list")
    if "import" in table and (type(table["import"]) is not list or not table["import"]
                              or not all(type(item) is str and item for item in table["import"])):
        raise TableError("import must be a nonempty list of module names")
    root = (path.parent / table.get("root", ".")).resolve()
    if not root.is_dir():
        raise TableError(f"root is not a directory: {root}")
    names = set()
    for mutant in table["mutants"]:
        if type(mutant) is not dict or not MUTANT_FIELDS <= set(mutant) or set(mutant) - MUTANT_FIELDS - {"equivalent"}:
            raise TableError(f"mutant needs exactly {sorted(MUTANT_FIELDS)} and optional equivalent: {mutant}")
        if not all(type(mutant[key]) is str for key in MUTANT_FIELDS) or not all(
                mutant[key] for key in ("name", "file", "original")):
            raise TableError(f"mutant fields must be strings, nonempty except a deleting replacement: {mutant.get('name')}")
        if mutant["name"] in names:
            raise TableError(f"duplicate mutant name: {mutant['name']}")
        if mutant["original"] == mutant["replacement"]:
            raise TableError(f"mutant changes nothing: {mutant['name']}")
        if "equivalent" in mutant and (type(mutant["equivalent"]) is not str or not mutant["equivalent"].strip()):
            raise TableError(f"equivalent label needs a reason: {mutant['name']}")
        target = (root / mutant["file"]).resolve()
        if not target.is_relative_to(root) or not target.is_file():
            raise TableError(f"mutant file is not inside root: {mutant['file']}")
        names.add(mutant["name"])
    return path, root, table


def copied_files(root, patterns):
    files = []
    for path in sorted(root.rglob("*")):
        relative = path.relative_to(root)
        if any(part in (".git", "__pycache__") for part in relative.parts) or not path.is_file():
            continue
        if patterns is None or any(fnmatch.fnmatch(str(relative), pattern) for pattern in patterns):
            files.append(relative)
    return files


def process_tree(root_pid):
    """Return (pid, pgid) for every live descendant of root_pid."""
    rows = subprocess.run(["ps", "-axo", "pid=,ppid=,pgid=,stat="], capture_output=True, text=True, timeout=10)
    children = {}
    for line in rows.stdout.splitlines():
        values = line.split()
        if len(values) == 4 and all(value.isdigit() for value in values[:3]) and not values[3].startswith("Z"):
            pid, parent, group = map(int, values[:3])
            children.setdefault(parent, []).append((pid, group))
    found, queue = [], [root_pid]
    while queue:
        for pid, group in children.get(queue.pop(), []):
            found.append((pid, group))
            queue.append(pid)
    return found


def exited_unreaped(process):
    """Report whether the child exited without reaping it."""
    event = os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
    return event is not None and event.si_pid == process.pid and event.si_code in (
        os.CLD_EXITED, os.CLD_KILLED, os.CLD_DUMPED)


def alive(pid):
    row = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], capture_output=True, text=True, timeout=10)
    return row.returncode == 0 and bool(row.stdout.strip()) and not row.stdout.strip().startswith("Z")


def stop_tree(process):
    """Stop, then kill, the command's group and every descendant; return survivors."""
    with contextlib.suppress(ProcessLookupError, PermissionError):
        os.killpg(process.pid, signal.SIGSTOP)
    tree = {}
    for _ in range(2):
        for pid, group in process_tree(process.pid):
            tree[pid] = group
            with contextlib.suppress(ProcessLookupError, PermissionError):
                os.kill(pid, signal.SIGSTOP)
    groups = {process.pid} | {group for group in tree.values() if group in tree}
    for group in groups:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.killpg(group, signal.SIGKILL)
    for pid in tree:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.kill(pid, signal.SIGKILL)
    process.wait()
    deadline = time.monotonic() + 5
    remaining = sorted(tree)
    while remaining and time.monotonic() < deadline:
        remaining = [pid for pid in remaining if alive(pid)]
        if remaining:
            time.sleep(0.05)
    return remaining


def run(argv, cwd, environment, timeout):
    """Run one command in its own process group with output in a file, never a pipe."""
    started = time.monotonic()
    timed_out, survivors = False, []
    with tempfile.TemporaryFile() as sink:
        process = subprocess.Popen(argv, cwd=cwd, env=environment, stdin=subprocess.DEVNULL, stdout=sink,
                                   stderr=subprocess.STDOUT, start_new_session=True)
        try:
            deadline = time.monotonic() + timeout
            while not exited_unreaped(process):
                if time.monotonic() >= deadline:
                    timed_out = True
                    break
                time.sleep(0.02)
            if not timed_out:
                # The unreaped leader still reserves its group ID, so this
                # reaches only same-group leftovers of a command that ended.
                with contextlib.suppress(ProcessLookupError, PermissionError):
                    os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        finally:
            if process.returncode is None:
                survivors = stop_tree(process)
        sink.seek(0)
        output = ANSI.sub("", sink.read().decode(errors="replace"))
    return {"exit_code": None if timed_out else process.returncode, "output": output,
            "seconds": round(time.monotonic() - started, 3), "timed_out": timed_out, "survivors": survivors}


class Session:
    """Hold one table run: inputs, environment and the per-mutant procedure."""

    def __init__(self, table_path, root, table):
        self.table_path, self.root, self.table = table_path, root, table
        self.timeout = table.get("timeout_seconds", 300)
        self.files = copied_files(root, table.get("copy"))
        environment = {key: value for key, value in os.environ.items()
                       if key not in ("PYTHONPATH", "PYTHONHOME", "FORCE_COLOR")}
        environment["PYTHONDONTWRITEBYTECODE"] = "1"
        # Colored unittest or Go output would hide the failing test names.
        environment["NO_COLOR"] = "1"
        environment["PYTHON_COLORS"] = "0"
        self.environment = environment

    def prepare(self, work, mutant=None):
        for relative in self.files:
            target = work / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(self.root / relative, target)
        if mutant is not None:
            target = work / mutant["file"]
            text = target.read_text()
            count = text.count(mutant["original"])
            if count != 1:
                return {"status": "NOT_APPLIED", "matches": count}
            target.write_text(text.replace(mutant["original"], mutant["replacement"]))
        return None

    def compile(self, work, mutated_file):
        if self.table["language"] == "go":
            if "go_module" in self.table and not (work / "go.mod").exists():
                initialized = run(["go", "mod", "init", self.table["go_module"]], work, self.environment, self.timeout)
                if initialized["exit_code"] != 0:
                    return initialized
            return run(["go", "vet", "./..."], work, self.environment, self.timeout)
        target = work / mutated_file
        modules = self.table.get("import") or [target.stem]
        code = ("import importlib, py_compile, sys\n"
                "py_compile.compile(sys.argv[1], cfile=sys.argv[2], doraise=True)\n"
                "sys.path.insert(0, sys.argv[3])\n"
                "for name in sys.argv[4:]:\n"
                "    importlib.import_module(name)\n")
        with tempfile.TemporaryDirectory() as scratch:
            return run([sys.executable, "-c", code, str(target), str(Path(scratch) / "compiled.pyc"),
                        str(target.parent), *modules], target.parent, self.environment, self.timeout)

    def test(self, work):
        argv = [sys.executable if item == "{python}" else item for item in self.table["test"]]
        return run(argv, work, self.environment, self.timeout)

    def baseline(self):
        mutated = self.table["mutants"][0]["file"]
        with tempfile.TemporaryDirectory(prefix="mutate-baseline-") as temporary:
            work = Path(temporary)
            self.prepare(work)
            build = self.compile(work, mutated)
            if build["exit_code"] != 0:
                return {"status": "BASELINE_BUILD_FAILED", "evidence": evidence_lines(build["output"])}
            tested = self.test(work)
            if tested["survivors"]:
                return {"status": "BASELINE_CLEANUP_FAILED", "survivors": tested["survivors"]}
            if tested["exit_code"] != 0:
                return {"status": "BASELINE_FAILED", "evidence": evidence_lines(tested["output"])}
            return {"status": "PASS", "seconds": tested["seconds"], "summary": tail(tested["output"])}

    def mutate(self, index, mutant, logs):
        row = {"index": index, "name": mutant["name"], "file": mutant["file"]}
        if "equivalent" in mutant:
            row["equivalent"] = mutant["equivalent"]
        with tempfile.TemporaryDirectory(prefix="mutate-") as temporary:
            work = Path(temporary)
            problem = self.prepare(work, mutant)
            if problem is not None:
                row.update(problem)
                return row
            build = self.compile(work, mutant["file"])
            if build["exit_code"] != 0:
                (logs / f"{index:03d}.build.log").write_text(build["output"])
                row.update(status="BUILD_FAILED", evidence=evidence_lines(build["output"])
                           or tail(build["output"]).splitlines())
                return row
            tested = self.test(work)
        (logs / f"{index:03d}.test.log").write_text(tested["output"])
        row["seconds"] = tested["seconds"]
        killers = failing_tests(tested["output"])
        if tested["survivors"]:
            row.update(status="CLEANUP_FAILED", survivors=tested["survivors"])
        elif tested["timed_out"]:
            row.update(status="KILLED", killed_by=[f"timeout after {self.timeout} s"],
                       evidence=evidence_lines(tested["output"]))
        elif tested["exit_code"] == 0:
            row["status"] = "EQUIVALENT" if "equivalent" in mutant else "SURVIVED"
        elif killers:
            row.update(status="KILLED", killed_by=killers, evidence=evidence_lines(tested["output"]))
        else:
            row.update(status="UNNAMED_FAILURE", exit_code=tested["exit_code"],
                       evidence=evidence_lines(tested["output"]) or tail(tested["output"]).splitlines())
        return row


def evidence_lines(output):
    return [line.strip() for line in output.splitlines() if KILL_LINE.match(line.rstrip())]


def failing_tests(output):
    names = []
    for line in output.splitlines():
        match = GO_FAILED_TEST.match(line)
        if match:
            names.append(match.group(1))
            continue
        match = PY_FAILED_TEST.match(line)
        if match:
            method, location = match.groups()
            names.append(location if location.endswith("." + method) else f"{location}.{method}")
    return list(dict.fromkeys(names))


def tail(output, lines=5):
    return "\n".join(output.strip().splitlines()[-lines:])


def toolchain(language):
    if language == "go":
        result = subprocess.run(["go", "version"], capture_output=True, text=True)
        return result.stdout.strip()
    return f"Python {platform.python_version()} ({sys.executable})"


def git_source(root, table_path):
    """Return the Git head only when the table file is tracked in that checkout.

    A copy extracted with `git archive` inside an ignored directory of another
    checkout would otherwise report that checkout's head. Without tracked
    sources the report records null and relies on the per-file SHA256 list.
    """
    def git(*arguments):
        return subprocess.run(["git", *arguments], cwd=root, capture_output=True, text=True)

    head = git("rev-parse", "HEAD")
    if head.returncode:
        return None
    tracked = git("ls-files", "--error-unmatch", "--", str(table_path))
    if tracked.returncode:
        return None
    state = git("status", "--porcelain", "--untracked-files=all", "--", ".", str(table_path))
    return {"head": head.stdout.strip(), "clean": state.returncode == 0 and not state.stdout.strip()}


def verdict(rows, baseline):
    failures = [row["name"] for row in rows if row["status"] in FAILING]
    return baseline["status"] == "PASS" and not failures


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("table", type=Path)
    parser.add_argument("--output", required=True, type=Path, help="new run directory for the report")
    parser.add_argument("--only", action="append", default=[], help="run only this mutant; may repeat")
    parser.add_argument("--jobs", type=int, default=1, help="mutants to run at once")
    arguments = parser.parse_args(argv)
    try:
        table_path, root, table = load_table(arguments.table)
        known = {mutant["name"] for mutant in table["mutants"]}
        unknown = sorted(set(arguments.only) - known)
        if unknown:
            raise TableError(f"unknown mutant names: {unknown}")
        if not 1 <= arguments.jobs <= 32:
            raise TableError("--jobs must be between 1 and 32")
        if table["language"] == "go" and shutil.which("go") is None:
            raise TableError("the Go toolchain (`go`) is not on PATH")
        session = Session(table_path, root, table)
        uncopied = sorted({mutant["file"] for mutant in table["mutants"]} - {str(path) for path in session.files})
        if uncopied:
            raise TableError(f"mutated files are not copied by the table: {uncopied}")
        output = arguments.output.resolve()
        if output.exists() or output.is_symlink():
            raise TableError(f"output already exists; the tool writes only into a new run directory: {output}")
        output.parent.mkdir(parents=True, exist_ok=True)
        output.mkdir()
    except (TableError, OSError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 2
    logs = output / "logs"
    logs.mkdir()
    selected = [(index, mutant) for index, mutant in enumerate(table["mutants"])
                if not arguments.only or mutant["name"] in arguments.only]
    started = time.monotonic()
    baseline = session.baseline()
    rows = []
    if baseline["status"] == "PASS":
        with ThreadPoolExecutor(max_workers=arguments.jobs) as pool:
            rows = list(pool.map(lambda item: session.mutate(item[0], item[1], logs), selected))
    report = {
        "schema_version": 1, "kind": "single-check-mutants", "language": table["language"],
        "table": {"path": str(table_path), "sha256": sha256(table_path)},
        "tool": {"path": str(Path(__file__).resolve()), "sha256": sha256(__file__)},
        "root": str(root), "source": git_source(root, table_path),
        "sources": [{"path": str(relative), "sha256": sha256(root / relative)} for relative in session.files],
        "toolchain": toolchain(table["language"]), "test": table["test"],
        "selected": "all" if not arguments.only else sorted(arguments.only),
        "baseline": baseline, "elapsed_seconds": round(time.monotonic() - started, 3), "mutants": rows,
        "counts": {status: sum(row["status"] == status for row in rows)
                   for status in ("KILLED", "EQUIVALENT", *FAILING)},
        "mislabeled_equivalent": [row["name"] for row in rows if row["status"] == "KILLED" and "equivalent" in row],
    }
    report["result"] = "PASS" if verdict(rows, baseline) and not report["mislabeled_equivalent"] else "FAIL"
    (output / "mutants.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": report["result"], "baseline": baseline["status"], "counts": report["counts"],
                      "mislabeled_equivalent": report["mislabeled_equivalent"],
                      "report": str(output / "mutants.json")}))
    return 0 if report["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
