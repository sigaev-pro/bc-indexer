"""Independent official-source selection checks for the standalone updater."""

import copy
import datetime
import functools
import gzip
import hashlib
import io
import json
import lzma
import math
from pathlib import Path
import re
import urllib.parse

import build_contract as contract
import build_materials as materials


UPDATE_CONTRACT = "component-update-v1"
REPORT_KEYS = {"schema_version", "contract", "mode", "synthetic", "registry_sha256", "active_lock_before", "active_lock_after",
               "complete", "observed_at", "components", "observations", "candidate", "generated_outputs", "external_locks"}
ROW_KEYS = {"id", "current", "latest_stable", "selected", "compatibility", "notices", "blocked_reasons", "sources"}
OBSERVATION_KEYS = {"url", "status", "headers", "body", "observed_at"}


def stable(value, vendor=False):
    if not isinstance(value, str):
        return None
    tail = value.rsplit("/", 1)[-1].removeprefix("nats-").removeprefix("go").removeprefix("v")
    match = re.fullmatch(r"(\d+)\.(\d+)(?:\.(\d+))?" + (r"(?:-polygon(?:[.-]\d+)*)?" if vendor else ""), tail)
    return tuple(int(part or 0) for part in match.groups()) if match else None


def debian_compare(left, right):
    """Debian's epoch/upstream/revision ordering, including tilde and epochs."""
    def split(value):
        epoch, separator, rest = value.partition(":")
        if separator:
            contract.require(epoch.isdigit(), "invalid Debian epoch")
        else:
            epoch, rest = "0", value
        upstream, separator, revision = rest.rpartition("-")
        return int(epoch), upstream if separator else rest, revision if separator else "0"
    def part(a, b):
        def order(char):
            return -1 if char == "~" else 0 if not char else ord(char) if char.isalpha() and char.isascii() else ord(char) + 256
        while a or b:
            while a and not a[0].isdigit() or b and not b[0].isdigit():
                ca = a[0] if a and not a[0].isdigit() else ""
                cb = b[0] if b and not b[0].isdigit() else ""
                if order(ca) != order(cb):
                    return 1 if order(ca) > order(cb) else -1
                a, b = a[1:] if ca else a, b[1:] if cb else b
            ma, mb = re.match(r"\d*", a).group(), re.match(r"\d*", b).group()
            na, nb = ma.lstrip("0"), mb.lstrip("0")
            if len(na) != len(nb):
                return 1 if len(na) > len(nb) else -1
            if na != nb:
                return 1 if na > nb else -1
            a, b = a[len(ma):], b[len(mb):]
        return 0
    le, lu, lr = split(left)
    re_, ru, rr = split(right)
    return (1 if le > re_ else -1) if le != re_ else part(lu, ru) or part(lr, rr)


