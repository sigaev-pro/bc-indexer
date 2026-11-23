#!/usr/bin/env python3
"""Check the BSC finality package of E05-01.bsc_finality, print its vectors, assemble its test module.

`vectors` computes the expected digests of the fixed vector inputs from the
formulas in the package README, independently of the Go code; the Go tests
read the same values from `vectors.json`, and the portable test
`test_bsc_finality.py` checks that they match. `assemble DIR` extends the
lifecycle binding module of `lifecycle.py assemble` with the BSC finality
package (`adapters/bsc/finality`, installed at `extractor/bsc/finality`) and
the BSC reorg tests of E05-02.reorg (`test/native_lifecycle/bsc`), and wires
the repository-local `protocol/semantic` module in with a replace, so `go
test ./extractor/bsc/finality/` runs the whole slot on an in-memory store.
"""

import argparse
import json
import os
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission  # noqa: E402
import lifecycle as shared_lifecycle  # noqa: E402

ROOT = admission.ROOT
PACKAGE = ROOT / "adapters/bsc/finality"
REORG = ROOT / "test/native_lifecycle/bsc"
SEMANTIC = "github.com/sigaev-pro/bc-indexer/protocol/semantic"
PACKAGE_FILES = ("finality.go", "observatory.go", "finality_test.go", "vectors.json")
REORG_FILES = ("reorg_test.go",)

HEADER_TAG = b"extractor-bsc-finality-header-v1\x00"
EARLY_TAG = b"extractor-bsc-finality-early-v1\x00"
ANCHOR_TAG = b"extractor-bsc-finality-anchor-v1\x00"


def sha(data):
    return admission.sha(data)


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


def block_hash(seed):
    """The test block hash of seed: the same formula as the Go helper."""
    return (seed.encode().hex() + "0" * 64)[:63] + "1"


def attested_digest(snapshot, justified, finalized):
    return sha(HEADER_TAG + text("ATTESTED") + head(snapshot) + head(justified) + head(finalized)).hex()


def fallback_digest(snapshot, reason, genesis):
    return sha(HEADER_TAG + text("GENESIS_FALLBACK") + head(snapshot) + text(reason) + head(genesis)
               + head(genesis)).hex()


def early_digest(origin, evaluation, source, target, candidate, validators, count, quorum, matching, votes):
    return sha(EARLY_TAG + text(origin) + head(evaluation) + head(source) + head(target) + head(candidate)
               + head(validators) + u64(count) + u64(quorum) + u64(matching) + sha(votes.encode())).hex()


def anchor_digest(store_id, producer, anchor, header_digest, sequence):
    return sha(ANCHOR_TAG + text(store_id) + text(producer) + head(anchor) + raw(header_digest)
               + u64(sequence)).hex()


def quorum_of(count):
    """The BSC vote quorum of count validators: ceil(2*count/3), at least
    two, the same bound the Go validation requires."""
    quorum = (2 * count + 2) // 3
    return max(quorum, 2)


def vectors():
    """Return the expected values of the fixed vector inputs."""
    genesis = {"number": 0, "hash": block_hash("genesis")}
    snapshot = {"number": 9, "hash": block_hash("execution-9")}
    justified = {"number": 8, "hash": block_hash("just-8")}
    finalized = {"number": 6, "hash": block_hash("fin-6")}
    fallback_head = {"number": 3, "hash": block_hash("snap-3")}
    attested = attested_digest(snapshot, justified, finalized)
    fallback = fallback_digest(fallback_head, "NO_ATTESTATION", genesis)
    votes = "early-votes-vector-bytes"
    early = {
        "origin": "NATIVE_LIVE",
        "evaluation": {"number": 9, "hash": block_hash("eval-9")},
        "source": justified,
        "target": {"number": 9, "hash": block_hash("target-9")},
        "candidate": finalized,
        "validators": {"number": 8, "hash": block_hash("val-8")},
        "count": 21,
        "quorum": 14,
        "matching": 15,
        "votes": votes,
    }
    early_value = early_digest(early["origin"], early["evaluation"], early["source"], early["target"],
                               early["candidate"], early["validators"], early["count"], early["quorum"],
                               early["matching"], votes)
    # The anchor record of the Go vector test: an attested snapshot at the
    # execution head 9 of the canonical chain, source A5, sequence 7.
    anchor = {"number": 5, "hash": block_hash("A5")}
    anchor_header = attested_digest(snapshot, {"number": 6, "hash": block_hash("A6")}, anchor)
    return {
        "store_id": "native-store-1",
        "producer": "producer-1",
        "run": "run-1",
        "genesis": genesis,
        "snapshot_head": snapshot,
        "justified": justified,
        "finalized": finalized,
        "fallback_head": fallback_head,
        "attested_digest": attested,
        "fallback_digest": fallback,
        "early": early,
        "early_digest": early_value,
        "anchor_digest": anchor_digest("native-store-1", "producer-1.run-1", anchor, anchor_header, 7),
        "sequence": 7,
    }


def check(root=ROOT):
    """Raise ValueError unless vectors.json matches the python reference."""
    stored = json.loads((PACKAGE / "vectors.json").read_text())
    expected = vectors()
    if stored != expected:
        raise ValueError("vectors.json does not match the python reference")
    return stored


def assemble(target, root=ROOT):
    """Extend the lifecycle binding module with this package and the BSC
    reorg tests; return the package directories."""
    target, root = Path(target), Path(root)
    packages = shared_lifecycle.assemble(target, root)
    (target / "extractor/bsc/finality").mkdir(parents=True)
    for name in PACKAGE_FILES:
        shutil.copyfile(PACKAGE / name, target / "extractor/bsc/finality" / name)
    for name in REORG_FILES:
        shutil.copyfile(REORG / name, target / "extractor/bsc/finality" / name)
    # The reorg tests build their semantic descriptors on the repository
    # local protocol/semantic module; the assembled module reaches it through
    # a replace, like test/lifecycle_validator.
    relative = os.path.relpath(root / "protocol/semantic", target).replace(os.sep, "/")
    go_mod = target / "go.mod"
    go_mod.write_text(go_mod.read_text() + f"\nrequire {SEMANTIC} v0.0.0\n"
                      f"\nreplace {SEMANTIC} => {relative}\n")
    return sorted(packages + ["extractor/bsc/finality"])


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("vectors", "check", "assemble"))
    parser.add_argument("target", nargs="?", help="new directory for assemble")
    args = parser.parse_args(argv)
    if args.command == "vectors":
        print(json.dumps(vectors(), indent=2))
        return 0
    if args.command == "check":
        check()
        print("vectors.json matches the python reference")
        return 0
    if not args.target:
        print("assemble needs a target directory", file=sys.stderr)
        return 1
    print("\n".join(assemble(args.target)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
