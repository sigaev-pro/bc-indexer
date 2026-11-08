"""Inspect materialized build inputs without trusting their reported verdicts."""

import email.utils
import gzip
import hashlib
import json
import lzma
import math
from pathlib import Path, PurePosixPath
import re
import struct
import tarfile
import urllib.parse

import build_contract as contract


MAX_PACKAGE_SOURCE_REPORT = 16 * 1024 * 1024
SIGNATURE_LOG_FIELDS = {"tool", "exit_code", "command_index", "verifier_sha256", "keyring_sha256",
                        "inrelease_sha256", "release_sha256", "acquisition"}


def package_archive_url(url):
    contract.require(isinstance(url, str), "package archive URL is missing")
    parsed = urllib.parse.urlsplit(url)
    contract.require(parsed.scheme == "https" and parsed.netloc in {"deb.debian.org", "security.debian.org"}
                     and not parsed.query and not parsed.fragment
                     and re.fullmatch(r"/(?:debian|debian-security)/pool/.+\.deb", parsed.path)
                     and all(part not in {".", "..", ""} for part in parsed.path.split("/")[1:]),
                     "transport recovery is limited to official package archive GETs")


def package_fetch_journal(value, root):
    contract.require(type(value) is dict and set(value) == {"schema_version", "requests"}
                     and type(value["schema_version"]) is int and value["schema_version"] == 1
                     and type(value["requests"]) is list and 1 <= len(value["requests"]) <= 4096, "invalid package transport journal")
    result, identifiers = {}, set()
    for request in value["requests"]:
        contract.require(type(request) is dict and set(request) == {"request_id", "url", "output", "outcome", "attempts"}
                         and isinstance(request["request_id"], str) and re.fullmatch(r"[a-f0-9]{32}", request["request_id"])
                         and request["request_id"] not in identifiers, "invalid or duplicated package transport request")
        identifiers.add(request["request_id"])
        package_archive_url(request["url"])
        contract.verify_reference(root, request["output"])
        contract.require(request["output"]["path"] not in result, "package transport output is duplicated")
        attempts = request["attempts"]
        contract.require(type(attempts) is list and len(attempts) in {1, 2}
                         and request["outcome"] == ("FIRST_ATTEMPT_SUCCESS" if len(attempts) == 1 else "RECOVERED_TRANSPORT"),
                         "package transport is incomplete or exceeds its retry bound")
        previous = 0
        for ordinal, attempt in enumerate(attempts, 1):
            contract.require(type(attempt) is dict and set(attempt) == {"ordinal", "state", "started_at", "finished_at", "response_url", "status", "headers", "error", "body", "exception_partial_size", "retained_exception_partial_size", "observed_body_size", "body_truncated"}
                             and type(attempt["ordinal"]) is int and attempt["ordinal"] == ordinal
                             and all(type(attempt[key]) in (int, float) and math.isfinite(attempt[key]) for key in ("started_at", "finished_at"))
                             and previous <= attempt["started_at"] <= attempt["finished_at"], "package attempt identity or times differ")
            previous = attempt["finished_at"]
            contract.require(type(attempt["headers"]) is dict and all(k in {"content-type", "docker-content-digest", "link", "etag", "last-modified"}
                             and isinstance(v, str) for k, v in attempt["headers"].items()), "package attempt has unsafe response metadata")
            contract.verify_reference(root, attempt["body"])
            contract.require(attempt["body"]["size_bytes"] <= request["output"]["size_bytes"], "package attempt exceeds the archive bound")
            contract.require(type(attempt["observed_body_size"]) is int and attempt["observed_body_size"] == attempt["body"]["size_bytes"]
                             and attempt["body_truncated"] is False, "package attempt omitted or truncated observed bytes")
            contract.require(type(attempt["exception_partial_size"]) is int and type(attempt["retained_exception_partial_size"]) is int
                             and 0 <= attempt["retained_exception_partial_size"] == attempt["exception_partial_size"] <= attempt["body"]["size_bytes"],
                             "package attempt omits available exception bytes or exceeds its bound")
            if ordinal == len(attempts):
                contract.require(attempt["state"] == "SUCCEEDED" and attempt["error"] is None
                                 and attempt["exception_partial_size"] == 0
                                 and type(attempt["status"]) is int and attempt["status"] == 200
                                 and isinstance(attempt["response_url"], str)
                                 and all(attempt["body"][key] == request["output"][key] for key in ("sha256", "size_bytes")),
                                 "package success does not bind the exact archive")
                parsed = urllib.parse.urlsplit(attempt["response_url"])
                contract.require(parsed.scheme == "https" and parsed.netloc in {"deb.debian.org", "security.debian.org"}, "package response changed official source")
            else:
                error = attempt["error"]
                contract.require(attempt["state"] == "FAILED" and type(error) is dict
                                 and set(error) == {"type", "wrapped_type", "ssl_reason", "retryable"}
                                 and (error["type"], error["wrapped_type"]) in {("SSLEOFError", None), ("URLError", "SSLEOFError")}
                                 and error["ssl_reason"] == "UNEXPECTED_EOF_WHILE_READING" and error["retryable"] is True
                                 and (attempt["status"] is None or type(attempt["status"]) is int and attempt["status"] == 200),
                                 "package retry does not retain the permitted typed transport failure")
        result[request["output"]["path"]] = request
    return result