class Observations:
    def __init__(self, root, rows, now, synthetic):
        self.root, self.rows, self.used = Path(root), {}, set()
        contract.require(type(rows) is list and 1 <= len(rows) <= 4096, "updater observation set is missing or unbounded")
        for row in rows:
            contract.require(type(row) is dict and set(row) == OBSERVATION_KEYS and isinstance(row["url"], str)
                             and row["url"] not in self.rows and type(row["status"]) is int
                             and type(row["headers"]) is dict and all(isinstance(k, str) and isinstance(v, str) for k, v in row["headers"].items())
                             and type(row["observed_at"]) in (int, float) and math.isfinite(row["observed_at"]), "invalid updater source observation")
            parsed = urllib.parse.urlsplit(row["url"])
            contract.require(parsed.scheme == "https" and parsed.username is None and parsed.password is None
                             and parsed.hostname in {"api.github.com", "raw.githubusercontent.com", "go.dev", "dl.google.com",
                                                    "registry-1.docker.io", "deb.debian.org", "security.debian.org"}, "updater observed a nonofficial source")
            if not synthetic:
                contract.require(now - 3600 <= row["observed_at"] <= now + 300, "updater source observation is stale or from the future")
            contract.verify_reference(self.root, row["body"])
            self.rows[row["url"]] = row

    def file(self, url):
        row = self.rows.get(url)
        contract.require(row is not None and row["status"] == 200, "required official source is unavailable: " + url)
        self.used.add(url)
        return contract.verify_reference(self.root, row["body"])

    def data(self, url):
        path = self.file(url)
        contract.require(path.stat().st_size <= 64 * 1024 * 1024, "official metadata is too large")
        return path.read_bytes()

    def json(self, url):
        return contract.parse_json(self.data(url))

    def pages(self, url):
        initial, seen, result = urllib.parse.urlsplit(url), set(), []
        named = re.fullmatch(r"/repos/([^/]+/[^/]+)(/.*)", initial.path) if initial.netloc == "api.github.com" else None
        metadata_url = "https://api.github.com/repos/" + named[1] if named else None
        alias = None

        def repository_alias():
            metadata = self.json(metadata_url)
            contract.require(type(metadata) is dict and type(metadata.get("id")) is int and metadata["id"] > 0
                             and metadata.get("full_name") == named[1] and metadata.get("url") == metadata_url,
                             "official repository metadata has an invalid or different identity")
            return "/repositories/" + str(metadata["id"]) + named[2]

        if metadata_url in self.rows:
            alias = repository_alias()
        while url:
            parsed = urllib.parse.urlsplit(url)
            contract.require(len(seen) < 64 and parsed.scheme == initial.scheme
                             and parsed.netloc == initial.netloc,
                             "official pagination loops or changes source identity")
            if parsed.path != initial.path and named and alias is None:
                alias = repository_alias()
            contract.require(parsed.path == initial.path or (alias is not None and parsed.path == alias),
                             "official pagination loops or changes source identity")
            identity = (initial.path, tuple(sorted(urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)))) if named else url
            contract.require(identity not in seen, "official pagination loops or changes source identity")
            seen.add(identity)
            body = self.json(url)
            contract.require(type(body) is list, "official release or tag page is not a list")
            result.extend(body)
            header = self.rows[url]["headers"].get("link", "")
            next_urls = re.findall(r'<([^>]+)>;\s*rel="next"', header)
            contract.require(len(next_urls) <= 1, "official pagination has ambiguous next pages")
            url = next_urls[0] if next_urls else None
        return result

    def image_tags(self, url, repository):
        initial, seen, tags = urllib.parse.urlsplit(url), set(), []
        while url:
            parsed = urllib.parse.urlsplit(url)
            contract.require(url not in seen and len(seen) < 64 and parsed.scheme == initial.scheme
                             and parsed.netloc == initial.netloc and parsed.path == initial.path, "image tag pagination changed source identity")
            seen.add(url)
            body = self.json(url)
            contract.require(body.get("name") == repository and type(body.get("tags")) is list
                             and all(isinstance(tag, str) for tag in body["tags"]), "official image tag listing belongs to another repository")
            tags.extend(body["tags"])
            next_urls = re.findall(r'<([^>]+)>;\s*rel="next"', self.rows[url]["headers"].get("link", ""))
            contract.require(len(next_urls) <= 1, "image tag pagination is ambiguous")
            url = urllib.parse.urljoin(url, next_urls[0]) if next_urls else None
        return tags


def latest(entry, observations):
    source = entry["source"]
    if source["kind"] == "github_releases":
        rows = observations.pages(source["url"])
        eligible = []
        for row in rows:
            contract.require(type(row.get("draft")) is bool and type(row.get("prerelease")) is bool, "release stability flags are malformed")
            version = row.get("tag_name")
            score = stable(version, vendor=source["channel"] == "upstream-bundle")
            if row["draft"] or row["prerelease"] or score is None:
                continue
            if "tag_filter" in source and not re.fullmatch(source["tag_filter"], version):
                continue
            eligible.append((score, version))
        contract.require(eligible, "official release source has no selectable stable release")
        return max(eligible)[1]
    if source["kind"] == "git_module_tags":
        prefix = "refs/tags/cmd/protoc-gen-go-grpc/"
        eligible = [(stable(row["ref"]), row["ref"].removeprefix("refs/tags/")) for row in observations.pages(source["url"])
                    if isinstance(row.get("ref"), str) and row["ref"].startswith(prefix) and stable(row["ref"]) is not None]
        contract.require(eligible, "generator source has no stable namespaced module tag")
        return max(eligible)[1]
    if source["kind"] == "official_downloads":
        rows = observations.json(source["url"])
        contract.require(type(rows) is list, "Go release source is not a list")
        eligible = [(stable(row.get("version")), row["version"]) for row in rows
                    if row.get("stable") is True and stable(row.get("version")) is not None]
        contract.require(eligible, "official Go source has no stable release")
        return max(eligible)[1]
    raise ValueError("source requires its package or component-specific selection rule")


