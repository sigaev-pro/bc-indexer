"""Discover exact candidate pins without changing accepted inputs."""

import argparse
import copy
import functools
import gzip
import hashlib
import lzma
from pathlib import Path
import re
import sys
import time
import uuid

from core import (PLATFORMS, decode, describe, encode, hex_string, load_inputs,
                  need, regular, sha)
from sources import Transport, commit_for, debian_order, select_release


def raw_url(repository, commit, path):
    return "https://raw.githubusercontent.com/" + repository + "/" + commit + "/" + path


def acquire_artifact(transport, url, digest, size):
    need(hex_string(digest) and type(size) is int and size >= 0, "Invalid official artifact metadata")
    path = transport.fetch(url)
    need(transport.rows[url]["status"] == 200 and sha(path) == digest and path.stat().st_size == size, "Official artifact bytes differ from metadata")


def git_pin(entry, active, selected, transport):
    identifier, source = entry["id"], entry["source"]
    latest = select_release(entry, transport)
    version, owner = latest, None
    if source["channel"] == "upstream-bundle":
        owner = identifier.split("-", 1)[0]
        need(owner in selected, "Owning native client selection is incomplete")
        native = selected[owner]
        module = "cometbft" if identifier.endswith("cometbft") else "cosmos-sdk"
        original = "github.com/" + ("cometbft/cometbft" if module == "cometbft" else "cosmos/cosmos-sdk")
        contents = transport.data(raw_url(native["source_repository"], native["commit"], "go.mod")).decode()
        pattern = r"^\s*(?:replace\s+)?" + re.escape(original) + r"(?:\s+v\S+)?\s*=>\s*github\.com/0xPolygon/" + module + r"\s+(\S+)\s*(?://[^\n]*)?$"
        matches = re.findall(pattern, contents, re.M)
        need(len(matches) == 1, "Native go.mod must select one exact owned replacement")
        version = matches[0]
    commit = commit_for(source["repository"], version, transport)
    pin = dict(version=version, source_repository=source["repository"], commit=commit, compatibility="NOT_RUN", dependency_inputs=[])
    if owner:
        pin["owner_component"] = owner
    paths = [item["path"] for item in active.get("dependency_inputs", [])]
    need(len(paths) == len(set(paths)), "Active dependency input paths are duplicated")
    for path in paths:
        need(isinstance(path, str) and not path.startswith("/") and all(p not in ("", ".", "..") for p in path.split("/")), "Unsafe dependency input path")
        url = raw_url(source["repository"], commit, path)
        payload = transport.data(url)
        pin["dependency_inputs"].append(dict(path=path, url=url, sha256=hashlib.sha256(payload).hexdigest()))
    return latest, pin


def go_pin(entry, transport, mode):
    version = select_release(entry, transport)
    releases = transport.json(entry["source"]["url"])
    matches = [r for r in releases if r.get("version") == version]
    need(len(matches) == 1, "Go release metadata is ambiguous")
    files = [f for f in matches[0]["files"] if f.get("kind") == "source" or f.get("os") == "linux" and f.get("arch") in ("amd64", "arm64") and f.get("kind") == "archive"]
    need(len(files) == 3 and len({f["filename"] for f in files}) == 3
         and sum(f.get("kind") == "source" for f in files) == 1
         and {f.get("arch") for f in files if f.get("kind") == "archive"} == {"amd64", "arm64"}, "Go archive coverage differs")
    artifacts = []
    for f in files:
        name = f["filename"]
        need(isinstance(name, str) and re.fullmatch(r"[A-Za-z0-9._-]+", name), "Unsafe Go archive filename")
        artifact = dict(name=name, url="https://go.dev/dl/" + name, sha256=f["sha256"], size_bytes=f["size"], payload_verified=mode == "update")
        need(hex_string(f["sha256"]) and type(f["size"]) is int and f["size"] > 0, "Invalid Go checksum metadata")
        if mode == "update":
            acquire_artifact(transport, artifact["url"], f["sha256"], f["size"])
        artifacts.append(artifact)
    return version, dict(version=version, artifacts=artifacts, compatibility="NOT_RUN")


