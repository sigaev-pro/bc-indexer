"""Refresh the updater's claims independently and authenticate Debian inputs."""

import copy
import hashlib
from pathlib import Path
import re
import time
import urllib.parse
import urllib.request

import build_contract as contract
import build_materials as materials
import build_updates
import build_source_evidence
import build_source_transport


def refresh(runner, invocation, registry, active, registry_hash, lock_hash):
    report = copy.deepcopy(invocation["report"])
    directory = invocation["directory"]
    target = directory / "independent"
    target.mkdir(exist_ok=True)
    selected = invocation["validated"]["selected"]
    archive_urls = {artifact["url"] for row in selected.values() for artifact in row.get("artifacts", [])}
    needed = invocation["validated"]["observations"].used
    tokens = {}
    for row in report["observations"]:
        url = row["url"]
        if url not in needed or url in archive_urls:
            continue
        parsed = urllib.parse.urlsplit(url)
        request_headers = {}
        if parsed.hostname == "registry-1.docker.io":
            match = re.match(r"/v2/(library/[^/]+)/", parsed.path)
            contract.require(match is not None, "independent image observation is outside an official library repository")
            repository = match.group(1)
            if repository not in tokens:
                endpoint = "https://auth.docker.io/token?service=registry.docker.io&scope=" + urllib.parse.quote("repository:" + repository + ":pull", safe="")
                with urllib.request.urlopen(endpoint, timeout=runner.remaining(30)) as response:
                    tokens[repository] = contract.parse_json(response.read(1024 * 1024))["token"]
            request_headers = {"Authorization": "Bearer " + tokens[repository],
                "Accept": "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json"}
        row.update(build_source_transport.fetch(runner, url, directory, request_headers))
    validated = build_updates.validate_report(report, directory, registry, active, registry_hash, lock_hash,
                                               report["mode"], False, time.time())
    contract.require(validated["complete"], "independent live source check is incomplete")
    path = directory / "independent-report.json"
    path.write_bytes(contract.canonical(report))
    return {"report": report, "path": path, "directory": directory, "validated": validated}


def authenticate(runner, container, invocation, check_report):
    """Use only the authenticated cleartext returned by the image-owned sqv."""
    directory = runner.root / "updater-signatures"
    directory.mkdir()
    observations = invocation["validated"]["observations"]
    authenticated, proofs = {}, []
    keyring, verifier = runner.root / "os/debian-archive-keyring.pgp", runner.root / "os/sqv"
    for binding in invocation["validated"]["bindings"]:
        url = binding["inrelease_url"]
        if url not in authenticated:
            input_path = observations.file(url)
            output = directory / (hashlib.sha256(url.encode()).hexdigest() + ".Release")
            runner.inside(container, ["/usr/bin/sqv", "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp",
                "--output=/proof/" + output.relative_to(runner.root).as_posix(), "--cleartext",
                "/proof/" + input_path.relative_to(runner.root).as_posix()], timeout=30)
            authenticated[url] = (input_path, output, len(runner.commands) - 1)
        input_path, output, command_index = authenticated[url]
        materials.release(output.read_text(), binding["index_url"], binding["index_sha256"], binding["index_size"], time.time())
        proofs.append({**binding, "command_index": command_index, "inrelease": contract.reference(runner.root, input_path),
                       "release": contract.reference(runner.root, output), "packages": contract.reference(runner.root, observations.file(binding["index_url"])),
                       "keyring": contract.reference(runner.root, keyring), "verifier": contract.reference(runner.root, verifier)})
    result = {"schema_version": 1, "synthetic": False, "verified": True, "bindings": proofs,
              "check_report": contract.reference(runner.root, check_report)}
    path = directory / "proof.json"
    path.write_bytes(contract.canonical(result))
    return path


def validate_authentication(proof, root, commands, now, retained=False):
    contract.require(type(proof) is dict and set(proof) == {"schema_version", "synthetic", "verified", "bindings", "check_report"}
                     and type(proof["schema_version"]) is int and proof["schema_version"] == 1
                     and proof["synthetic"] is False and proof["verified"] is True
                     and type(proof["bindings"]) is list and len(proof["bindings"]) == 6, "updater signature proof is incomplete or synthetic")
    contract.reference_descriptor(proof["check_report"])
    if not retained:
        build_source_evidence.validate_report_journals(contract.verify_reference(root, proof["check_report"]), "check")
    seen, channels = set(), set()
    for row in proof["bindings"]:
        contract.require(set(row) == {"index_url", "index_sha256", "index_size", "inrelease_url", "command_index", "inrelease", "release", "packages", "keyring", "verifier"}
                         and row["index_url"] not in seen and type(row["command_index"]) is int
                         and 0 <= row["command_index"] < len(commands), "updater signature binding is invalid or duplicated")
        seen.add(row["index_url"])
        match = re.fullmatch(r"https://(deb\.debian\.org/debian|security\.debian\.org/debian-security)/dists/(trixie|trixie-security|trixie-updates)/main/binary-(amd64|arm64)/Packages\.(?:gz|xz)", row["index_url"])
        contract.require(match is not None and row["inrelease_url"] == row["index_url"].split("/main/", 1)[0] + "/InRelease"
                         and (match[2] == "trixie-security") == (match[1] == "security.debian.org/debian-security"),
                         "updater signature source channel differs")
        channels.add((match[2], match[3]))
        for field in ("inrelease", "release", "packages", "keyring", "verifier"):
            if retained:
                contract.reference_descriptor(row[field])
            else:
                contract.verify_reference(root, row[field])
        contract.require(row["index_sha256"] == row["packages"]["sha256"] and row["index_size"] == row["packages"]["size_bytes"],
                         "updater signature proof binds another index payload")
        command = commands[row["command_index"]]
        contract.require(type(command["exit_code"]) is int and command["exit_code"] == 0
                         and command["argv"][:3] == ["docker", "container", "exec"]
                         and command["argv"][4:] == ["/usr/bin/sqv", "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp",
                            "--output=/proof/" + row["release"]["path"], "--cleartext", "/proof/" + row["inrelease"]["path"]],
                         "updater signature verifier command differs or weakened its policy")
        if not retained:
            materials.release(contract.verify_reference(root, row["release"]).read_text(), row["index_url"], row["index_sha256"], row["index_size"], now)
    contract.require(channels == {(channel, arch) for channel in ("trixie", "trixie-security", "trixie-updates")
                                  for arch in ("amd64", "arm64")}, "updater signature platform/channel coverage differs")
    return proof
