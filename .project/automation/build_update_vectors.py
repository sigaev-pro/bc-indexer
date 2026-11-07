"""Synthetic official-source protocol fixtures; no acquired release or build claim."""

import copy
import gzip
import hashlib
from pathlib import Path
import urllib.parse

import build_contract as contract
import build_updates


FIXTURE_TIME = 946684800


def fixture(root, source):
    """Only raw source responses are written. Expected objects remain in memory."""
    root = Path(root)
    root.mkdir(parents=True, exist_ok=True)
    registry, active, registry_hash, lock_hash = contract.inputs(source)
    entries = {row["id"]: row for row in registry["components"]}
    selected, responses, newest = {}, {}, {}

    def add(url, value, status=200):
        data = value if isinstance(value, bytes) else contract.canonical(value)
        if url in responses:
            contract.require(responses[url]["bytes"] == data, "synthetic source URL has conflicting payloads")
            return
        responses[url] = {"bytes": data, "status": status, "headers": {}}

    for identifier, entry in entries.items():
        if entry["pin_kind"] != "git_source":
            continue
        item = copy.deepcopy(active["components"][identifier])
        source_info = entry["source"]
        version = "nats-88.2.0" if identifier == "nats-chart" else "cmd/protoc-gen-go-grpc/v88.2.0" if identifier == "protoc-gen-go-grpc" else "v88.2.0"
        commit = hashlib.sha1(((identifier if source_info["channel"] == "upstream-bundle" else source_info["repository"]) + "-synthetic-source").encode()).hexdigest()
        if source_info["channel"] == "upstream-bundle":
            version = "v0.0.0-20000101000000-" + commit[:12]
        item.update(version=version, commit=commit, compatibility="NOT_RUN")
        selected[identifier] = item
        newest[identifier] = "v88.2.0-polygon" if source_info["channel"] == "upstream-bundle" else version
        if source_info["kind"] == "git_module_tags":
            add(source_info["url"], [{"ref": "refs/tags/" + version, "object": {"type": "commit", "sha": commit}},
                                      {"ref": "refs/tags/cmd/protoc-gen-go-grpc/v99.0.0-rc.1", "object": {"type": "commit", "sha": "f" * 40}}])
        else:
            add(source_info["url"], [{"tag_name": newest[identifier], "draft": False, "prerelease": False,
                                       "body": "Synthetic stable release with no runtime compatibility claim."},
                                      {"tag_name": "v999.0.0", "draft": True, "prerelease": False},
                                      {"tag_name": "v999.0.0-rc.1", "draft": False, "prerelease": True}])
        if source_info["channel"] == "upstream-bundle":
            add("https://api.github.com/repos/" + source_info["repository"] + "/commits/" + commit[:12],
                {"sha": commit, "commit": {"committer": {"date": "2000-01-01T00:00:00Z"}}})
        else:
            tag = hashlib.sha1((identifier + "-synthetic-annotated-tag").encode()).hexdigest()
            # Shared repositories use the same tag object, just as the real Go
            # protobuf library and generator share one release source.
            tag = hashlib.sha1((source_info["repository"] + version + "-annotated").encode()).hexdigest()
            add("https://api.github.com/repos/" + source_info["repository"] + "/git/ref/tags/" + urllib.parse.quote(version, safe=""),
                {"object": {"type": "tag", "sha": tag}})
            add("https://api.github.com/repos/" + source_info["repository"] + "/git/tags/" + tag,
                {"object": {"type": "commit", "sha": commit}})
    for identifier, item in selected.items():
        for dep in item.get("dependency_inputs", []):
            if dep["path"].endswith("go.mod"):
                value = "module synthetic/" + item["source_repository"] + "\n\ngo 1.27.0\n"
                if identifier in {"bor", "heimdall"}:
                    value += "replace (\n"
                    for name, original in (("cometbft", "github.com/cometbft/cometbft"), ("cosmos-sdk", "github.com/cosmos/cosmos-sdk")):
                        value += " " + original + " => github.com/0xPolygon/" + name + " " + selected[identifier + "-" + name]["version"] + "\n"
                    value += ")\n"
                payload = value.encode()
            else:
                payload = b"synthetic/module v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
            url = "https://raw.githubusercontent.com/" + item["source_repository"] + "/" + item["commit"] + "/" + dep["path"]
            dep.update(url=url, sha256=hashlib.sha256(payload).hexdigest())
            add(url, payload)
    chart = selected["nats-chart"]
    chart_url = "https://raw.githubusercontent.com/" + chart["source_repository"] + "/" + chart["commit"] + "/helm/charts/nats/Chart.yaml"
    chart_payload = b"name: nats\nversion: 88.2.0\nappVersion: 88.2.0\n"
    add(chart_url, chart_payload)
    chart["chart_metadata"] = {"version": "88.2.0", "app_version": "88.2.0", "url": chart_url,
                               "sha256": hashlib.sha256(chart_payload).hexdigest()}
    go_version, go_files, go_artifacts = "go1.99.1", [], []
    for suffix, os_name, architecture, kind in (("src.tar.gz", "", "", "source"), ("linux-amd64.tar.gz", "linux", "amd64", "archive"),
                                                ("linux-arm64.tar.gz", "linux", "arm64", "archive")):
        name = go_version + "." + suffix
        payload = gzip.compress(("Synthetic archive only: " + name).encode(), mtime=0)
        digest = hashlib.sha256(payload).hexdigest()
        go_files.append({"filename": name, "os": os_name, "arch": architecture, "kind": kind, "sha256": digest, "size": len(payload)})
        go_artifacts.append({"name": name, "url": "https://go.dev/dl/" + name, "sha256": digest, "size_bytes": len(payload), "payload_verified": True})
        add("https://go.dev/dl/" + name, payload)
    add(entries["go"]["source"]["url"], [{"version": go_version, "stable": True, "files": go_files},
                                          {"version": "go1.999.0rc1", "stable": False, "files": []}])
    selected["go"] = {"version": go_version, "compatibility": "NOT_RUN", "artifacts": go_artifacts}
    newest["go"] = go_version
    for identifier in contract.IMAGES:
        entry = entries[identifier]
        version = {"golang-build-image": "1.99.1-trixie", "nats-runtime-image": "88.2.0-alpine", "debian-runtime-image": "13.99-slim"}[identifier]
        repository = entry["source"]["repository"]
        base = "https://registry-1.docker.io/v2/" + repository.removeprefix("docker.io/")
        add(entry["source"]["url"], {"name": repository.removeprefix("docker.io/"), "tags": [version, "latest", "99.0-rc1-slim"]})
        platforms, manifests = {}, []
        for platform in contract.PLATFORMS:
            payload = contract.canonical({"schemaVersion": 2, "synthetic": True, "component": identifier, "platform": platform})
            digest = "sha256:" + hashlib.sha256(payload).hexdigest()
            platforms[platform] = digest
            manifests.append({"digest": digest, "size": len(payload), "platform": {"os": "linux", "architecture": platform.split("/")[1]}})
            add(base + "/manifests/" + digest, payload)
        payload = contract.canonical({"schemaVersion": 2, "manifests": manifests})
        digest = "sha256:" + hashlib.sha256(payload).hexdigest()
        add(base + "/manifests/" + version, payload)
        selected[identifier] = {"version": version, "compatibility": "NOT_RUN", "image": repository, "digest": digest,
                                "pull_reference": repository + "@" + digest, "platform_manifests": platforms}
        newest[identifier] = version
    package_entries = [row for row in registry["components"] if row["pin_kind"] == "distribution_package"]
    indexes = []
    for channel in ("trixie", "trixie-security", "trixie-updates"):
        base = "https://security.debian.org/debian-security" if channel == "trixie-security" else "https://deb.debian.org/debian"
        add(base + "/dists/" + channel + "/InRelease", ("SYNTHETIC UNSIGNED INRELEASE: " + channel + "\n").encode())
        for architecture in ("amd64", "arm64"):
            records = []
            for entry in package_entries:
                package = entry["source"]["package"]
                version = "88.2-1+deb13u1"
                filename = "pool/main/s/synthetic/" + package + "_" + version + "_" + architecture + ".deb"
                payload = b"!<arch>\n" + ("synthetic: " + package + architecture).encode()
                digest = hashlib.sha256(payload).hexdigest()
                records.append("Package: " + package + "\nVersion: " + version + "\nArchitecture: " + architecture + "\nFilename: " + filename
                               + "\nSHA256: " + digest + "\nSize: " + str(len(payload)) + "\n")
                if channel == "trixie":
                    item = selected.setdefault(entry["id"], {"package": package, "compatibility": "NOT_RUN", "version_by_platform": {}, "artifacts": []})
                    item["version_by_platform"]["linux/" + architecture] = version
                    item["artifacts"].append({"platform": "linux/" + architecture, "version": version, "url": base + "/" + filename,
                                              "sha256": digest, "payload_verified": True})
                    add(base + "/" + filename, payload)
            url = base + "/dists/" + channel + "/main/binary-" + architecture + "/Packages.gz"
            payload = gzip.compress(("\n".join(records) + "\n").encode(), mtime=0)
            add(url, payload)
            indexes.append({"url": url, "sha256": hashlib.sha256(payload).hexdigest()})
    for entry in package_entries:
        selected[entry["id"]]["package_indexes"] = copy.deepcopy(indexes)
        newest[entry["id"]] = selected[entry["id"]]["version_by_platform"]
    candidate = copy.deepcopy(active)
    candidate.update(components=selected, package_indexes=indexes, previous_lock_sha256=lock_hash,
                     status="PREPARED_CANDIDATE", synthetic=True, release_ready=False, active_lock_changed=False)
    rows = []
    all_urls = list(responses)
    for entry in registry["components"]:
        identifier = entry["id"]
        current = active["components"][identifier]
        urls = [entry["source"]["url"]] if entry["source"]["url"] in responses else [indexes[0]["url"]]
        rows.append({"id": identifier, "current": current.get("version", current.get("version_by_platform")),
                     "latest_stable": newest[identifier], "selected": selected[identifier], "compatibility": "NOT_RUN",
                     "notices": ["Synthetic source protocol fixture; no release or build claim."], "blocked_reasons": [], "sources": urls})
    observations = []
    for url, value in responses.items():
        path = root / "responses" / hashlib.sha256(url.encode()).hexdigest()
        path.parent.mkdir(exist_ok=True)
        path.write_bytes(value["bytes"])
        observations.append({"url": url, "status": value["status"], "headers": value["headers"],
                             "body": contract.reference(root, path), "observed_at": FIXTURE_TIME})
    transport = root / "transport.json"
    transport.write_bytes(contract.canonical({"schema_version": 1, "synthetic": True, "observations": observations}))
    report = {"schema_version": 1, "contract": build_updates.UPDATE_CONTRACT, "mode": "check", "synthetic": True,
              "registry_sha256": registry_hash, "active_lock_before": lock_hash, "active_lock_after": lock_hash,
              "complete": True, "observed_at": FIXTURE_TIME, "components": rows, "observations": observations, "candidate": None,
              "generated_outputs": [{"id": row["id"], "owner_tasks": row["owner_tasks"], "state": "NOT_RUN"} for row in registry["generated_outputs"]],
              "external_locks": [{"path": name, "sha256": contract.file_hash(Path(source) / name), "state": "UNCHANGED"} for name in registry["external_lock_paths"]]}
    return {"transport": transport, "report": report, "candidate": candidate, "registry": registry, "active": active,
            "registry_hash": registry_hash, "lock_hash": lock_hash, "root": root}
