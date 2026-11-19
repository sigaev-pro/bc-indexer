#!/usr/bin/env python3
"""Source-pinned activation fixture using accepted ownership and runner behavior."""

import importlib.util
from pathlib import Path

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("poly_activation_support", HERE / "activation_support.py")
support = importlib.util.module_from_spec(spec)
spec.loader.exec_module(support)
for name in support.PINS:
    support.accepted(name)
driver = support.module("poly_native")

# These fixture seams are pinned above. No native runner, lifecycle, cleanup or
# capture implementation is replaced. The accepted driver retains its guards.
driver.HERE = HERE
driver.FILES = ("activation_native.py", "activation_support.py", "activation_judge.py", "probe.go", "ORACLE.md")
driver.support = support
original_keep_code = driver.keep_code


def keep_code(output, deadline):
    origins, copies = original_keep_code(output, deadline)
    destination = output / "accepted-poly"
    destination.mkdir()
    for name in support.PINS:
        support.accepted(name)
        path = support.ACCEPTED / name
        ref = driver.reference(path, deadline=deadline)
        origins.append(ref)
        copies.append(driver.copy_artifact(path, destination / name, ref, deadline=deadline))
    return origins, copies


driver.keep_code = keep_code

if __name__ == "__main__":
    raise SystemExit(driver.main())
