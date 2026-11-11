#!/usr/bin/env python3
"""Check the shared E01-02.admission core, print its vectors, assemble the binding module.

`check` refuses unless `adapters/<family>/admission` holds the same file names
with the same bytes for Geth, BSC and Bor; the `native/` directory of each
family is not compared. `vectors` prints the expected digests and canonical
JSON outputs of the fixed vector inputs, computed here from the formulas in
the package README, independently of the Go code; the Go tests read the same
values from `vectors.json`, and a portable test checks that they match.
`assemble DIR` lays out source_inputs, admission and the binding glue under
`extractor/` in a new Go module named like the native trees.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import struct
import sys

ROOT = Path(__file__).resolve().parents[2]
FAMILIES = ("geth", "bsc", "bor")
FAMILY_DIR = "native"
MODULE = "github.com/ethereum/go-ethereum"
GO_VERSION = "1.25"
MAX_DEPTH = 64


class CanonicalError(ValueError):
    """JSON outside the canonical rule."""


def tag(name):
    return name.encode() + b"\x00"


def u64(value):
    return struct.pack(">Q", value)


def text(value):
    return u64(len(value.encode())) + value.encode()


def sha(data):
    return hashlib.sha256(data).digest()


def raw(digest):
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise ValueError(f"bad digest {digest!r}")
    return bytes.fromhex(digest)


def observation_key(producer, run, sequence):
    return f"{producer}.{run}.{sequence:016x}"


def admitted_digest(key, sequence, candidate, pin_digest, capsule_digest):
    return sha(tag("extractor-admission-admitted-v1") + text(key) + u64(sequence) + u64(candidate["number"])
               + raw(candidate["hash"]) + raw(candidate["parent_hash"]) + raw(candidate["parent_root"])
               + raw(pin_digest) + raw(capsule_digest)).hex()


def consumed_digest(capsule_digest, count, chain):
    return sha(tag("extractor-source-inputs-consumed-v1") + raw(capsule_digest) + u64(count) + raw(chain)).hex()


def exec_ready_digest(admitted, consumed, execution):
    return sha(tag("extractor-admission-exec-ready-v1") + raw(admitted) + raw(consumed) + raw(execution)).hex()


class Integer(str):
    """An integer literal kept as written."""


def _integer(literal):
    if literal == "-0":
        raise CanonicalError("negative zero")
    return Integer(literal)


def _reject_float(literal):
    raise CanonicalError(f"number {literal} is not an integer")


def _reject_constant(name):
    raise CanonicalError(f"constant {name}")


def _no_surrogate(value):
    if re.search("[\ud800-\udfff]", value):
        raise CanonicalError("lone surrogate escape")
    return value


def _pairs(pairs):
    out = {}
    for key, value in pairs:
        _no_surrogate(key)
        if key in out:
            raise CanonicalError(f"duplicate key {key!r}")
        out[key] = value
    return out


def _depth(value):
    if isinstance(value, dict):
        return 1 + max((_depth(v) for v in value.values()), default=0)
    if isinstance(value, list):
        return 1 + max((_depth(v) for v in value), default=0)
    return 0


def _string(value):
    out = []
    for char in _no_surrogate(value):
        if char in '"\\':
            out.append("\\" + char)
        elif ord(char) < 0x20:
            out.append(f"\\u{ord(char):04x}")
        else:
            out.append(char)
    return '"' + "".join(out) + '"'


def _encode(value):
    if isinstance(value, dict):
        return "{" + ",".join(_string(k) + ":" + _encode(value[k]) for k in sorted(value)) + "}"
    if isinstance(value, list):
        return "[" + ",".join(_encode(v) for v in value) + "]"
    if isinstance(value, Integer):
        return str(value)
    if isinstance(value, str):
        return _string(value)
    if value is True:
        return "true"
    if value is False:
        return "false"
    if value is None:
        return "null"
    raise CanonicalError(f"unexpected value {value!r}")


def canonical(data):
    """Return the canonical bytes of JSON data, or raise CanonicalError."""
    try:
        source = data.decode("utf-8")
        value = json.loads(source, object_pairs_hook=_pairs, parse_int=_integer, parse_float=_reject_float,
                           parse_constant=_reject_constant)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise CanonicalError(str(error)) from error
    if _depth(value) > MAX_DEPTH:
        raise CanonicalError(f"nesting deeper than {MAX_DEPTH}")
    return _encode(value).encode("utf-8")


# Fixed vector inputs. The Go tests read them, with the expected values, from vectors.json.
PRODUCER, RUN, SEQUENCE = "producer-1", "run-1", 0
CANDIDATE = {"number": 7, "hash": sha(b"block-7").hex(), "parent_hash": sha(b"block-6").hex(),
             "parent_root": sha(b"root-6").hex()}
PIN_DIGEST = sha(b"pin-record").hex()
CAPSULE_DIGEST = sha(b"capsule").hex()
CONSUMED_COUNT, CONSUMED_CHAIN = 2, sha(b"chain").hex()
EXEC_DIGEST = sha(b"record-corpus").hex()
CANONICAL_INPUTS = (
    b'{"b":1,"a":[true,false,null],"c":{"z":"x","y":-12}}',
    b' { "a" : 1 ,\t"b" : [ 1 , 2 ] }\r\n',
    b'{"terminalTotalDifficulty":58750000000000000000000,"chainId":1}',
    b'"\\u003c>&\\u2028\\u2029"',
    b'"a\\nb\\u0001\\"\\\\\\/\\b\\f\\r\\t\x7f"',
    '{"é":1,"z":2,"\\ud83d\\ude00":3,"A":4,"":5}'.encode(),
    b'[{},[],"",0,-1]',
    b"[" * MAX_DEPTH + b"]" * MAX_DEPTH,
    b"[" * (MAX_DEPTH + 1) + b"]" * (MAX_DEPTH + 1),
    b"1.0", b"1e3", b"-0", b"01", b"-", b"1.", b'{"a":1,"a":2}', b'"\\ud800"', b'"\\ud800\\u0041"',
    b'"\\udc00\\ud800"', b"\xef\xbb\xbf{}", b'"\xff"', b'"a\x01"', b"{} x", b"NaN", b"[1,]", b'{"a" 1}',
    b"", b"tru", b'"\\x"', b'"\\u12"', b'{1:2}', b'"abc', b"[1 2]",
    b'"\\u12zz"', b'"\\', b'"\\ud800\\u12"', b'{"a":"\\x"}', b'{"a":1 "b":2}',
)


def vectors():
    key = observation_key(PRODUCER, RUN, SEQUENCE)
    admitted = admitted_digest(key, SEQUENCE, CANDIDATE, PIN_DIGEST, CAPSULE_DIGEST)
    consumed = consumed_digest(CAPSULE_DIGEST, CONSUMED_COUNT, CONSUMED_CHAIN)
    cases = []
    for data in CANONICAL_INPUTS:
        try:
            want = canonical(data).hex()
        except CanonicalError:
            want = None
        cases.append({"input": data.hex(), "want": want})
    return {
        "producer": PRODUCER, "run": RUN, "sequence": SEQUENCE, "key": key, "candidate": CANDIDATE,
        "pin_digest": PIN_DIGEST, "capsule_digest": CAPSULE_DIGEST, "consumed_count": CONSUMED_COUNT,
        "consumed_chain": CONSUMED_CHAIN, "exec_digest": EXEC_DIGEST, "admitted_digest": admitted,
        "consumed_digest": consumed, "exec_ready_digest": exec_ready_digest(admitted, consumed, EXEC_DIGEST),
        "canonical": cases,
    }


def shared_files(root, family):
    base = root / "adapters" / family / "admission"
    if not base.is_dir():
        raise ValueError(f"missing shared package: {base.relative_to(root)}")
    return {path.relative_to(base).as_posix(): path.read_bytes() for path in sorted(base.rglob("*"))
            if path.is_file() and path.relative_to(base).parts[0] != FAMILY_DIR}


def check_identical(root=ROOT):
    """Return the shared file digests; raise ValueError if the copies differ."""
    copies = {family: shared_files(root, family) for family in FAMILIES}
    names = {family: sorted(files) for family, files in copies.items()}
    if not names[FAMILIES[0]]:
        raise ValueError("the shared package has no files")
    for family in FAMILIES[1:]:
        if names[family] != names[FAMILIES[0]]:
            raise ValueError(f"admission file names differ: {names}")
    digests = {}
    for name in names[FAMILIES[0]]:
        values = {family: hashlib.sha256(copies[family][name]).hexdigest() for family in FAMILIES}
        if len(set(values.values())) != 1:
            raise ValueError(f"shared file {name} differs between families: {values}")
        digests[name] = values[FAMILIES[0]]
    return digests


def assemble(target, root=ROOT):
    """Write the binding module into the new directory target and return its package directories."""
    target = Path(target)
    if target.exists():
        raise ValueError(f"target exists: {target}")
    check_identical(root)
    packages = {
        "extractor/source_inputs": (root / "adapters" / "geth" / "source_inputs", ("inputs.go", "inputs_test.go")),
        "extractor/admission": (root / "adapters" / "geth" / "admission",
                                ("admission.go", "admission_test.go", "canonical.go", "canonical_test.go",
                                 "vectors.json")),
        "extractor/admission_binding": (root / "test" / "harness" / "fixtures" / "admission_core",
                                        ("binding.go", "binding_test.go")),
    }
    for package, (source, names) in packages.items():
        (target / package).mkdir(parents=True)
        for name in names:
            shutil.copyfile(source / name, target / package / name)
    (target / "go.mod").write_text(f"module {MODULE}\n\ngo {GO_VERSION}\n")
    return sorted(packages)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("check", "vectors", "assemble"))
    parser.add_argument("target", nargs="?", help="new directory for assemble")
    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            output = check_identical(ROOT)
        elif args.command == "vectors":
            output = vectors()
        else:
            if not args.target:
                raise ValueError("assemble needs a target directory")
            output = assemble(args.target, ROOT)
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