def resolve_tag(repository, version, observations):
    pseudo = re.fullmatch(r"v\d+\.\d+\.\d+-(?:0\.)?(\d{14})-([a-f0-9]{12})(?:\+incompatible)?", version)
    if pseudo:
        row = observations.json("https://api.github.com/repos/" + repository + "/commits/" + pseudo.group(2))
        commit = row["sha"]
        date = datetime.datetime.fromisoformat(row["commit"]["committer"]["date"].replace("Z", "+00:00"))
        contract.require(contract.COMMIT.fullmatch(commit) and commit.startswith(pseudo.group(2))
                         and date.astimezone(datetime.timezone.utc).strftime("%Y%m%d%H%M%S") == pseudo.group(1),
                         "native-owned pseudo-version does not bind its commit and timestamp")
        return commit
    url = "https://api.github.com/repos/" + repository + "/git/ref/tags/" + urllib.parse.quote(version, safe="")
    target = observations.json(url)["object"]
    seen = set()
    while target["type"] == "tag":
        sha = target["sha"]
        contract.require(contract.COMMIT.fullmatch(sha) and sha not in seen and len(seen) < 8, "annotated tag chain is invalid")
        seen.add(sha)
        target = observations.json("https://api.github.com/repos/" + repository + "/git/tags/" + sha)["object"]
    contract.require(target["type"] == "commit" and contract.COMMIT.fullmatch(target["sha"]), "selected tag did not resolve to one immutable commit")
    return target["sha"]


def owner_replacement(identifier, selected, observations):
    owner = identifier.split("-", 1)[0]
    native = selected[owner]
    source_url = "https://raw.githubusercontent.com/" + native["source_repository"] + "/" + native["commit"] + "/go.mod"
    source = observations.data(source_url).decode()
    dependency = "github.com/cometbft/cometbft" if identifier.endswith("cometbft") else "github.com/cosmos/cosmos-sdk"
    expected_repo = "github.com/0xPolygon/" + ("cometbft" if identifier.endswith("cometbft") else "cosmos-sdk")
    pattern = r"^\s*(?:replace\s+)?" + re.escape(dependency) + r"(?:\s+v\S+)?\s*=>\s*" + re.escape(expected_repo) + r"\s+(\S+)\s*(?://[^\n]*)?$"
    matches = re.findall(pattern, source, re.M)
    contract.require(len(matches) == 1, "native-owned fork has no unique replacement in its owning native go.mod")
    return owner, matches[0]


def package_records(candidate, observations):
    indexes = candidate.get("package_indexes")
    contract.require(type(indexes) is list and len(indexes) == 6, "candidate needs all six architecture/channel package indexes")
    records, bindings, channels = {}, [], set()
    wanted = {row["package"] for row in candidate["components"].values() if "package" in row}
    for row in indexes:
        url, digest = row["url"], row["sha256"]
        match = re.fullmatch(r"https://(deb\.debian\.org/debian|security\.debian\.org/debian-security)/dists/(trixie|trixie-security|trixie-updates)/main/binary-(amd64|arm64)/Packages\.(gz|xz)", url)
        contract.require(match is not None and contract.HEX.fullmatch(digest), "candidate package index has an unsupported channel or format")
        channel, architecture = match.group(2), match.group(3)
        contract.require((channel, architecture) not in channels and ((channel == "trixie-security") == match.group(1).startswith("security.")),
                         "candidate package index channel or architecture is duplicated or crossed")
        channels.add((channel, architecture))
        payload = observations.data(url)
        contract.require(hashlib.sha256(payload).hexdigest() == digest, "candidate package index hash differs from actual official bytes")
        decoded = gzip.decompress(payload) if match.group(4) == "gz" else lzma.decompress(payload)
        for record in materials.fields(io.StringIO(decoded.decode())):
            if record.get("Package") in wanted and {"Package", "Version", "Architecture", "Filename", "SHA256", "Size"} <= set(record):
                key = (record["Package"], architecture)
                records.setdefault(key, []).append((record, url))
        base = url.split("/dists/", 1)[0]
        inrelease_url = base + "/dists/" + channel + "/InRelease"
        observations.file(inrelease_url)
        bindings.append({"index_url": url, "index_sha256": digest, "index_size": len(payload), "inrelease_url": inrelease_url})
    contract.require(channels == {(channel, arch) for channel in ("trixie", "trixie-security", "trixie-updates") for arch in ("amd64", "arm64")},
                     "candidate package source coverage is incomplete")
    return records, bindings