def read_signature_log(reference, root):
    status = contract.parse_json(contract.verify_reference(root, reference).read_bytes())
    contract.require(type(status) is dict and set(status) in
                     (SIGNATURE_LOG_FIELDS, SIGNATURE_LOG_FIELDS | {"archive_fetches", "original_signature_log"}),
                     "OS signature log has an invalid shape")
    if "archive_fetches" in status:
        original = contract.parse_json(contract.verify_reference(root, status["original_signature_log"]).read_bytes())
        contract.require(type(original) is dict and set(original) == SIGNATURE_LOG_FIELDS
                         and contract.canonical(original) == contract.canonical({key: status[key] for key in SIGNATURE_LOG_FIELDS}),
                         "extended OS signature log differs from its preserved original")
    return status


def package_transports(closures, root):
    references = []
    for closure in closures:
        for index in closure["signed_indexes"]:
            status = read_signature_log(index["signature_log"], root)
            contract.require("archive_fetches" in status, "OS closure has no sealed package transport evidence")
            references.append(status["archive_fetches"])
    contract.require(references and all(reference == references[0] for reference in references), "OS closures bind different package transport journals")
    contract.reference_descriptor(references[0])
    contract.require(references[0]["size_bytes"] <= 32 * 1024 * 1024, "package transport journal exceeds its bound")
    requests = package_fetch_journal(contract.parse_json(contract.verify_reference(root, references[0]).read_bytes()), root)
    for closure in closures:
        wanted = {(p["name"], p["version"], p["architecture"]) for p in closure["packages"]}
        indexes = {row["url"]: package_index(contract.verify_reference(root, row["packages"]), wanted) for row in closure["signed_indexes"]}
        for package in closure["packages"]:
            request = requests.get(package["archive"]["path"])
            record = indexes[package["index_url"]].get((package["name"], package["version"], package["architecture"]))
            contract.require(request is not None and record is not None and request["output"] == package["archive"]
                             and request["url"] == package["index_url"].split("/dists/", 1)[0] + "/" + record["Filename"],
                             "package transport does not bind its signed source and retained archive")
    return {"requests": len(requests), "recovered": sum(row["outcome"] == "RECOVERED_TRANSPORT" for row in requests.values())}


def package_source_observations(data):
    """Read source metadata only; no old component selection or verdict is trusted."""
    contract.require(len(data) <= MAX_PACKAGE_SOURCE_REPORT, "package source report exceeds its bound")
    report = contract.parse_json(data)
    contract.require(type(report) is dict and type(report.get("schema_version")) is int and report["schema_version"] == 1
                     and report.get("contract") == "component-update-v1" and report.get("mode") in {"check", "update"}
                     and report.get("synthetic") is False, "package source report has the wrong scope or is synthetic")
    rows, result = report.get("observations"), {}
    contract.require(type(rows) is list and 1 <= len(rows) <= 4096, "package source observations are missing or unbounded")
    for row in rows:
        contract.require(type(row) is dict and set(row) in ({"url", "status", "headers", "body", "observed_at"},
                                                         {"url", "status", "headers", "body", "observed_at", "acquisition"})
                         and isinstance(row["url"], str) and row["url"] not in result and type(row["status"]) is int
                         and type(row["headers"]) is dict and all(isinstance(k, str) and isinstance(v, str) for k, v in row["headers"].items())
                         and type(row["observed_at"]) in (int, float) and math.isfinite(row["observed_at"]),
                         "package source observation is malformed or duplicated")
        parsed = urllib.parse.urlsplit(row["url"])
        contract.require(parsed.scheme == "https" and parsed.netloc in {"api.github.com", "raw.githubusercontent.com", "go.dev", "dl.google.com",
                         "registry-1.docker.io", "deb.debian.org", "security.debian.org"}, "package source observation is not official HTTPS")
        contract.reference_descriptor(row["body"])
        if "acquisition" in row:
            # Historical input bytes only: fresh signatures establish acceptance.
            contract.reference_descriptor(row["acquisition"])
        result[row["url"]] = row
    return result


def package_source_pair(observations, url, digest):
    match = re.fullmatch(r"https://(deb\.debian\.org/debian|security\.debian\.org/debian-security)/dists/(trixie|trixie-security|trixie-updates)/main/binary-(amd64|arm64)/Packages\.(?:gz|xz)", url)
    contract.require(match is not None and (match[2] == "trixie-security") == (match[1] == "security.debian.org/debian-security"),
                     "retained package index has another source or channel")
    release_url = url.split("/main/", 1)[0] + "/InRelease"
    index, signed = observations.get(url), observations.get(release_url)
    contract.require(index is not None and signed is not None and index["status"] == signed["status"] == 200
                     and index["body"]["sha256"] == digest, "retained package pair is missing, unavailable, or differs from its pin")
    return index, signed


def package_acquisition(acquisition, index, root):
    contract.require(type(acquisition) is dict, "package acquisition evidence is missing")
    if acquisition.get("mode") == "live-fetch":
        contract.require(set(acquisition) == {"mode"}, "live acquisition contains retained-source claims")
        return
    contract.require(set(acquisition) == {"mode", "report", "index_observation", "inrelease_observation"}
                     and acquisition["mode"] == "retained-report", "package acquisition evidence is malformed")
    contract.reference_descriptor(acquisition["report"])
    contract.require(acquisition["report"]["size_bytes"] <= MAX_PACKAGE_SOURCE_REPORT, "package source report exceeds its bound")
    report = contract.verify_reference(root, acquisition["report"])
    observations = package_source_observations(report.read_bytes())
    original_index, original_signed = package_source_pair(observations, index["url"], index["packages"]["sha256"])
    contract.require(acquisition["index_observation"] == original_index and acquisition["inrelease_observation"] == original_signed,
                     "retained package observation metadata changed")
    for original, copied in ((original_index, index["packages"]), (original_signed, index["inrelease"])):
        contract.verify_reference(root, copied)
        contract.require(all(original["body"][key] == copied[key] for key in ("sha256", "size_bytes")),
                         "retained package bytes differ from their original observation")


