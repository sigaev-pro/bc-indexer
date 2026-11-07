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


def refresh(runner, invocation, registry, active, registry_hash, lock_hash):
    report = copy.deepcopy(invocation["report"])
    directory = invocation["directory"]
    target = directory / "independent"
    target.mkdir()
    selected = invocation["validated"]["selected"]
    archive_urls = {artifact["url"] for row in selected.values() for artifact in row.get("artifacts", [])}
    needed = invocation["validated"]["observations"].used
    tokens = {}
    for row in report["observations"]:
        url = row["url"]
        if url not in needed or url in archive_urls:
            continue
        output = target / hashlib.sha256(url.encode()).hexdigest()
        parsed = urllib.parse.urlsplit(url)
        started = time.time()
        headers = {}
        if parsed.hostname == "api.github.com":
            _, raw, _ = runner.run(["gh", "api", "--include", url], timeout=60)
            data = raw.read_bytes()
            separator = b"\r\n\r\n" if b"\r\n\r\n" in data else b"\n\n"
            header, found, body = data.partition(separator)
            contract.require(found and re.match(rb"HTTP/\S+ 200(?:\s|$)", header), "independent official GitHub observation did not succeed")
            for line in header.decode().splitlines()[1:]:
                key, split, value = line.partition(":")
                if split and key.lower() in {"link", "etag", "content-type"}:
                    headers[key.lower()] = value.strip()
            output.write_bytes(body)
        else:
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
            runner.fetch(url, output, headers=request_headers, maximum=256 * 1024 * 1024)
            headers = runner.fetches[-1]["headers"]
        row.update(status=200, headers=headers, body=contract.reference(directory, output), observed_at=started)
    validated = build_updates.validate_report(report, directory, registry, active, registry_hash, lock_hash,
                                               report["mode"], False, time.time())
    contract.require(validated["complete"], "independent live source check is incomplete")
    path = directory / "independent-report.json"
    path.write_bytes(contract.canonical(report))
    return {"report": report, "path": path, "directory": directory, "validated": validated}


def authenticate(runner, container, invocation):
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
    result = {"schema_version": 1, "synthetic": False, "verified": True, "bindings": proofs}
    path = directory / "proof.json"
    path.write_bytes(contract.canonical(result))
    return path


def validate_authentication(proof, root, commands, now, retained=False):
    contract.require(type(proof) is dict and set(proof) == {"schema_version", "synthetic", "verified", "bindings"}
                     and type(proof["schema_version"]) is int and proof["schema_version"] == 1
                     and proof["synthetic"] is False and proof["verified"] is True
                     and type(proof["bindings"]) is list and len(proof["bindings"]) == 6, "updater signature proof is incomplete or synthetic")
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