def image_pin(entry, selected, transport):
    source, identifier = entry["source"], entry["id"]
    repository = source["repository"].removeprefix("docker.io/")
    tags = transport.pages(source["url"], repository)
    need(all(isinstance(t, str) for t in tags), "Malformed image tag listing")
    if identifier == "golang-build-image":
        version = selected["go"]["version"].removeprefix("go") + "-trixie"
    elif identifier == "nats-runtime-image":
        version = selected["nats-server"]["version"].removeprefix("v") + "-alpine"
    else:
        candidates = [(int(m[1]), tag) for tag in tags if (m := re.fullmatch(r"13\.([0-9]+)-slim", tag))]
        need(candidates, "No stable Debian 13 slim image")
        version = max(candidates)[1]
    need(version in tags, "Selected component version has no matching official image")
    prefix = "https://registry-1.docker.io/v2/" + repository + "/manifests/"
    payload = transport.data(prefix + version)
    index = decode(payload)
    digest = "sha256:" + hashlib.sha256(payload).hexdigest()
    platforms = {}
    for item in index["manifests"]:
        platform = item.get("platform", {})
        if platform.get("os") != "linux" or platform.get("architecture") not in ("amd64", "arm64"):
            continue
        name = "linux/" + platform["architecture"]
        need(name not in platforms and re.fullmatch(r"sha256:[a-f0-9]{64}", item["digest"]), "Duplicate or invalid image platform")
        platforms[name] = item["digest"]
        child = transport.data(prefix + item["digest"])
        need("sha256:" + hashlib.sha256(child).hexdigest() == item["digest"], "Image platform bytes changed")
        parsed = decode(child)
        need(parsed.get("schemaVersion") == 2, "Unsupported image manifest version")
    need(set(platforms) == set(PLATFORMS), "Image omits a candidate architecture")
    return version, dict(version=version, image=source["repository"], digest=digest, pull_reference=source["repository"] + "@" + digest,
                         platform_manifests=platforms, compatibility="NOT_RUN")


def control_records(text):
    record, last = {}, None
    for line in text.splitlines() + [""]:
        if not line:
            if record:
                yield record
            record, last = {}, None
        elif line[0].isspace():
            need(last is not None, "Malformed package continuation")
            record[last] += "\n" + line
        else:
            key, sep, value = line.partition(":")
            need(sep and key not in record, "Malformed or duplicate package field")
            record[key], last = value.strip(), key


class Packages:
    def __init__(self, active, transport):
        self.active, self.transport = active, transport
        self.indexes, self.records, self.urls = [], {}, set()
        self.loaded = False

    def load(self):
        if self.loaded:
            return
        transport = self.transport
        inputs = self.active["package_indexes"]
        need(len(inputs) == 6, "Expected six package channel/architecture indexes")
        seen = set()
        wanted = {p["package"] for p in self.active["components"].values() if "package" in p}
        for item in inputs:
            url = item["url"]
            match = re.fullmatch(r"https://(deb\.debian\.org/debian|security\.debian\.org/debian-security)/dists/(trixie|trixie-security|trixie-updates)/main/binary-(amd64|arm64)/Packages\.(gz|xz)", url)
            need(match is not None, "Unsupported Debian index source")
            host, channel, arch, compression = match.groups()
            need((channel, arch) not in seen and (channel == "trixie-security") == host.startswith("security."), "Duplicate or crossed Debian channel")
            seen.add((channel, arch))
            before = set(transport.used)
            payload = transport.data(url)
            inrelease = "https://" + host + "/dists/" + channel + "/InRelease"
            transport.data(inrelease)
            self.urls.update(transport.used - before)
            digest = hashlib.sha256(payload).hexdigest()
            self.indexes.append(dict(url=url, sha256=digest))
            decoder = gzip.decompress if compression == "gz" else lzma.decompress
            decoded = decoder(payload)
            need(len(decoded) <= 512 * 1024**2, "Decoded package index exceeds the size bound")
            for row in control_records(decoded.decode()):
                if row.get("Package") not in wanted:
                    continue
                need({"Package", "Version", "Architecture", "Filename", "SHA256", "Size"} <= set(row), "Incomplete package metadata")
                need(row["Architecture"] in (arch, "all") and hex_string(row["SHA256"]) and row["Size"].isdigit(), "Invalid package architecture or checksum")
                filename = row["Filename"]
                need(not filename.startswith("/") and all(p not in ("", ".", "..") for p in filename.split("/")), "Unsafe package archive path")
                self.records.setdefault((row["Package"], arch), []).append((row, url))
        need(seen == {(c, a) for c in ("trixie", "trixie-security", "trixie-updates") for a in ("amd64", "arm64")}, "Incomplete Debian channel coverage")
        self.loaded = True

    def select(self, entry, mode):
        self.load()
        name = entry["source"]["package"]
        versions, artifacts = {}, []
        for platform in PLATFORMS:
            choices = self.records.get((name, platform.split("/")[1]), [])
            need(choices, "Package source omits a required architecture")
            record, index = max(choices, key=functools.cmp_to_key(lambda a, b: debian_order(a[0]["Version"], b[0]["Version"])))
            url = index.split("/dists/", 1)[0] + "/" + record["Filename"]
            versions[platform] = record["Version"]
            artifacts.append(dict(platform=platform, version=record["Version"], url=url, sha256=record["SHA256"], size_bytes=int(record["Size"]), payload_verified=mode == "update"))
            if mode == "update":
                acquire_artifact(self.transport, url, record["SHA256"], int(record["Size"]))
        return versions, dict(package=name, version_by_platform=versions, artifacts=artifacts, package_indexes=self.indexes, compatibility="NOT_RUN")


