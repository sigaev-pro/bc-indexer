#!/usr/bin/env python3
"""Read a closed request and print a build plan without executing it."""

from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from core import CONTRACT, closed, decode, encode, load_inputs, make_plan, need


def main():
    try:
        request = decode(sys.stdin.buffer.read())
        closed(request, "contract kind registry_path lock_path platform limits", "Build plan request")
        need(request["contract"] == CONTRACT and request["kind"] == "BuildPlan", "Unknown build plan request")
        for key in ("registry_path", "lock_path"):
            need(isinstance(request[key], str) and Path(request[key]).is_absolute(), "Input paths must be absolute")
        plan = make_plan(load_inputs(request["registry_path"], request["lock_path"]), request["platform"], request["limits"])
        sys.stdout.buffer.write(encode(plan))
        return 0
    except (ValueError, TypeError, KeyError, OSError, OverflowError) as error:
        print("Build plan rejected: " + str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