def validate_report(report, root, registry, active, registry_hash, lock_hash, mode, synthetic, now):
    contract.require(type(report) is dict and set(report) == REPORT_KEYS and type(report["schema_version"]) is int
                     and report["schema_version"] == 1 and report["contract"] == UPDATE_CONTRACT and report["mode"] == mode
                     and report["synthetic"] is synthetic and report["registry_sha256"] == registry_hash
                     and report["active_lock_before"] == report["active_lock_after"] == lock_hash
                     and type(report["complete"]) is bool, "updater report identity, mode, or active-lock binding differs")
    contract.require(type(report["observed_at"]) in (int, float) and math.isfinite(report["observed_at"])
                     and (synthetic or now - 3600 <= report["observed_at"] <= now + 300), "updater report time is invalid or stale")
    entries = {row["id"]: row for row in registry["components"]}
    rows = report["components"]
    contract.require(type(rows) is list and len(rows) == 41 and {row["id"] for row in rows} == set(entries), "updater does not cover all 41 components exactly once")
    for row in rows:
        contract.require(type(row) is dict and set(row) == ROW_KEYS and row["compatibility"] == "NOT_RUN"
                         and all(type(row[key]) is list and all(isinstance(value, str) and value for value in row[key])
                                 for key in ("notices", "blocked_reasons", "sources")), "updater component row is malformed or claims unproved compatibility")
        current = active["components"][row["id"]]
        contract.require(row["current"] == current.get("version", current.get("version_by_platform")), "updater current version differs from the active lock")
    expected_outputs = [{"id": row["id"], "owner_tasks": row["owner_tasks"], "state": "NOT_RUN"} for row in registry["generated_outputs"]]
    contract.require(report["generated_outputs"] == expected_outputs, "updater transferred or omitted generated-output gates")
    contract.require(type(report["external_locks"]) is list and {row["path"] for row in report["external_locks"]} == set(registry["external_lock_paths"])
                     and len(report["external_locks"]) == len(registry["external_lock_paths"])
                     and all(set(row) == {"path", "sha256", "state"} and contract.HEX.fullmatch(row["sha256"]) and row["state"] == "UNCHANGED"
                             for row in report["external_locks"]), "updater omitted or promoted a separately governed external lock")
    observations = Observations(root, report["observations"], now, synthetic)
    if not report["complete"]:
        contract.require(report["candidate"] is None and any(row["blocked_reasons"] for row in rows), "incomplete source discovery cannot emit a complete candidate")
        return {"complete": False, "bindings": [], "observations": observations}
    contract.require(all(not row["blocked_reasons"] and row["selected"] is not None for row in rows), "complete updater report contains unresolved components")
    selected = {row["id"]: row["selected"] for row in rows}
    if mode == "update":
        candidate = contract.parse_json(contract.verify_reference(root, report["candidate"]).read_bytes())
    else:
        contract.require(report["candidate"] is None, "check must not create a candidate lock")
        # A check exposes its proposed pins and source indexes through the same
        # component objects. Package pins carry all index hashes they use.
        indexes = {item["url"]: item for row in selected.values() for item in row.get("package_indexes", [])}
        candidate = {"components": selected, "package_indexes": list(indexes.values())}
    if mode == "update":
        contract.require(candidate.get("schema_version") == 1 and type(candidate.get("schema_version")) is int
                         and candidate.get("registry_sha256") == registry_hash and candidate.get("previous_lock_sha256") == lock_hash
                         and candidate.get("status") == "PREPARED_CANDIDATE" and candidate.get("release_ready") is False
                         and candidate.get("active_lock_changed") is False and candidate.get("synthetic") is synthetic
                         and candidate.get("components") == selected, "updater candidate is unbound, incomplete, or promoted")
        profiles = copy.deepcopy(active["profiles"])
        for profile in profiles:
            profile.update(build="NOT_RUN", runtime="NOT_RUN")
        pending = [{"id": row["id"], "status": "PENDING_IMPLEMENTATION", "runtime": "NOT_RUN"} for row in registry["generated_outputs"]]
        contract.require(candidate.get("profiles") == profiles and candidate.get("pending_outputs") == pending,
                         "updater changed profiles or transferred earlier generated/runtime results")
    records, bindings = package_records(candidate, observations)
    for row in rows:
        identifier, item, entry = row["id"], row["selected"], entries[row["id"]]
        source = entry["source"]
        contract.require(item.get("compatibility") == "NOT_RUN", "selected pin claims compatibility before gates")
        if entry["pin_kind"] == "git_source":
            newest = latest(entry, observations)
            contract.require(row["latest_stable"] == newest, "updater selected an incomplete or unstable release listing")
            version = newest
            if source["channel"] == "upstream-bundle":
                owner, version = owner_replacement(identifier, selected, observations)
                contract.require(item.get("owner_component") == owner, "updater lost the native bundle owner")
            contract.require(item["version"] == version and item["source_repository"] == source["repository"]
                             and item["commit"] == resolve_tag(source["repository"], version, observations), "updater source version, repository, or peeled commit differs")
            required_inputs = {dep["path"] for dep in active["components"][identifier].get("dependency_inputs", [])}
            inputs = item.get("dependency_inputs", [])
            contract.require({dep["path"] for dep in inputs} == required_inputs and len(inputs) == len(required_inputs), "updater dropped a dependency input from a component")
            for dep in inputs:
                url = "https://raw.githubusercontent.com/" + source["repository"] + "/" + item["commit"] + "/" + dep["path"]
                contract.require(dep["url"] == url and hashlib.sha256(observations.data(url)).hexdigest() == dep["sha256"],
                                 "updater dependency bytes are mutable, substituted, or incorrectly hashed")
        elif entry["pin_kind"] == "artifact_set":
            newest = latest(entry, observations)
            contract.require(item["version"] == row["latest_stable"] == newest, "Go candidate is not the highest observed stable version")
            release = next(value for value in observations.json(source["url"]) if value["version"] == newest)
            files = [value for value in release["files"] if value.get("kind") == "source"
                     or value.get("os") == "linux" and value.get("arch") in {"amd64", "arm64"} and value.get("kind") == "archive"]
            artifacts = {value["name"]: value for value in item["artifacts"]}
            contract.require(len(files) == 3 and len(artifacts) == 3 and set(artifacts) == {value["filename"] for value in files}, "Go candidate archive coverage differs")
            for official in files:
                artifact = artifacts[official["filename"]]
                contract.require(artifact["url"] == "https://go.dev/dl/" + official["filename"]
                                 and artifact["sha256"] == official["sha256"] and artifact["size_bytes"] == official["size"], "Go archive differs from official checksum metadata")
                if mode == "update":
                    actual = observations.file(artifact["url"])
                    contract.require(contract.file_hash(actual) == artifact["sha256"] and actual.stat().st_size == artifact["size_bytes"], "Go candidate archive was not materialized exactly")
        elif entry["pin_kind"] == "oci_image":
            tags = observations.image_tags(source["url"], source["repository"].removeprefix("docker.io/"))
            if identifier == "golang-build-image":
                version = selected["go"]["version"].removeprefix("go") + "-trixie"
            elif identifier == "nats-runtime-image":
                version = selected["nats-server"]["version"].removeprefix("v") + "-alpine"
            else:
                versions = [(int(match.group(1)), tag) for tag in tags if isinstance(tag, str)
                            and (match := re.fullmatch(r"13\.(\d+)-slim", tag))]
                contract.require(versions, "official Debian image source has no stable Debian 13 slim image")
                version = max(versions)[1]
            contract.require(version in tags and item["version"] == row["latest_stable"] == version
                             and item["image"] == source["repository"] and item["pull_reference"] == item["image"] + "@" + item["digest"], "image candidate is floating or mismatched with its selected component")
            url = "https://registry-1.docker.io/v2/" + source["repository"].removeprefix("docker.io/") + "/manifests/" + version
            index = observations.data(url)
            contract.require(item["digest"] == "sha256:" + hashlib.sha256(index).hexdigest(), "image tag did not resolve to the selected immutable index")
            parsed = contract.parse_json(index)
            platforms = {"linux/" + value["platform"]["architecture"]: value["digest"] for value in parsed["manifests"]
                         if value.get("platform", {}).get("os") == "linux" and value["platform"]["architecture"] in {"amd64", "arm64"}}
            contract.require(set(platforms) == set(contract.PLATFORMS) and item["platform_manifests"] == platforms, "image candidate omits or substitutes a required platform")
            for digest in platforms.values():
                payload = observations.data(url.rsplit("/", 1)[0] + "/" + digest)
                contract.require("sha256:" + hashlib.sha256(payload).hexdigest() == digest, "image platform manifest bytes differ")
        elif entry["pin_kind"] == "distribution_package":
            contract.require(item["package"] == source["package"] and set(item["version_by_platform"]) == set(contract.PLATFORMS), "package candidate identity differs")
            expected_versions = {}
            for platform in contract.PLATFORMS:
                choices = records.get((source["package"], platform.split("/")[1]), [])
                contract.require(choices, "official signed package indexes omit a required platform package")
                official, index_url = max(choices, key=functools.cmp_to_key(lambda a, b: debian_compare(a[0]["Version"], b[0]["Version"])))
                expected_versions[platform] = official["Version"]
                artifacts = [value for value in item["artifacts"] if value["platform"] == platform]
                expected_url = index_url.split("/dists/", 1)[0] + "/" + official["Filename"]
                contract.require(len(artifacts) == 1 and artifacts[0]["version"] == official["Version"]
                                 and artifacts[0]["url"] == expected_url and artifacts[0]["sha256"] == official["SHA256"], "package candidate revision or archive hash differs from its index")
                if mode == "update":
                    payload = observations.file(expected_url)
                    contract.require(contract.file_hash(payload) == official["SHA256"] and payload.stat().st_size == int(official["Size"]), "selected package archive was not materialized exactly")
            contract.require(item["version_by_platform"] == row["latest_stable"] == expected_versions, "package selection is not the latest exact stable revision for each platform")
        else:
            raise ValueError("unhandled registered updater component")
    chart = selected["nats-chart"]
    chart_url = "https://raw.githubusercontent.com/" + chart["source_repository"] + "/" + chart["commit"] + "/helm/charts/nats/Chart.yaml"
    chart_bytes = observations.data(chart_url)
    chart_fields = {}
    for key in ("version", "appVersion"):
        values = re.findall(r"^" + key + r":\s*[\"']?([^\s\"']+)[\"']?\s*$", chart_bytes.decode(), re.M)
        contract.require(len(values) == 1, "NATS chart has no unique version metadata")
        chart_fields[key] = values[0]
    contract.require(chart_fields["version"] == chart["version"].removeprefix("nats-")
                     and chart_fields["appVersion"] == selected["nats-server"]["version"].removeprefix("v")
                     and chart.get("chart_metadata") == {"version": chart_fields["version"], "app_version": chart_fields["appVersion"],
                                                        "url": chart_url, "sha256": hashlib.sha256(chart_bytes).hexdigest()},
                     "NATS chart is not compatible with its selected server metadata")
    # Check declared provenance after every required source has been authenticated.
    for row in rows:
        contract.require(row["sources"] and set(row["sources"]) <= observations.used, "component source references are missing or unverified")
    return {"complete": True, "bindings": bindings, "observations": observations, "selected": selected}