def discover(registry, active, transport, mode):
    rows, selected = [], {}
    packages = Packages(active, transport)
    # Native owners and toolchains precede their dependent bundle and image pins.
    priority = {"git_source": 0, "artifact_set": 1, "oci_image": 3, "distribution_package": 4}
    ordered = sorted(registry["components"], key=lambda e: 2 if e["source"]["channel"] == "upstream-bundle" else priority[e["pin_kind"]])
    for entry in ordered:
        identifier = entry["id"]
        current = active["components"][identifier]
        row = dict(id=identifier, current=current.get("version", current.get("version_by_platform")), latest_stable=None,
                   selected=None, compatibility="NOT_RUN", notices=["Compatibility and security review remain required before promotion."], blocked_reasons=[], sources=[])
        transport.used = set()
        try:
            kind = entry["pin_kind"]
            if kind == "git_source":
                latest, pin = git_pin(entry, current, selected, transport)
            elif kind == "artifact_set":
                latest, pin = go_pin(entry, transport, mode)
            elif kind == "oci_image":
                latest, pin = image_pin(entry, selected, transport)
            elif kind == "distribution_package":
                latest, pin = packages.select(entry, mode)
                transport.used.update(packages.urls)
                row["notices"].append("Debian signatures require independent verification with the pinned image keyring.")
            else:
                raise ValueError("Unsupported registered component kind")
            selected[identifier] = pin
            row.update(latest_stable=latest, selected=pin)
        except (ValueError, KeyError, TypeError, OSError, AttributeError, OverflowError, lzma.LZMAError) as error:
            row["blocked_reasons"].append("Source discovery failed: " + str(error))
        row["sources"] = sorted(transport.used)
        rows.append(row)
    chart_row = next(row for row in rows if row["id"] == "nats-chart")
    if chart_row["selected"] is not None:
        try:
            chart = chart_row["selected"]
            url = raw_url(chart["source_repository"], chart["commit"], "helm/charts/nats/Chart.yaml")
            body = transport.data(url)
            fields = {}
            for field in ("version", "appVersion"):
                values = re.findall(r"^" + field + r":\s*[\"']?([^\s\"']+)[\"']?\s*$", body.decode(), re.M)
                need(len(values) == 1, "Ambiguous NATS chart metadata")
                fields[field] = values[0]
            need(fields["version"] == chart["version"].removeprefix("nats-") and fields["appVersion"] == selected["nats-server"]["version"].removeprefix("v"), "NATS chart and server versions are incompatible")
            chart["chart_metadata"] = dict(version=fields["version"], app_version=fields["appVersion"], url=url, sha256=hashlib.sha256(body).hexdigest())
            chart_row["sources"] = sorted(set(chart_row["sources"]) | {url})
        except (ValueError, KeyError, TypeError, OSError) as error:
            chart_row["blocked_reasons"].append("Chart compatibility unresolved: " + str(error))
    by_id = {r["id"]: r for r in rows}
    return [by_id[e["id"]] for e in registry["components"]], selected, packages.indexes


