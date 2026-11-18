#!/usr/bin/env python3
"""Validate the source-only E01-05 inventory without granting runtime credit."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
import tarfile

ROOT = Path(__file__).resolve().parents[2]
CLIENTS = {"geth": "ETH", "bsc": "BSC", "bor": "POLY"}
PROFILES = {
    "geth": {"ethereum-mainnet", "ethereum-sepolia", "ethereum-hoodi"},
    "bsc": {"bsc-mainnet", "bsc-testnet"},
    "bor": {"polygon-pos-mainnet", "polygon-pos-amoy"},
}
VARIANTS = {0: "EvmLegacy", 1: "EvmAccessList", 2: "EvmDynamicFee",
            3: "EvmBlob", 4: "EvmSetCode", 127: "BorStateSync"}
SPECIAL = {
    "geth": {"eth.beacon-roots", "eth.history-storage", "eth.withdrawal-queue", "eth.consolidation-queue"},
    "bsc": {"bsc.parlia-system"},
    "bor": {"bor.legacy-state-sync", "bor.body-state-sync"},
}
COMMON_FILES = {"core/types/transaction.go", "core/types/transaction_signing.go",
                "core/state_processor.go", "core/state_transition.go"}
EXTRA_FILES = {"geth": set(), "bsc": {"consensus/parlia/parlia.go"},
               "bor": {"consensus/bor/bor.go", "core/types/tx_state_sync.go", "core/types/state_data.go"}}
HEX256 = re.compile(r"[0-9a-f]{64}")
MAX_JSON_BYTES = 1024 * 1024
MAX_ARCHIVE_BYTES = 1024 * 1024 * 1024
MAX_SOURCE_BYTES = 4 * 1024 * 1024


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"Duplicate JSON key: {key}")
        result[key] = value
    return result


def load(path):
    with Path(path).open("rb") as stream:
        raw = stream.read(MAX_JSON_BYTES + 1)
    require(len(raw) <= MAX_JSON_BYTES, "JSON input exceeds the size limit")
    return json.loads(raw, object_pairs_hook=unique_object)


def expected_types(client):
    return {0, 1, 2, 3, 4, 127} if client == "bor" else {0, 1, 2, 3, 4}


def validate(document, client, candidates):
    """Validate a complete inventory for one explicitly selected client."""
    require(client in CLIENTS, "Unknown client")
    require(isinstance(document, dict), "Inventory must be an object")
    required = {"schema_version", "client", "family", "source_commit", "archive_sha256", "files",
                "body_types", "special_paths", "unknown_type", "runtime", "public_profiles"}
    extras = {"geth": set(), "bsc": {"classification"}, "bor": {"state_sync_payload"}}[client]
    require(set(document) == required | extras, "Inventory fields differ from the source-only schema")
    require(type(document["schema_version"]) is int and document["schema_version"] == 1,
            "Unknown inventory version")
    require(document["client"] == client and document["family"] == CLIENTS[client], "Client/family mismatch")
    require(document["source_commit"] == candidates["components"][client]["commit"], "Source commit mismatch")
    require(isinstance(document["archive_sha256"], str) and HEX256.fullmatch(document["archive_sha256"]),
            "Invalid archive digest")
    files = document["files"]
    require(isinstance(files, dict) and set(files) == COMMON_FILES | EXTRA_FILES[client], "Source file set mismatch")
    require(all(isinstance(value, str) and HEX256.fullmatch(value) for value in files.values()),
            "Invalid source file digest")
    require(document["runtime"] == "NOT_RUN", "Source inventory cannot grant runtime credit")
    profiles = document["public_profiles"]
    require(isinstance(profiles, dict) and set(profiles) == PROFILES[client], "Public profile set mismatch")
    require(all(value == "NOT_RUN" for value in profiles.values()), "Source inventory cannot grant public credit")
    rows = document["body_types"]
    require(isinstance(rows, list), "Body type rows must be a list")
    seen = set()
    for row in rows:
        require(isinstance(row, dict) and set(row) == {"native_type", "variant", "activation", "path"},
                "Malformed body type row")
        number = row["native_type"]
        require(type(number) is int and number in expected_types(client), "Unknown native type")
        require(number not in seen, "Duplicate native type")
        seen.add(number)
        require(row["variant"] == VARIANTS[number], "Native type/variant mismatch")
        require(all(isinstance(row[key], str) and row[key].strip() for key in ("activation", "path")),
                "Each native type needs activation and path evidence")
    require(seen == expected_types(client), "Missing native type")
    special = document["special_paths"]
    require(isinstance(special, list), "Special paths must be a list")
    seen = set()
    for row in special:
        require(isinstance(row, dict) and set(row) == {"id", "origin", "native_type", "path", "owner"},
                "Malformed special path")
        name = row["id"]
        require(isinstance(name, str) and name in SPECIAL[client] and name not in seen, "Unknown/duplicate special path")
        seen.add(name)
        if name == "bor.body-state-sync":
            require(type(row["native_type"]) is int and row["native_type"] == 127 and
                    row["origin"] == "BODY_TRANSACTION", "StateSync body type/origin mismatch")
        else:
            origin = "BODY_CLASSIFICATION" if client == "bsc" else (
                "BOR_LEGACY_SYNTHETIC_RECEIPT" if client == "bor" else "OUTSIDE_TRANSACTION")
            require(row["native_type"] is None and row["origin"] == origin,
                    "Classification or outside-body path must not invent a native type")
        owner = "E01-05.type_paths" if name in {"bsc.parlia-system", "bor.body-state-sync"} else "E04-02.optional_scope"
        require(row["owner"] == owner, "Special path owner mismatch")
        require(isinstance(row["path"], str) and row["path"].strip(), "Missing special execution path")
    require(seen == SPECIAL[client], "Missing special path")
    require(isinstance(document["unknown_type"], str) and "ErrTxTypeNotSupported" in document["unknown_type"],
            "Missing native unknown-type rejection")
    for field in extras:
        require(isinstance(document[field], str) and document[field].strip(), "Missing system payload/classification rule")


def lookup(document, native_type):
    """Look up a source row; this does not validate a transaction or activate a fork."""
    require(type(native_type) is int, "Native type must be an integer")
    for row in document["body_types"]:
        if row["native_type"] == native_type:
            return row
    raise ValueError(f"Unknown native type: {native_type}")


def verify_source(document, archive_path):
    """Bind the reviewed source matrix to the retained original native archive."""
    with Path(archive_path).open("rb") as stream:
        size = stream.seek(0, 2)
        require(size <= MAX_ARCHIVE_BYTES, "Source archive exceeds the size limit")
        stream.seek(0)
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
        require(digest == document["archive_sha256"], "Source archive digest mismatch")
        stream.seek(0)
        sources = {}
        with tarfile.open(fileobj=stream, mode="r:") as archive:
            for index, member in enumerate(archive):
                require(index < 100000, "Too many source archive members")
                if member.name not in document["files"]:
                    continue
                require(member.name not in sources, "Duplicate source archive member")
                require(member.isfile() and member.size <= MAX_SOURCE_BYTES, "Invalid source archive member")
                data = archive.extractfile(member).read(MAX_SOURCE_BYTES + 1)
                require(len(data) == member.size, "Source member size mismatch")
                require(hashlib.sha256(data).hexdigest() == document["files"][member.name], "Source file digest mismatch")
                sources[member.name] = data.decode("utf-8")
    require(set(sources) == set(document["files"]), "Missing source archive members")
    transaction = sources["core/types/transaction.go"]
    constants = {int(value, 16) for value in re.findall(r"\b\w+TxType\s*=\s*(0x[0-9a-fA-F]+)", transaction)}
    require(constants == {row["native_type"] for row in document["body_types"]}, "Native source type set differs from matrix")
    # These pins make the boundary explicit; the full file digests, not a loose
    # text match alone, bind the reviewed control flow.
    require("return nil, ErrTxTypeNotSupported" in transaction, "Native decoder rejection is absent")
    signer = sources["core/types/transaction_signing.go"]
    require("func MakeSigner(" in signer, "Native block-specific signer is absent")
    return {"result": "SOURCE_VERIFIED", "client": document["client"], "source_commit": document["source_commit"],
            "archive_sha256": digest, "verified_files": sorted(sources), "runtime": "NOT_RUN",
            "public_profiles": document["public_profiles"]}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("check")
    source = sub.add_parser("source")
    source.add_argument("client", choices=CLIENTS)
    source.add_argument("--archive", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        candidates = load(ROOT / ".project/bootstrap/components/candidate-lock.json")
        documents = {}
        for client in CLIENTS:
            documents[client] = load(ROOT / f"adapters/{client}/type_mapping/paths.json")
            validate(documents[client], client, candidates)
        result = ({"result": "INVENTORY_VALID", "runtime": "NOT_RUN", "clients": sorted(documents)}
                  if args.command == "check" else verify_source(documents[args.client], args.archive))
    except (ValueError, KeyError, TypeError, OSError, tarfile.TarError) as error:
        print(json.dumps({"result": "FAIL", "error": str(error)}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
