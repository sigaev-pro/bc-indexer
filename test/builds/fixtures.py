"""Independent synthetic protocol fixtures. These are not build evidence."""

import copy
import gzip
import hashlib
import json
from pathlib import Path
import urllib.parse

PROJECT = Path(__file__).resolve().parents[2]
REGISTRY = PROJECT / ".project/bootstrap/components/registry.json"
LOCK = PROJECT / ".project/bootstrap/components/candidate-lock.json"


def packed(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n").encode()


def hash_bytes(value):
    return hashlib.sha256(value).hexdigest()


class Files:
    def __init__(self, root):
        self.root = Path(root)
        self.count = 0

    def put(self, value):
        self.count += 1
        data = value if isinstance(value, bytes) else packed(value)
        name = "synthetic-" + str(self.count) + ".data"
        (self.root / name).write_bytes(data)
        return dict(path=name, sha256=hash_bytes(data), size_bytes=len(data))


def manifest(plan, files):
    def artifact(label):
        return files.put(("SYNTHETIC ONLY: " + label).encode())
    value = dict(schema_version=1, task_id="E01-01.builds", contract="build-artifacts-v1", evidence_kind="synthetic-fixture",
                 plan=plan, plan_sha256=hash_bytes(packed(plan)), identity=dict(run_id="independent-test", operation_id="build-test", nonce="3" * 64,
                 head_sha="4" * 40, base_sha="5" * 40, verifier_source_commit="6" * 40), builds={}, module_closures={}, os_closures={}, image_inputs={},
                 host_tools={name: artifact(name) for name in ("docker", "git", "python", "github_cli")}, runtime="NOT_RUN")
    for name, pin in plan["images"].items():
        value["image_inputs"][name] = pin | {k: artifact(name + k) for k in ("index", "manifest", "config", "inspection", "archive")}
    for name in plan["module_components"]:
        value["module_closures"][name] = dict(source_commit=plan["sources"][name]["commit"],
            **{k: artifact(name + k) for k in ("source_archive", "modules", "downloads", "verification")}, local_modules=files.put({}),
            artifacts=[dict(module="example.test/" + name, version="v1.0.0", **{k: artifact(name + k) for k in ("zip", "mod", "info")})])
    for name in plan["native_components"]:
        value["builds"][name] = dict(source_commit=plan["sources"][name]["commit"], compiler_image=plan["images"]["golang-build-image"]["reference"],
            **{k: artifact(name + k) for k in ("binary", "build_info", "imports", "dynamic_links", "oci_layout", "oci_index", "oci_manifest", "oci_config", "oci_layer", "version_output")},
            runtime_packages=[], repeat_build="NOT_RUN")
    for name in ("golang-build-image", "debian-runtime-image"):
        value["os_closures"][name] = dict(image_reference=plan["images"][name]["reference"], installed=artifact(name + "installed"),
            signed_indexes=[dict(url=p["url"], **{k: artifact(name + k + p["url"]) for k in ("inrelease", "release", "packages", "signature_log", "keyring", "verifier")}) for p in plan["package_indexes"]],
            packages=[dict(name="synthetic-lib", version="1.0-1", architecture="amd64", index_url=plan["package_indexes"][0]["url"], archive=artifact(name + "deb"))])
    value["profile_bindings"] = []
    for p in plan["profiles"]:
        binary = lambda name: value["builds"][name]["binary"]["sha256"]
        l1 = p["supporting_l1"]
        value["profile_bindings"].append(p | dict(native_binary_sha256=binary(p["native_component"]), consensus_binary_sha256=binary(p["consensus_component"]) if p["consensus_component"] else None,
            supporting_l1_binary_sha256={k: binary(l1[k]) for k in ("execution_component", "consensus_component")} if l1 else None))
    value["updater"] = dict(**{k: artifact("updater " + k) for k in ("report", "candidate", "check_report", "independent_report", "signature_proof")},
                            active_lock_before=plan["lock_sha256"], active_lock_after=plan["lock_sha256"], live_sources=True)
    value["cleanup"] = files.put(dict(synthetic=True, owned_resources=[], remaining_resources=[]))
    value["commands"] = [dict(argv=["synthetic-command"], env={"GOTOOLCHAIN": "local"}, exit_code=0, started_at=100, finished_at=101, stdout=artifact("stdout"), stderr=artifact("stderr"))]
    return value


class OfficialFixture:
    """Invent source data from the public registry, without oracle inputs."""
    def __init__(self, root):
        self.root, self.files, self.rows = Path(root), Files(root), {}
        self.registry = json.loads(REGISTRY.read_bytes())
        self.active = json.loads(LOCK.read_bytes())
        self.generate()

    def add(self, url, data, headers=None, status=200):
        self.rows[url] = dict(url=url, status=status, headers=headers or {}, observed_at=1234567890,
                              body=self.files.put(data))

    def save(self):
        path = self.root / "transport.json"
        path.write_bytes(packed(dict(schema_version=1, synthetic=True, observations=list(self.rows.values()))))
        return path

    def generate(self):
        versions = {}
        for entry in self.registry["components"]:
            if entry["pin_kind"] != "git_source":
                continue
            source, name = entry["source"], entry["id"]
            version = "nats-3.2.1" if name == "nats-chart" else "cmd/protoc-gen-go-grpc/v3.2.1" if name == "protoc-gen-go-grpc" else "v3.2.1"
            versions[name] = version
            if source["kind"] == "git_module_tags":
                self.add(source["url"], [{"ref": "refs/tags/" + version}, {"ref": "refs/tags/v99.0.0"}])
            else:
                self.add(source["url"], [dict(tag_name=version, draft=False, prerelease=False), dict(tag_name="v99.0.0-rc1", draft=False, prerelease=True), dict(tag_name="v100.0.0", draft=True, prerelease=False)])
            commit = hashlib.sha1((source["repository"] + version).encode()).hexdigest()
            tag_url = "https://api.github.com/repos/" + source["repository"] + "/git/ref/tags/" + urllib.parse.quote(version, safe="")
            self.add(tag_url, {"object": {"type": "commit", "sha": commit}})
            for dep in self.active["components"][name].get("dependency_inputs", []):
                url = "https://raw.githubusercontent.com/" + source["repository"] + "/" + commit + "/" + dep["path"]
                content = ("// SYNTHETIC " + name + " " + dep["path"] + "\n").encode()
                if dep["path"] == "go.mod":
                    content += b"module example.test/synthetic\n\nreplace (\n github.com/cometbft/cometbft => github.com/0xPolygon/cometbft v3.2.1\n github.com/cosmos/cosmos-sdk => github.com/0xPolygon/cosmos-sdk v3.2.1\n)\n"
                self.add(url, content)
            if name == "nats-chart":
                self.add("https://raw.githubusercontent.com/" + source["repository"] + "/" + commit + "/helm/charts/nats/Chart.yaml", b"version: 3.2.1\nappVersion: 3.2.1\n")
        # Independent pagination and annotated-tag controls.
        geth = next(e for e in self.registry["components"] if e["id"] == "geth")["source"]
        next_url = geth["url"] + "&page=2"
        self.add(next_url, [dict(tag_name="v3.2.1", draft=False, prerelease=False)])
        self.add(geth["url"], [dict(tag_name="v1.0.0", draft=False, prerelease=False)], {"link": '<' + next_url + '>; rel="next"'})
        tag = "https://api.github.com/repos/ethereum/go-ethereum/git/ref/tags/v3.2.1"
        commit = hashlib.sha1(b"ethereum/go-ethereumv3.2.1").hexdigest()
        self.add(tag, {"object": {"type": "tag", "sha": "a" * 40}})
        self.add("https://api.github.com/repos/ethereum/go-ethereum/git/tags/" + "a" * 40, {"object": {"type": "commit", "sha": commit}})
        downloads = []
        for suffix, arch, kind in (("src", "", "source"), ("linux-amd64", "amd64", "archive"), ("linux-arm64", "arm64", "archive")):
            name = "go3.2.1." + suffix + ".tar.gz"
            payload = ("SYNTHETIC GO " + suffix).encode()
            downloads.append(dict(filename=name, os="linux" if arch else "", arch=arch, kind=kind, sha256=hash_bytes(payload), size=len(payload)))
            self.add("https://go.dev/dl/" + name, payload)
        self.add("https://go.dev/dl/?mode=json", [dict(version="go3.2.1", stable=True, files=downloads), dict(version="go99.0rc1", stable=False, files=[])])
        for entry in self.registry["components"]:
            if entry["pin_kind"] != "oci_image":
                continue
            source = entry["source"]
            repository = source["repository"].removeprefix("docker.io/")
            version = {"golang-build-image": "3.2.1-trixie", "nats-runtime-image": "3.2.1-alpine", "debian-runtime-image": "13.4-slim"}[entry["id"]]
            self.add(source["url"], dict(name=repository, tags=["latest", version]))
            manifests = []
            prefix = "https://registry-1.docker.io/v2/" + repository + "/manifests/"
            for arch in ("amd64", "arm64"):
                data = packed(dict(schemaVersion=2, synthetic=repository + arch))
                digest = "sha256:" + hash_bytes(data)
                self.add(prefix + digest, data)
                manifests.append(dict(platform=dict(os="linux", architecture=arch), digest=digest))
            self.add(prefix + version, dict(schemaVersion=2, manifests=manifests))
        packages = [e["source"]["package"] for e in self.registry["components"] if e["pin_kind"] == "distribution_package"]
        for index in self.active["package_indexes"]:
            url = index["url"]
            arch = "amd64" if "binary-amd64" in url else "arm64"
            base, rest = url.split("/dists/")
            channel = rest.split("/")[0]
            blocks = []
            for package in packages:
                payload = ("SYNTHETIC DEB " + package + arch).encode()
                filename = "pool/test/" + package + "_2.0-1_" + arch + ".deb"
                self.add(base + "/" + filename, payload)
                blocks.append("\n".join(["Package: " + package, "Version: 2.0-1", "Architecture: " + arch, "Filename: " + filename, "SHA256: " + hash_bytes(payload), "Size: " + str(len(payload))]))
            raw = ("\n\n".join(blocks) + "\n\n").encode()
            if url.endswith(".xz"):
                import lzma
                compressed = lzma.compress(raw)
            else:
                compressed = gzip.compress(raw, mtime=0)
            self.add(url, compressed)
            self.add(base + "/dists/" + channel + "/InRelease", b"SYNTHETIC UNSIGNED INDEX CONTROL\n")
