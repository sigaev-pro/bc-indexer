#!/usr/bin/env python3
"""Print the vectors of the E05-01.eth_finality core, check them, assemble its test module.

`vectors` prints the expected digests of the fixed vector inputs, computed
here from the formulas of the package README, independently of the Go code;
the Go tests read the same values from `vectors.json`, and a portable test
checks that they match. `assemble DIR` extends the assembled lifecycle
module (source_inputs, admission, decision, lifecycle) with the finality
package under `extractor/finality`; the Go mutant table of the package runs
only there, because the package imports admission, decision and lifecycle
at their native paths.
"""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission  # noqa: E402
import decision  # noqa: E402
import lifecycle  # noqa: E402

ROOT = admission.ROOT
PACKAGE = ROOT / "adapters" / "geth" / "finality"
PACKAGE_FILES = ("finality.go", "finality.mutants.json", "finality_test.go", "vectors.json")


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


def digest_of(name, *parts):
    h = hashlib.sha256()
    h.update(tag(name))
    for part in parts:
        h.update(part)
    return h.hexdigest()


def decision_bytes(producer, sequence):
    return b"\x01" + text(producer) + u64(sequence)


def observation_digest(field, known, block=None, origin="", basis="", reason="", decision=None):
    """The evidence digest of one typed observation record.

    The known branch carries the block, the origin and the safe basis (UNSET
    for finalized); the unknown branch carries its reason. The decision ref
    is the FINALITY decision that produced the record.
    """
    parts = [text(field)]
    if known:
        parts += [b"\x01", head(block), text(origin), text(basis)]
    else:
        parts += [b"\x00", text(reason)]
    parts.append(decision_bytes(*decision) if decision else b"\x00")
    return digest_of("extractor-finality-observation-v1", *parts)


# Fixed vector inputs.
STORE_ID = decision.STORE_ID
PRODUCER, RUN = admission.PRODUCER, admission.RUN
OWNER = f"{PRODUCER}.{RUN}"
HEAD = {"number": 7, "hash": sha(b"head-7").hex()}
BLOCK3 = {"number": 3, "hash": sha(b"block-3").hex()}
BLOCK5 = {"number": 5, "hash": sha(b"block-5").hex()}
CLEAR_EVIDENCE = sha(b"native-clear-evidence").hex()


def vectors():
    live_finalized = observation_digest("FINALIZED", True, BLOCK5, "NATIVE_LIVE", "UNSET",
                                        decision=(OWNER, 0))
    return {
        "store_id": STORE_ID, "producer": PRODUCER, "run": RUN, "head": HEAD,
        "set_finalized_caller": "geth.set-finalized", "set_safe_caller": "geth.set-safe",
        "startup_caller": "geth.startup-reconcile",
        "block_3": BLOCK3, "block_5": BLOCK5,
        "live_safe_digest": observation_digest("SAFE", True, BLOCK5, "NATIVE_LIVE", "CONSENSUS_SETTER",
                                               decision=(OWNER, 0)),
        "live_finalized_digest": live_finalized,
        "restart_safe_digest": observation_digest("SAFE", True, BLOCK5, "NATIVE_RESTART",
                                                  "STARTUP_FINALIZED_FALLBACK", decision=(OWNER, 1)),
        "restart_finalized_digest": observation_digest("FINALIZED", True, BLOCK5, "NATIVE_RESTART", "UNSET",
                                                       decision=(OWNER, 0)),
        "unset_safe_digest": observation_digest("SAFE", False, reason="NATIVE_REFERENCE_UNSET",
                                                decision=(OWNER, 0)),
        "retained_safe_digest": observation_digest("SAFE", True, BLOCK5, "RETAINED_DURABLE",
                                                   "CONSENSUS_SETTER", decision=(OWNER, 0)),
        "restart_evidence_digest": digest_of("extractor-finality-restart-v1", text("FINALIZED"), head(BLOCK5),
                                             head(HEAD)),
        "clear_evidence_digest": CLEAR_EVIDENCE,
    }


def check_vectors():
    """Return the stored vectors; raise ValueError if they differ from the formulas."""
    stored = json.loads((PACKAGE / "vectors.json").read_text())
    want = vectors()
    if stored != want:
        raise ValueError(f"vectors.json differs from the formulas: {stored} != {want}")
    return sorted(stored)


# The repository-local modules the finality package composes at its native
# paths: the recovery GC anchor owner and the protocol/semantic identity
# model. Each is bound with a resolved local replace, so an assembly under
# a symlinked temporary directory resolves the same worktree files.
LOCAL_MODULES = {
    "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc": "adapters/recovery_gc",
    "github.com/sigaev-pro/bc-indexer/protocol/semantic": "protocol/semantic",
}


def assemble(target, root=ROOT):
    """Extend the assembled lifecycle module with the finality package and return its directories."""
    target = Path(target)
    # lifecycle.assemble refuses an existing target before it writes.
    packages = lifecycle.assemble(target, root)
    (target / "extractor/finality").mkdir(parents=True)
    for name in PACKAGE_FILES:
        shutil.copyfile(root / "adapters" / "geth" / "finality" / name, target / "extractor/finality" / name)
    # The replaces of a dependency module are ignored, so the assembled
    # main module binds every module of the graph itself.
    module = ["", "// The repository-local modules the finality package composes",
              "// at its native paths: the recovery GC anchor owner and the",
              "// protocol/semantic identity model.",
              "require (",
              "\tgithub.com/sigaev-pro/bc-indexer/adapters/recovery_gc v0.0.0",
              "\tgithub.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0",
              ")",
              ""]
    for module_path, relative in sorted(LOCAL_MODULES.items()):
        local = (root / relative).resolve()
        module.append(f"replace {module_path} => {local}")
    (target / "go.mod").write_text((target / "go.mod").read_text() + "\n".join(module) + "\n")
    return sorted(packages + ["extractor/finality"])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("check", "vectors", "assemble"))
    parser.add_argument("target", nargs="?", help="new directory for assemble")
    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            output = check_vectors()
        elif args.command == "vectors":
            output = vectors()
        else:
            if not args.target:
                raise ValueError("assemble needs a target directory")
            output = assemble(args.target)
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
