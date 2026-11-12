#!/usr/bin/env python3
"""Assemble the test module of the Bor decision rules (E01-02.decision_bridge).

`python3 test/harness/bor_decision.py assemble DIR` writes the module of
`python3 test/harness/decision.py assemble DIR` (the shared source_inputs,
admission and decision packages under extractor/, module
github.com/ethereum/go-ethereum) and adds the stdlib-only Bor package
`adapters/bor/decision/native/decider` at extractor/decision/native/decider,
the path at which the preparation installs it in the Bor tree. Its mutant
table `decider.mutants.json` runs there.
"""

import argparse
import json
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))

import decision  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
DECIDER = ("decider.go", "decider_test.go", "decider.mutants.json")


def assemble(target, root=ROOT):
    """Write the module into the new directory target and return its package directories."""
    target = Path(target)
    packages = decision.assemble(target, root)
    package = "extractor/decision/native/decider"
    (target / package).mkdir(parents=True)
    for name in DECIDER:
        shutil.copyfile(root / "adapters" / "bor" / "decision" / "native" / "decider" / name, target / package / name)
    return sorted(packages + [package])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("assemble",))
    parser.add_argument("target")
    args = parser.parse_args(argv)
    try:
        output = assemble(args.target, ROOT)
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
