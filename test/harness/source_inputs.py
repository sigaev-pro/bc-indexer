#!/usr/bin/env python3
"""Check the shared E01-02.inputs core and print its independent digest vectors.

`check` refuses unless `adapters/<family>/source_inputs` holds the same file
names with the same bytes for Geth, BSC and Bor. `vectors` prints the expected
digests of the fixed vector inputs, computed here with Python's hashlib from
the formulas in the package README, independently of the Go code; the Go unit
tests hard-code these values and a portable test checks that they match.
"""

import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

ROOT = Path(__file__).resolve().parents[2]
FAMILIES = ("geth", "bsc", "bor")
CHUNK_BYTES = 64 * 1024

# Fixed vector inputs. The Go test file uses the same values.
OBSERVATION = "obs-1"
ITEMS = (("header", b"header-bytes"), ("body", b""))
REQUESTS = (
    (b"GET /clerk/event-record/list?from-id=1&to-time=1700000000&limit=50", b'{"result":[]}'),
    (b"GET /bor/span/1", bytes(range(256)) * 300),
)


def tag(name):
    return name.encode() + b"\x00"


def u64(value):
    return struct.pack(">Q", value)


def sha(data):
    return hashlib.sha256(data).digest()


def capsule_digest(items):
    data = tag("extractor-source-inputs-capsule-v1") + u64(len(items))
    for name, content in items:
        data += u64(len(name)) + name.encode() + u64(len(content)) + sha(content)
    return sha(data)


def entry_digest(sequence, request, response):
    return sha(tag("extractor-source-inputs-entry-v1") + u64(sequence) + sha(request) + u64(len(request))
               + sha(response) + u64(len(response)))


def chain_start(capsule):
    return sha(tag("extractor-source-inputs-chain-v1") + capsule)


def chain_next(chain, entry):
    return sha(chain + entry)


def consumed_digest(capsule, count, chain):
    return sha(tag("extractor-source-inputs-consumed-v1") + capsule + u64(count) + chain)


def vectors():
    capsule = capsule_digest(ITEMS)
    chain = chain_start(capsule)
    result = {"CapsuleDigest": capsule.hex(), "ChainStart": chain.hex()}
    for sequence, (request, response) in enumerate(REQUESTS):
        entry = entry_digest(sequence, request, response)
        chain = chain_next(chain, entry)
        result[f"Entry{sequence}Digest"] = entry.hex()
        result[f"Chain{sequence + 1}"] = chain.hex()
    result["ConsumedOneDigest"] = consumed_digest(capsule, 1, bytes.fromhex(result["Chain1"])).hex()
    result["ConsumedDigest"] = consumed_digest(capsule, len(REQUESTS), chain).hex()
    result["ConsumedEmptyDigest"] = consumed_digest(capsule, 0, chain_start(capsule)).hex()
    return result


def shared_files(root, family):
    base = root / "adapters" / family / "source_inputs"
    if not base.is_dir():
        raise ValueError(f"missing shared package: {base.relative_to(root)}")
    return {path.relative_to(base).as_posix(): path.read_bytes()
            for path in sorted(base.rglob("*")) if path.is_file()}


def check_identical(root=ROOT):
    """Return the shared file digests; raise ValueError if the copies differ."""
    copies = {family: shared_files(root, family) for family in FAMILIES}
    names = {family: sorted(files) for family, files in copies.items()}
    if not names[FAMILIES[0]]:
        raise ValueError("the shared package has no files")
    for family in FAMILIES[1:]:
        if names[family] != names[FAMILIES[0]]:
            raise ValueError(f"source_inputs file names differ: {names}")
    digests = {}
    for name in names[FAMILIES[0]]:
        values = {family: hashlib.sha256(copies[family][name]).hexdigest() for family in FAMILIES}
        if len(set(values.values())) != 1:
            raise ValueError(f"shared file {name} differs between families: {values}")
        digests[name] = values[FAMILIES[0]]
    return digests


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("check", "vectors"))
    args = parser.parse_args(argv)
    try:
        output = check_identical(ROOT) if args.command == "check" else vectors()
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
