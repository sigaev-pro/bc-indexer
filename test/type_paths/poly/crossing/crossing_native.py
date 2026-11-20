#!/usr/bin/env python3
"""Bounded private ordinary-type activation crossings; diagnostic only."""

import importlib.util
from pathlib import Path

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("poly_crossing_support", HERE / "crossing_support.py")
support = importlib.util.module_from_spec(spec)
spec.loader.exec_module(support)
for name in support.parent.PINS:
    support.parent.accepted(name)
for name in support.ACTIVATION_PINS:
    support.activation_source(name)
driver = support.module("poly_native")
driver.HERE = HERE
driver.FILES = ("crossing_native.py", "crossing_support.py", "probe.go", "ORACLE.md")
driver.support = support
original_keep_code = driver.keep_code


def keep_code(output, deadline):
    origins, copies = original_keep_code(output, deadline)
    for folder, names, read, label in (
        (support.parent.ACCEPTED, support.parent.PINS, support.parent.accepted, "accepted-poly"),
        (support.ACTIVATION, support.ACTIVATION_PINS, support.activation_source, "activation-helpers"),
    ):
        destination = output / label
        destination.mkdir()
        for name in names:
            read(name)
            path = folder / name
            ref = driver.reference(path, deadline=deadline)
            origins.append(ref)
            copies.append(driver.copy_artifact(path, destination / name, ref, deadline=deadline))
    return origins, copies


driver.keep_code = keep_code

if __name__ == "__main__":
    raise SystemExit(driver.main())
