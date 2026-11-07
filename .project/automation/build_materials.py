"""Inspect materialized build inputs without trusting their reported verdicts."""

import email.utils
import gzip
import hashlib
import json
import lzma
from pathlib import Path, PurePosixPath
import re
import struct
import tarfile

import build_contract as contract


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


def modules(value, downloads, source_module):
    contract.require(value and downloads, "resolved Go closure is empty")
    mains = [row for row in value if row.get("Main") is True]
    contract.require(len(mains) == 1 and mains[0]["Path"] == source_module, "resolved graph uses another native root")
    paths = [row["Path"] for row in value]
    contract.require(len(paths) == len(set(paths)), "resolved graph repeats a module")
    required = {effective(row) for row in value if row.get("Main") is not True}
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


def imported_packages(rows, source_module, identifier):
    contract.require(rows, "compiled package graph is empty")
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
        status = contract.parse_json(contract.verify_reference(root, index["signature_log"]).read_bytes())
        contract.require(set(status) == {"tool", "exit_code", "command_index", "verifier_sha256", "keyring_sha256", "inrelease_sha256", "release_sha256"}
                         and status["tool"] == "sqv" and type(status["exit_code"]) is int and status["exit_code"] == 0
                         and type(status["command_index"]) is int and 0 <= status["command_index"] < len(commands)
                         and all(status[key + "_sha256"] == index[key]["sha256"] for key in ("verifier", "keyring", "inrelease", "release")),
                         "OS closure has no bound signed Release verification")
        command = commands[status["command_index"]]
        expected_tail = ["/usr/bin/sqv", "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp",
                         "--output=/proof/" + index["release"]["path"], "--cleartext", "/proof/" + index["inrelease"]["path"]]
        contract.require(command["exit_code"] == 0 and command["argv"][:3] == ["docker", "container", "exec"]
                         and command["argv"][4:] == expected_tail, "signed Release verification used another command or weakened policy")
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
    plan = manifest["plan"]
    for row in manifest["image_inputs"].values():
        image_input(row, root, plan["platform"])
    for identifier, row in manifest["module_closures"].items():
        archive = contract.verify_reference(root, row["source_archive"])
        source_archive(archive, plan["sources"][identifier]["dependency_inputs"])
        source_module = archive_module(archive)
        graph = json_stream(contract.verify_reference(root, row["modules"]).read_text())
        acquired = modules(graph, json_stream(contract.verify_reference(root, row["downloads"]).read_text()), source_module)
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
            imported_packages(json_stream(contract.verify_reference(root, build["imports"]).read_text()), source_module, identifier)
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
