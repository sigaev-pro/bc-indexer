"""Run portable bootstrap checks on tracked files; this is not product acceptance."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import unittest

import check_docs

ROOT = Path(__file__).resolve().parents[2]


def command(arguments):
    result = subprocess.run(arguments, cwd=ROOT, text=True, capture_output=True)
    if result.stdout:
        print(result.stdout, end="" if result.stdout.endswith("\n") else "\n")
    if result.stderr:
        print(result.stderr, end="" if result.stderr.endswith("\n") else "\n", file=sys.stderr)
    if result.returncode:
        raise ValueError(f"check failed with exit {result.returncode}: {' '.join(arguments)}")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def source_checks(paths):
    counts = {"python_files": 0, "json_files": 0}
    for path in paths:
        if path.suffix == ".py":
            compile(path.read_bytes(), str(path.relative_to(ROOT)), "exec")
            counts["python_files"] += 1
        elif path.suffix == ".json":
            json.loads(path.read_text(), object_pairs_hook=unique_object)
            counts["json_files"] += 1
    return counts


def run_tests(paths):
    tests = [path for path in paths if path.name.startswith("test_") and path.suffix == ".py"
             and path.is_relative_to(ROOT / ".project")]
    suite = unittest.TestSuite()
    for path in tests:
        module_name = "bootstrap_test_" + hashlib.sha256(str(path.relative_to(ROOT)).encode()).hexdigest()[:16]
        specification = importlib.util.spec_from_file_location(module_name, path)
        module = importlib.util.module_from_spec(specification)
        sys.path.insert(0, str(path.parent))
        sys.modules[module_name] = module
        try:
            specification.loader.exec_module(module)
            cases = unittest.defaultTestLoader.loadTestsFromModule(module)
            if not cases.countTestCases():
                raise ValueError(f"tracked test module has no unittest cases: {path.relative_to(ROOT)}")
            suite.addTests(cases)
        finally:
            sys.path.pop(0)
    if not suite.countTestCases():
        raise ValueError("no tracked bootstrap tests were discovered")
    count = suite.countTestCases()
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    if not result.wasSuccessful():
        raise ValueError("bootstrap tests failed")
    return {"test_files": len(tests), "tests": count}


def main():
    report = {"scope": "bootstrap", "runtime": "NOT_RUN", "status": "FAIL", "checks": {}}
    try:
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True,
                              text=True, check=True).stdout.strip()
        report["head_sha"] = head
        changed = subprocess.run(["git", "status", "--porcelain=v1", "--untracked-files=no"],
                                 cwd=ROOT, capture_output=True, text=True, check=True).stdout
        report["source_state"] = "modified" if changed else "clean"
        expected = os.environ.get("BOOTSTRAP_HEAD_SHA")
        if expected and expected != head:
            raise ValueError("checkout does not match the requested candidate SHA")
        if expected and changed:
            raise ValueError("tracked files differ from the requested candidate commit")
        base = os.environ.get("BOOTSTRAP_BASE_SHA")
        if base and base != "0" * 40:
            if not re.fullmatch(r"[0-9a-f]{40}", base):
                raise ValueError("invalid comparison base SHA")
            command(["git", "diff", "--check", base, head])
        command(["git", "diff", "--check"])
        command(["git", "diff", "--cached", "--check"])
        paths = check_docs.tracked_paths(ROOT)
        report["checks"]["sources"] = source_checks(paths)
        documents = check_docs.validate(ROOT)
        report["checks"]["documents"] = documents
        if documents["errors"]:
            raise ValueError("document checks failed")
        command([sys.executable, ".project/agency/roles.py", "verify"])
        report["checks"]["tests"] = run_tests(paths)
        validator = ROOT / ".project/bootstrap/components/verify.py"
        if validator in paths:
            command([sys.executable, str(validator), "--self-test"])
            report["checks"]["components"] = "PASS"
        else:
            if any(path.is_relative_to(validator.parent) for path in paths):
                raise ValueError("component files are present without their required validator")
            report["checks"]["components"] = "NOT_PRESENT: BOOT-06 has not been merged"
        if expected:
            command(["git", "diff", "--exit-code", "HEAD", "--"])
        report["status"] = "PASS"
    except (OSError, ValueError, SyntaxError, subprocess.CalledProcessError) as error:
        report["error"] = str(error)
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