def json_stream(data):
    decoder = json.JSONDecoder(object_pairs_hook=contract.unique_object,
                               parse_constant=lambda value: (_ for _ in ()).throw(ValueError("nonfinite JSON")))
    rows, offset = [], 0
    while offset < len(data):
        match = re.match(r"\s*", data[offset:])
        offset += match.end()
        if offset == len(data):
            break
        row, offset = decoder.raw_decode(data, offset)
        contract.require(type(row) is dict, "Go output contains a non-object")
        rows.append(row)
    return rows


def module_path(source):
    found = re.findall(r"^module\s+([^\s]+)\s*$", Path(source).read_text(), re.M)
    contract.require(len(found) == 1, "source go.mod has no unique module identity")
    return found[0].strip('"')


def effective(row):
    target = row.get("Replace", row)
    contract.require(isinstance(target.get("Path"), str) and isinstance(target.get("Version"), str)
                     and target["Version"].startswith("v"), "unresolved or local module replacement")
    return target["Path"], target["Version"]


def local_modules(value, source_archive):
    """Bind local replacements to regular files in the selected source archive."""
    result = {}
    for row in value:
        replacement = row.get("Replace")
        if replacement is None or replacement.get("Version"):
            continue
        path = replacement.get("Path", "")
        relative = path.removeprefix("./")
        contract.require(path.startswith("./") and relative
                         and all(part not in {"", ".", ".."} for part in relative.split("/"))
                         and "\\" not in relative, "local module replacement escapes its native source")
        directory = "/source/" + relative
        contract.require(replacement.get("Dir") == directory and replacement.get("GoMod") == directory + "/go.mod",
                         "local module metadata differs from its archived source directory")
        contract.require(isinstance(row.get("Path"), str) and row["Path"] not in result
                         and isinstance(row.get("Version"), str) and row["Version"].startswith("v"),
                         "local module identity is missing or duplicated")
        files, go_mod = {}, None
        with tarfile.open(source_archive, "r:") as archive:
            names = set()
            for member in archive:
                contract.require(member.name not in names, "source archive repeats an entry")
                names.add(member.name)
                name = PurePosixPath(member.name)
                contract.require(not name.is_absolute() and ".." not in name.parts, "source archive contains an unsafe path")
                if relative == member.name or relative.startswith(member.name.rstrip("/") + "/"):
                    contract.require(member.isdir(), "local module source traverses a non-directory")
                if not member.name.startswith(relative + "/") or member.isdir():
                    continue
                contract.require(member.isfile(), "local module source contains a link or special file")
                with archive.extractfile(member) as stream:
                    digest = hashlib.file_digest(stream, "sha256").hexdigest()
                files[member.name] = {"sha256": digest, "size_bytes": member.size}
                if member.name == relative + "/go.mod":
                    contract.require(member.size <= 1024 * 1024, "local module go.mod exceeds its bound")
                    go_mod = archive.extractfile(member).read().decode()
        found = re.findall(r"^module\s+([^\s]+)\s*$", go_mod or "", re.M)
        contract.require(len(found) == 1 and found[0].strip('"') == row["Path"],
                         "local module is missing or has another archived identity")
        result[row["Path"]] = {"path": relative, "version": row["Version"], "files": files}
    return result