def exclusive_json(path, value):
    with Path(path).open("xb") as stream:
        stream.write(encode(value))


def arguments(argv):
    parser = argparse.ArgumentParser(description="Check official stable releases or prepare a separate exact candidate lock.")
    parser.add_argument("mode", choices=("check", "update"))
    for name in ("registry", "active-lock", "expected-lock-sha256", "report"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--output")
    parser.add_argument("--fixture-transport")
    args = parser.parse_args(argv)
    need((args.mode == "update") == (args.output is not None), "Only update requires --output")
    return args


def run(argv):
    args = arguments(argv)
    snapshot = load_inputs(args.registry, args.active_lock)
    registry, active, registry_hash, lock_hash = snapshot
    need(hex_string(args.expected_lock_sha256) and args.expected_lock_sha256 == lock_hash, "Expected active-lock hash is stale")
    project = Path(__file__).resolve().parents[2]
    external = [{"path": name, "sha256": sha(regular(project, name)), "state": "UNCHANGED"} for name in registry["external_lock_paths"]]
    protected = [Path(args.registry).resolve(), Path(args.active_lock).resolve()] + [project / r["path"] for r in external]
    if args.fixture_transport:
        protected.append(Path(args.fixture_transport).resolve())
    report_path = Path(args.report).absolute()
    outputs = [report_path] + ([Path(args.output).absolute()] if args.output else [])
    for path in outputs:
        need(path.parent.is_dir() and not path.exists() and not path.is_symlink(), "Output must be a new file in an existing directory")
        for ancestor in [path.parent, *path.parent.parents]:
            need(not ancestor.is_symlink(), "Output path contains a symlink")
        need(path.resolve() not in protected, "Output would overwrite a governed input")
    need(len({p.resolve() for p in outputs}) == len(outputs), "Report and candidate paths must differ")
    if args.output:
        need(Path(args.output).absolute().is_relative_to(report_path.parent), "Candidate must be within the report directory")
    transport = Transport(report_path.parent / (report_path.name + ".sources-" + uuid.uuid4().hex), args.fixture_transport)
    rows, selected, indexes = discover(registry, active, transport, args.mode)
    complete = all(not row["blocked_reasons"] and row["selected"] is not None for row in rows)
    need(sha(args.registry) == registry_hash and sha(args.active_lock) == lock_hash
         and all(sha(project / row["path"]) == row["sha256"] for row in external), "A governed input changed during discovery")
    report = dict(schema_version=1, contract="component-update-v1", mode=args.mode, synthetic=transport.synthetic,
                  registry_sha256=registry_hash, active_lock_before=lock_hash, active_lock_after=lock_hash,
                  complete=complete, observed_at=time.time(), components=rows, observations=list(transport.rows.values()), candidate=None,
                  generated_outputs=[dict(id=o["id"], owner_tasks=o["owner_tasks"], state="NOT_RUN") for o in registry["generated_outputs"]], external_locks=external)
    candidate_written = False
    try:
        if complete and args.mode == "update":
            profiles = copy.deepcopy(active["profiles"])
            for profile in profiles:
                profile.update(build="NOT_RUN", runtime="NOT_RUN")
            candidate = dict(schema_version=1, registry_sha256=registry_hash, previous_lock_sha256=lock_hash,
                             status="PREPARED_CANDIDATE", release_ready=False, active_lock_changed=False, synthetic=transport.synthetic,
                             components=selected, package_indexes=indexes, profiles=profiles,
                             pending_outputs=[dict(id=o["id"], status="PENDING_IMPLEMENTATION", runtime="NOT_RUN") for o in registry["generated_outputs"]])
            exclusive_json(args.output, candidate)
            candidate_written = True
            report["candidate"] = describe(report_path.parent, args.output)
        exclusive_json(report_path, report)
    except BaseException:
        if candidate_written:
            Path(args.output).unlink()
        raise
    return 0 if complete else 1


def main(argv=None):
    try:
        return run(sys.argv[1:] if argv is None else argv)
    except (ValueError, OSError, KeyError, TypeError, OverflowError) as error:
        print("Version request rejected: " + str(error), file=sys.stderr)
        return 2