def modules(value, downloads, source_module, local=None):
    contract.require(value and isinstance(downloads, list), "resolved Go closure is empty")
    local = local or {}
    mains = [row for row in value if row.get("Main") is True]
    contract.require(len(mains) == 1 and mains[0]["Path"] == source_module, "resolved graph uses another native root")
    paths = [row["Path"] for row in value]
    contract.require(len(paths) == len(set(paths)), "resolved graph repeats a module")
    required = {effective(row) for row in value if row.get("Main") is not True and row["Path"] not in local}
    acquired = {}
    for row in downloads:
        contract.require(not row.get("Error") and not row.get("Replace"), "module acquisition has an error or unresolved replacement")
        key = effective(row)
        contract.require(key not in acquired, "module acquisition repeats a module version")
        for field in ("Sum", "GoModSum"):
            contract.require(re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", row.get(field, "")), "module has no checked Go checksum")
        for field in ("Info", "GoMod", "Zip"):
            contract.require(isinstance(row.get(field), str) and row[field].startswith("/proof/cache/"),
                             "module cache artifact is outside its isolated graph")
        acquired[key] = row
    contract.require(set(acquired) == required, "acquired module closure differs from the resolved graph")
    return acquired


def imported_packages(rows, source_module, identifier, local=None):
    contract.require(rows, "compiled package graph is empty")
    local = local or {}
    seen, main = set(), False
    cache = "/proof/cache/" + identifier + "/"
    for row in rows:
        name = row.get("ImportPath")
        contract.require(isinstance(name, str) and name not in seen and not row.get("Incomplete")
                         and not row.get("Error") and not row.get("DepsErrors"), "compiled package graph is incomplete or duplicated")
        seen.add(name)
        module = row.get("Module")
        if module is None:
            contract.require(row.get("Standard") is True, "nonstandard package has no module identity")
            continue
        owner = module.get("Replace", module)
        contract.require(not name.startswith("github.com/nats-io/")
                         and not owner.get("Path", "").startswith("github.com/nats-io/"),
                         "NATS SDK crossed the native/Gateway boundary")
        if module.get("Main") is True:
            contract.require(module["Path"] == source_module and module.get("Dir") == "/source",
                             "native package uses another source root")
            main = True
        elif module.get("Path") in local:
            selected = local[module["Path"]]
            directory = "/source/" + selected["path"]
            package_dir = row.get("Dir", "")
            contract.require(owner.get("Path") == "./" + selected["path"] and not owner.get("Version")
                             and module.get("Version") == selected["version"]
                             and owner.get("Dir") == directory and owner.get("GoMod") == directory + "/go.mod"
                             and (name == module["Path"] or name.startswith(module["Path"] + "/"))
                             and (package_dir == directory or package_dir.startswith(directory + "/"))
                             and ".." not in PurePosixPath(package_dir).parts
                             and str(PurePosixPath(package_dir)) == package_dir,
                             "local package differs from its verified source-contained module")
        else:
            contract.require(isinstance(owner.get("Dir"), str) and owner["Dir"].startswith(cache),
                             "native package graph shares another component's cache")
    contract.require(main, "compiled package graph does not contain the selected native root")


def elf(path, platform):
    with Path(path).open("rb") as stream:
        header = stream.read(64)
    contract.require(len(header) == 64 and header[:6] == b"\x7fELF\x02\x01"
                     and struct.unpack_from("<H", header, 18)[0] == {"linux/amd64": 62, "linux/arm64": 183}[platform],
                     "materialized native binary has another architecture or is not a 64-bit ELF")


def build_info(value, compiler, source_module, commit, platform):
    lines = value.splitlines()
    contract.require(lines and lines[0].endswith(": " + compiler), "binary was built by another Go compiler")
    entries = [line.strip().split("\t") for line in lines[1:]]
    main = [row for row in entries if row[0] == "mod"]
    contract.require(len(main) == 1 and main[0][1] == source_module, "binary has another native module identity")
    settings = {}
    for row in entries:
        if row[0] == "build":
            contract.require(len(row) == 2 and "=" in row[1], "malformed Go build setting")
            key, value = row[1].split("=", 1)
            contract.require(key not in settings, "duplicate Go build setting")
            settings[key] = value
        if row[0] in {"dep", "=>"}:
            contract.require(not row[1].startswith("github.com/nats-io/"), "native binary links the NATS SDK")
    contract.require(settings.get("GOOS") == "linux" and settings.get("GOARCH") == platform.split("/")[1]
                     and settings.get("CGO_ENABLED") == "1" and settings.get("-trimpath") == "true"
                     and commit in settings.get("-ldflags", ""), "native binary lacks its exact source or build settings")


def source_archive(path, dependency_inputs):
    expected = {row["path"]: row["sha256"] for row in dependency_inputs}
    found = {}
    with tarfile.open(path, "r:") as archive:
        for entry in archive:
            name = PurePosixPath(entry.name)
            contract.require(not name.is_absolute() and ".." not in name.parts, "source archive contains an unsafe path")
            if entry.name in expected:
                contract.require(entry.isfile() and entry.name not in found, "dependency input is not one regular source file")
                stream = archive.extractfile(entry)
                found[entry.name] = hashlib.file_digest(stream, "sha256").hexdigest()
    contract.require(found == expected, "archived dependency inputs differ from their selected source hashes")


def archive_module(path):
    with tarfile.open(path, "r:") as archive:
        member = archive.getmember("go.mod")
        contract.require(member.isfile() and member.size <= 1024 * 1024, "source module input is unsafe")
        value = archive.extractfile(member).read().decode()
    found = re.findall(r"^module\s+([^\s]+)\s*$", value, re.M)
    contract.require(len(found) == 1, "archived go.mod has no unique module identity")
    return found[0].strip('"')


def archived_module_metadata(path):
    result = {}
    with tarfile.open(path, "r:") as archive:
        for member in archive:
            if member.name not in {"go.mod", "go.sum"}:
                continue
            limit = 1024 * 1024 if member.name == "go.mod" else 16 * 1024 * 1024
            contract.require(member.name not in result and member.isfile() and member.size <= limit,
                             "archived module metadata is duplicated, unsafe, or unbounded")
            result[member.name] = archive.extractfile(member).read()
    contract.require("go.mod" in result, "archived module file is missing")
    result.setdefault("go.sum", b"")
    return result


def checksum_entries(data):
    contract.require(len(data) <= 16 * 1024 * 1024, "module checksum file exceeds its bound")
    result = {}
    for line in data.decode().splitlines():
        fields = line.split()
        contract.require(len(fields) == 3 and fields[1].startswith("v")
                         and re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", fields[2]), "invalid module checksum entry")
        key = tuple(fields[:2])
        contract.require(key not in result, "module checksum entry is duplicated or conflicting")
        result[key] = fields[2]
    return result


def native_go_phase(arguments, identifier):
    """Accept only argument forms emitted by the fixed native executor."""
    targets = {"geth": ("./cmd/geth", "geth"), "bsc": ("./cmd/geth", "geth"),
               "bor": ("./cmd/cli/main.go", "bor"), "prysm": ("./cmd/beacon-chain", "beacon-chain"),
               "heimdall": ("./cmd/heimdalld", "heimdalld")}
    tail = arguments[1:]
    if tail in (["version"], ["mod", "download", "-json", "all"], ["list", "-m", "-json", "all"], ["mod", "verify"]):
        return "metadata"
    contract.require(identifier in targets, "unexpected Go phase for a module-only component")
    target, name = targets[identifier]
    binary = "/proof/binaries/" + identifier + "/" + name
    if tail == ["version", "-m", binary]:
        return "inspection"
    if tail[:1] == ["build"]:
        phase, flags = "build", tail[1:]
    else:
        contract.require(tail[:3] == ["list", "-deps", "-json"], "unexpected native Go command or global argument override")
        phase, flags = "imports", tail[3:]
    contract.require(flags and flags[-1] == target, "native Go command selects another source target")
    values, offset = {}, 0
    while offset < len(flags) - 1:
        flag = flags[offset]
        contract.require(flag not in values, "native Go command repeats a build flag")
        if flag in {"-trimpath", "-buildvcs=false", "-mod=readonly", "-pgo=prod.pprof"}:
            values[flag] = True
            offset += 1
        else:
            contract.require(flag in {"-p", "-tags", "-ldflags", "-o"} and offset + 1 < len(flags) - 1,
                             "native Go command overrides module metadata or uses an unapproved build flag")
            values[flag] = flags[offset + 1]
            offset += 2
    required = {"-trimpath", "-buildvcs=false", "-mod=readonly", "-p", "-ldflags"}
    if identifier in {"geth", "bsc"}:
        required.add("-tags")
        contract.require(values.get("-tags") == "urfave_cli_no_docs", "native build tags differ")
    if identifier == "bor":
        required.add("-pgo=prod.pprof")
    if phase == "build":
        required.add("-o")
        contract.require(values.get("-o") == binary, "native build output belongs to another component")
    contract.require(set(values) == required and re.fullmatch(r"[1-4]", values.get("-p", ""))
                     and re.fullmatch(r"-X [^\s=]+=[^\s]+(?: -X [^\s=]+=[^\s]+)*", values.get("-ldflags", "")),
                     "native Go command differs from the fixed build argument forms")
    return phase


def module_metadata(identifier, row, root, commands, build=None):
    """Bind alternate metadata to native Go commands, not an offline sumdb replay."""
    contract.require(identifier in contract.MODULES, "unknown native module metadata owner")
    directory = "sources/" + identifier + "/metadata/"
    originals = archived_module_metadata(contract.verify_reference(root, row["source_archive"]))

    def invocation(command):
        argv = command["argv"]
        if argv[:3] != ["docker", "container", "exec"]:
            return None
        offset, environment = 3, {}
        while offset < len(argv) and argv[offset] == "--env":
            contract.require(offset + 1 < len(argv) and "=" in argv[offset + 1], "invalid native Go environment")
            key, value = argv[offset + 1].split("=", 1)
            contract.require(key not in environment, "native Go environment repeats a variable")
            environment[key] = value
            offset += 2
        contract.require(offset + 1 < len(argv), "native command has no container or executable")
        return argv[offset], environment, argv[offset + 1:]

    owner, authenticated, positions = None, None, []
    for field, tail in (("downloads", ["mod", "download", "-json", "all"]),
                        ("modules", ["list", "-m", "-json", "all"]), ("verification", ["mod", "verify"])):
        matches = [(i, command) for i, command in enumerate(commands) if command.get("stdout") == row[field]]
        contract.require(len(matches) == 1, "module metadata lacks a unique native command")
        position, command = matches[0]
        contract.command_record(command, root)
        parsed = invocation(command)
        contract.require(parsed is not None and parsed[2] == ["/usr/local/go/bin/go", *tail], "module metadata used another native command")
        if owner is None:
            owner, authenticated = parsed[:2]
        contract.require(parsed[:2] == (owner, authenticated), "module acquisition crossed its component or environment")
        positions.append(position)
    contract.require(positions == sorted(set(positions)), "module acquisition commands are out of order")
    required = {"GOFLAGS": "-mod=readonly -modfile=/proof/" + directory + "go.mod", "GOTOOLCHAIN": "local",
                "GOWORK": "off", "GOENV": "off", "GOPRIVATE": "", "GONOSUMDB": "", "GONOPROXY": "",
                "GOMODCACHE": "/proof/cache/" + identifier + "/mod", "GOPATH": "/proof/cache/" + identifier + "/gopath",
                "GOCACHE": "/proof/cache/" + identifier + "/build"}
    contract.require(all(authenticated.get(key) == value for key, value in required.items())
                     and authenticated.get("GOPROXY") == "https://proxy.golang.org" and authenticated.get("GOSUMDB") == "sum.golang.org",
                     "module checksum acquisition lacks its isolated authenticated environment")
    parsed_commands = [(i, command, invocation(command)) for i, command in enumerate(commands)]
    phases = []
    for position, command, parsed in parsed_commands:
        if parsed is not None and parsed[2][0] == "/usr/local/go/bin/go":
            belongs = parsed[0] == owner or parsed[1].get("GOMODCACHE") == required["GOMODCACHE"] or parsed[1].get("GOFLAGS") == required["GOFLAGS"]
            if not belongs:
                continue
            contract.require(parsed[0] == owner, "native Go phase uses another component container")
            contract.command_record(command, root)
            phase = native_go_phase(parsed[2], identifier)
            authenticated_policy = ("https://proxy.golang.org", "sum.golang.org")
            policies = {("off", "off")} if phase in {"build", "imports"} else {authenticated_policy}
            if phase == "inspection":
                policies.add(("off", "off"))
            contract.require(all(parsed[1].get(key) == value for key, value in required.items())
                             and (parsed[1].get("GOPROXY"), parsed[1].get("GOSUMDB")) in policies,
                             "native Go phase changed its module metadata or authentication policy")
            if phase in {"build", "imports"}:
                phases.append((position, command, phase))
    main = [item for item in json_stream(contract.verify_reference(root, row["modules"]).read_text()) if item.get("Main") is True]
    contract.require(len(main) == 1 and main[0].get("Dir") == "/source"
                     and main[0].get("GoMod") == "/proof/" + directory + "go.mod", "native graph used another module metadata file or root")
    captured = {}
    for name in ("go.mod", "go.sum"):
        matches = [(i, command) for i, command, parsed in parsed_commands
                   if parsed == (owner, {}, ["cat", "/proof/" + directory + name])]
        contract.require(len(matches) == 1 and matches[0][0] > positions[-1], "module metadata capture is missing, duplicated, or out of order")
        position, command = matches[0]
        contract.require(all(position < item[0] for item in phases), "module metadata capture follows a build or import phase")
        contract.command_record(command, root)
        limit = 1024 * 1024 if name == "go.mod" else 16 * 1024 * 1024
        contract.require(command["stdout"]["size_bytes"] <= limit, "module metadata capture exceeds its bound")
        captured[name] = contract.verify_reference(root, command["stdout"]).read_bytes()
        actual = contract.safe_file(root, directory + name)
        contract.require(actual.stat().st_size == len(captured[name]) and actual.read_bytes() == captured[name],
                         "module metadata changed after its captured native verification")
    contract.require(captured["go.mod"] == originals["go.mod"], "alternate module file differs from its archived input")
    original_sums, final_sums = checksum_entries(originals["go.sum"]), checksum_entries(captured["go.sum"])
    contract.require(all(final_sums.get(key) == value for key, value in original_sums.items()), "original module checksum entries changed or disappeared")
    selected = {}
    for item in json_stream(contract.verify_reference(root, row["downloads"]).read_text()):
        contract.require(not item.get("Error"), "module download contains an error")
        for field, suffix in (("Sum", ""), ("GoModSum", "/go.mod")):
            key = (item["Path"], item["Version"] + suffix)
            contract.require(key not in selected and final_sums.get(key) == item.get(field), "selected module checksum differs from its authenticated download")
            selected[key] = item[field]
    contract.require(all(key[1].endswith("/go.mod") for key in final_sums.keys() - original_sums.keys() - selected.keys()),
                     "generated checksum file contains an unselected module archive")
    if build is not None:
        imports = [item for item in phases if item[2] == "imports"]
        compiled = [item for item in phases if item[2] == "build"]
        contract.require(len(imports) == len(compiled) == 1 and imports[0][0] < compiled[0][0]
                         and imports[0][1]["stdout"] == build["imports"], "final build lacks its accepted import and compile commands")
        arguments = invocation(compiled[0][1])[2]
        contract.require(arguments[arguments.index("-o") + 1] == "/proof/" + build["binary"]["path"],
                         "final binary differs from its accepted compile output")
        contract.verify_reference(root, build["binary"])


def fields(lines):
    """Read Debian control records while preserving multiline hash sections."""
    record, current = {}, None
    for raw in lines:
        line = raw.rstrip("\r\n")
        if not line:
            if record:
                yield record
            record, current = {}, None
        elif line.startswith((" ", "\t")):
            contract.require(current is not None, "Debian field continuation has no parent")
            record[current] += "\n" + line[1:]
        else:
            contract.require(":" in line, "malformed Debian field")
            current, value = line.split(":", 1)
            contract.require(current not in record, "duplicate Debian field")
            record[current] = value.lstrip()
    if record:
        yield record


def release(value, index_url, index_sha256, index_size, now):
    rows = list(fields(value.splitlines()))
    contract.require(len(rows) == 1 and rows[0].get("Origin") == "Debian", "signed Release is not one Debian document")
    record = rows[0]
    distribution = index_url.split("/dists/", 1)[1].split("/", 1)[0]
    contract.require(record.get("Codename") == distribution, "signed Release belongs to another Debian channel")
    for key in ("Date", "Valid-Until"):
        if key in record:
            timestamp = email.utils.parsedate_to_datetime(record[key]).timestamp()
            contract.require(timestamp <= now + 300 if key == "Date" else timestamp > now,
                             "signed Release is from the future or has expired")
    contract.require("Date" in record, "signed Release has no publication time")
    relative = index_url.split("/dists/", 1)[1].split("/", 1)[1]
    matches = [line.split() for line in record.get("SHA256", "").splitlines() if len(line.split()) == 3 and line.split()[2] == relative]
    contract.require(matches == [[index_sha256, str(index_size), relative]], "signed Release does not bind the exact selected Packages payload")


def package_index(path, wanted):
    opener = gzip.open if str(path).endswith(".gz") else lzma.open if str(path).endswith(".xz") else open
    selected = {}
    with opener(path, "rt", encoding="utf-8") as stream:
        for row in fields(stream):
            key = (row.get("Package"), row.get("Version"), row.get("Architecture"))
            if key not in wanted:
                continue
            contract.require(key not in selected and contract.HEX.fullmatch(row.get("SHA256", ""))
                             and row.get("Size", "").isdigit(), "signed index has an ambiguous or unhashed package")
            filename = row.get("Filename", "")
            contract.require(filename.startswith("pool/") and ".." not in PurePosixPath(filename).parts,
                             "signed package filename escapes the archive")
            selected[key] = row
    return selected


def installed(value, architecture):
    result = set()
    for line in value.splitlines():
        row = tuple(line.split("\t"))
        contract.require(len(row) == 3 and row[0] and row[1] and row[2] in {architecture, "all"}
                         and row not in result, "installed package inventory is malformed")
        result.add(row)
    contract.require(result, "installed package inventory is empty")
    return result


def signature_status(index, root, commands):
    status = read_signature_log(index["signature_log"], root)
    contract.require(status["tool"] == "sqv" and type(status["exit_code"]) is int and status["exit_code"] == 0
                     and type(status["command_index"]) is int and 0 <= status["command_index"] < len(commands)
                     and all(status[key + "_sha256"] == index[key]["sha256"] for key in ("verifier", "keyring", "inrelease", "release")),
                     "OS closure has no bound signed Release verification")
    package_acquisition(status["acquisition"], index, root)
    command = commands[status["command_index"]]
    expected_tail = ["/usr/bin/sqv", "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp",
                     "--output=/proof/" + index["release"]["path"], "--cleartext", "/proof/" + index["inrelease"]["path"]]
    contract.require(command["exit_code"] == 0 and command["argv"][:3] == ["docker", "container", "exec"]
                     and command["argv"][4:] == expected_tail, "signed Release verification used another command or weakened policy")
    return status


def os_closure(row, root, plan, now, commands):
    architecture = plan["platform"].split("/")[1]
    required = installed(contract.verify_reference(root, row["installed"]).read_text(), architecture)
    actual = {(item["name"], item["version"], item["architecture"]): item for item in row["packages"]}
    contract.require(len(actual) == len(row["packages"]) and set(actual) == required,
                     "retained package archives do not cover the complete installed OS closure")
    allowed_indexes = {item["url"]: item["sha256"] for item in plan["package_indexes"]}
    records = {}
    for index in row["signed_indexes"]:
        path = contract.verify_reference(root, index["packages"])
        contract.require(contract.file_hash(path) == allowed_indexes[index["url"]], "selected Packages payload changed")
        release_text = contract.verify_reference(root, index["release"]).read_text()
        release(release_text, index["url"], allowed_indexes[index["url"]], path.stat().st_size, now)
        signature_status(index, root, commands)
        records[index["url"]] = package_index(path, required)
    for key, package in actual.items():
        record = records[package["index_url"]].get(key)
        contract.require(record is not None and package["archive"]["sha256"] == record["SHA256"]
                         and package["archive"]["size_bytes"] == int(record["Size"]),
                         "OS package bytes are not bound by the selected signed index")
        path = contract.verify_reference(root, package["archive"])
        with path.open("rb") as stream:
            contract.require(stream.read(8) == b"!<arch>\n", "OS archive is not a Debian ar file")
    return required


def oci(row, root, plan, identifier):
    contract.require(contract.parse_json(contract.verify_reference(root, row["oci_layout"]).read_bytes()) == {"imageLayoutVersion": "1.0.0"},
                     "built image has no OCI image-layout marker")
    values = {key: contract.parse_json(contract.verify_reference(root, row[key]).read_bytes())
              for key in ("oci_index", "oci_manifest", "oci_config")}
    index, manifest, config = (values[key] for key in ("oci_index", "oci_manifest", "oci_config"))
    contract.require(type(index.get("schemaVersion")) is int and index["schemaVersion"] == 2
                     and type(index.get("manifests")) is list and len(index["manifests"]) == 1,
                     "OCI index is not one concrete built image")
    def descriptor(value, ref, media_type):
        contract.require(value.get("mediaType") == media_type and value.get("digest") == "sha256:" + ref["sha256"]
                         and type(value.get("size")) is int and value["size"] == ref["size_bytes"], "OCI descriptor does not bind materialized bytes")
    descriptor(index["manifests"][0], row["oci_manifest"], "application/vnd.oci.image.manifest.v1+json")
    contract.require(index["manifests"][0].get("platform") == {"os": "linux", "architecture": plan["platform"].split("/")[1]},
                     "OCI index advertises another platform")
    contract.require(type(manifest.get("schemaVersion")) is int and manifest["schemaVersion"] == 2
                     and type(manifest.get("layers")) is list and len(manifest["layers"]) == 1, "OCI manifest is incomplete")
    descriptor(manifest["config"], row["oci_config"], "application/vnd.oci.image.config.v1+json")
    descriptor(manifest["layers"][0], row["oci_layer"], "application/vnd.oci.image.layer.v1.tar")
    binary_name = {"geth": "geth", "bsc": "geth", "bor": "bor", "prysm": "beacon-chain", "heimdall": "heimdalld"}[identifier]
    binary_path = "usr/local/bin/" + binary_name
    contract.require(config.get("os") == "linux" and config.get("architecture") == plan["platform"].split("/")[1]
                     and config.get("rootfs") == {"type": "layers", "diff_ids": ["sha256:" + row["oci_layer"]["sha256"]]}
                     and config.get("config", {}).get("Entrypoint") == ["/" + binary_path], "OCI config differs from its build platform or binary")
    labels = config.get("config", {}).get("Labels", {})
    contract.require(labels.get("io.extractor.source-commit") == plan["sources"][identifier]["commit"]
                     and labels.get("io.extractor.lock-sha256") == plan["lock_sha256"]
                     and labels.get("io.extractor.base-image") == plan["images"]["debian-runtime-image"]["reference"], "OCI source, base, or lock labels differ")
    found = []
    with tarfile.open(contract.verify_reference(root, row["oci_layer"]), "r:") as archive:
        for entry in archive:
            path = PurePosixPath(entry.name)
            contract.require(not path.is_absolute() and ".." not in path.parts, "OCI layer contains an escaping path")
            if str(path) == binary_path:
                contract.require(entry.isfile(), "OCI native binary is not a regular file")
                stream = archive.extractfile(entry)
                found.append((entry.size, hashlib.file_digest(stream, "sha256").hexdigest()))
    contract.require(found == [(row["binary"]["size_bytes"], row["binary"]["sha256"])], "OCI layer omits or substitutes the compiled native binary")


def image_input(row, root, platform):
    index_path = contract.verify_reference(root, row["index"])
    manifest_path = contract.verify_reference(root, row["manifest"])
    config_path = contract.verify_reference(root, row["config"])
    contract.require("sha256:" + contract.file_hash(index_path) == row["reference"].split("@", 1)[1]
                     and "sha256:" + contract.file_hash(manifest_path) == row["platform_digest"],
                     "acquired image metadata differs from its exact pin")
    index = contract.parse_json(index_path.read_bytes())
    matching = [item for item in index.get("manifests", []) if item.get("platform", {}).get("os") == "linux"
                and item.get("platform", {}).get("architecture") == platform.split("/")[1]]
    contract.require(len(matching) == 1 and matching[0]["digest"] == row["platform_digest"]
                     and matching[0]["size"] == manifest_path.stat().st_size, "OCI index does not select the requested immutable platform")
    manifest = contract.parse_json(manifest_path.read_bytes())
    config_digest = "sha256:" + contract.file_hash(config_path)
    contract.require(manifest["config"]["digest"] == config_digest and manifest["config"]["size"] == config_path.stat().st_size,
                     "acquired image config differs from its manifest")
    config = contract.parse_json(config_path.read_bytes())
    contract.require(config.get("os") == "linux" and config.get("architecture") == platform.split("/")[1],
                     "acquired image config has another platform")
    inspected = contract.parse_json(contract.verify_reference(root, row["inspection"]).read_bytes())
    accepted_references = {row["reference"], row["reference"].removeprefix("docker.io/library/")}
    contract.require(type(inspected) is list and len(inspected) == 1 and inspected[0].get("Id") == config_digest
                     and bool(accepted_references.intersection(inspected[0].get("RepoDigests", [])))
                     and inspected[0].get("Os") == "linux" and inspected[0].get("Architecture") == platform.split("/")[1],
                     "Docker did not materialize the selected content and platform")
    with tarfile.open(contract.verify_reference(root, row["archive"]), "r:") as archive:
        descriptor = contract.parse_json(archive.extractfile("manifest.json").read(1024 * 1024))
        contract.require(type(descriptor) is list and len(descriptor) == 1, "saved image archive has an ambiguous image set")
        saved_config = archive.extractfile(descriptor[0]["Config"])
        contract.require("sha256:" + hashlib.file_digest(saved_config, "sha256").hexdigest() == config_digest,
                         "saved image archive contains another config")
        layers = descriptor[0]["Layers"]
        contract.require(len(layers) == len(config["rootfs"]["diff_ids"]), "saved image archive has an incomplete layer set")
        for name, expected in zip(layers, config["rootfs"]["diff_ids"]):
            stream = archive.extractfile(name)
            contract.require(stream is not None, "saved image layer is missing")
            magic = stream.read(2)
            stream.seek(0)
            payload = gzip.GzipFile(fileobj=stream) if magic == b"\x1f\x8b" else stream
            contract.require("sha256:" + hashlib.file_digest(payload, "sha256").hexdigest() == expected,
                             "saved image layer differs from its selected config")


def validate_materialized(manifest, root, source, now):
    """Only actual fixed-command execution may call this acceptance inspection."""
    contract.validate_manifest(manifest, root, source)
    import build_source_evidence
    build_source_evidence.validate_updater_journals(manifest["updater"], root)
    package_transports(list(manifest["os_closures"].values()), root)
    plan = manifest["plan"]
    for row in manifest["image_inputs"].values():
        image_input(row, root, plan["platform"])
    for identifier, row in manifest["module_closures"].items():
        archive = contract.verify_reference(root, row["source_archive"])
        source_archive(archive, plan["sources"][identifier]["dependency_inputs"])
        source_module = archive_module(archive)
        graph = json_stream(contract.verify_reference(root, row["modules"]).read_text())
        local = local_modules(graph, archive)
        contract.require(contract.parse_json(contract.verify_reference(root, row["local_modules"]).read_bytes()) == local,
                         "retained local modules differ from their selected source archive")
        acquired = modules(graph, json_stream(contract.verify_reference(root, row["downloads"]).read_text()), source_module, local)
        module_metadata(identifier, row, root, manifest["commands"], manifest["builds"].get(identifier))
        artifacts = {(item["module"], item["version"]): item for item in row["artifacts"]}
        contract.require(len(artifacts) == len(row["artifacts"]) and set(artifacts) == set(acquired),
                         "retained module archives differ from the complete resolved closure")
        for key, item in artifacts.items():
            for field, upstream in (("zip", "Zip"), ("mod", "GoMod"), ("info", "Info")):
                expected_path = acquired[key][upstream].removeprefix("/proof/")
                contract.require(item[field]["path"] == expected_path
                                 and expected_path.startswith("cache/" + identifier + "/"),
                                 "module archive is shared, substituted, or outside its own graph")
        contract.require(contract.verify_reference(root, row["verification"]).read_text().strip() == "all modules verified",
                         "resolved module content was not verified by Go")
        if identifier in contract.NATIVE:
            build = manifest["builds"][identifier]
            elf(contract.verify_reference(root, build["binary"]), plan["platform"])
            build_info(contract.verify_reference(root, build["build_info"]).read_text(), plan["compiler"]["version"],
                       source_module, row["source_commit"], plan["platform"])
            imported_packages(json_stream(contract.verify_reference(root, build["imports"]).read_text()), source_module, identifier, local)
            oci(build, root, plan, identifier)
    for identifier, row in manifest["os_closures"].items():
        complete = os_closure(row, root, plan, now, manifest["commands"])
        if identifier == "golang-build-image":
            for package in plan["packages"].values():
                contract.require(any(name == package["package"] and version == package["version"] for name, version, _ in complete),
                                 "compiler image did not install every selected build package revision")
        else:
            for build in manifest["builds"].values():
                contract.require({(item["name"], item["version"], item["architecture"]) for item in build["runtime_packages"]} <= complete,
                                 "runtime OS closure is missing a linked native library package")
    return manifest
