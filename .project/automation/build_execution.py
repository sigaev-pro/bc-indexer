"""Fixed commands for the reviewed build plan; no candidate command execution."""

import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import tarfile
import time
import urllib.parse
import urllib.request
import uuid

import build_contract as contract
import build_materials as materials
import build_resources
import evidence


NAMES = {"geth": "geth", "bsc": "geth", "bor": "bor", "prysm": "beacon-chain", "heimdall": "heimdalld"}
TARGETS = {"geth": "./cmd/geth", "bsc": "./cmd/geth", "bor": "./cmd/cli/main.go",
           "prysm": "./cmd/beacon-chain", "heimdall": "./cmd/heimdalld"}
MAX_FETCH = 2 * 1024 ** 3


def official_url(value):
    parsed = urllib.parse.urlsplit(value)
    contract.require(parsed.scheme == "https" and parsed.username is None and parsed.password is None
                     and parsed.hostname in {"registry-1.docker.io", "auth.docker.io", "deb.debian.org", "security.debian.org",
                                              "api.github.com", "raw.githubusercontent.com", "go.dev", "dl.google.com"},
                     "build acquisition requires an approved official HTTPS source")
    return parsed


class Runner:
    def __init__(self, root, source, candidate, request_path, plan):
        self.root, self.source, self.candidate = (Path(path).resolve() for path in (root, source, candidate))
        self.request_path = Path(request_path).resolve()
        self.request = contract.parse_json(self.request_path.read_bytes())
        self.spec = build_resources.validate(self.request["owned_docker"])
        self.inventory = self.request_path.parent / "docker-resources.json"
        build_resources.read_inventory(self.spec, self.inventory)
        self.plan = contract.validate_plan(plan, self.source)
        contract.require(set(self.spec["images"]) == {row["reference"] for row in plan["images"].values()},
                         "supervised images differ from the accepted build plan")
        self.deadline = self.request["deadline"]
        self.monotonic_deadline = time.monotonic() + max(0, self.deadline - time.time())
        self.commands, self.cleanups, self.fetches = [], [], []
        self.package_fetches, self.package_journal_reference = [], None
        self.package_fetches_sealed = False
        self.cleaning = False
        self.root.mkdir(mode=0o700, parents=True, exist_ok=False)
        (self.root / "logs").mkdir()

    def remaining(self, requested=3600):
        available = min(requested, self.plan["limits"]["command_seconds"], self.deadline - time.time(),
                        self.monotonic_deadline - time.monotonic())
        contract.require(available > 0 and not Path(self.request["stop_file"]).exists(), "build deadline or stop forbids more work")
        return available

    def run(self, argv, output=None, env=None, timeout=3600, check=True):
        number = len(self.commands)
        stdout_path = Path(output) if output else self.root / "logs" / (str(number) + ".stdout")
        stderr_path = self.root / "logs" / (str(number) + ".stderr")
        stdout_path.parent.mkdir(parents=True, exist_ok=True)
        started = time.time()
        process_env = {**os.environ, **(env or {})}
        bound = min(timeout, 30) if self.cleaning else self.remaining(timeout)
        row = {"argv": argv, "env": env or {}, "exit_code": None, "started_at": started, "finished_at": None}
        self.commands.append(row)
        evidence.atomic_json(self.root / "command-history.json", self.commands)
        try:
            with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
                try:
                    result = subprocess.run(argv, cwd=self.root, stdout=stdout, stderr=stderr, env=process_env,
                                            timeout=bound, stdin=subprocess.DEVNULL)
                    row["exit_code"] = result.returncode
                finally:
                    for stream in (stdout, stderr):
                        stream.flush()
                        os.fsync(stream.fileno())
        except BaseException as error:
            row["error"] = type(error).__name__ + ": " + str(error)
            raise
        finally:
            row.update(finished_at=time.time())
            for key, path in (("stdout", stdout_path), ("stderr", stderr_path)):
                if path.is_file():
                    row[key] = contract.reference(self.root, path)
            evidence.atomic_json(self.root / "command-history.json", self.commands)
        if check:
            contract.require(result.returncode == 0, "fixed build command failed; retained command " + str(number))
        return result.returncode, stdout_path, stderr_path

    def control(self, arguments):
        code, stdout, stderr = self.run(["docker", *arguments], timeout=30, check=False)
        contract.require(stdout.stat().st_size + stderr.stat().st_size <= 1024 * 1024, "Docker control output exceeded its bound")
        return subprocess.CompletedProcess(arguments, code, stdout.read_text(), stderr.read_text())

    def fetch(self, url, output, headers=None, expected_sha256=None, maximum=MAX_FETCH, retry_package_archive=False):
        if retry_package_archive:
            contract.require(not headers, "package archive recovery does not accept authentication headers")
            return self.fetch_package(url, output, expected_sha256, maximum)
        official_url(url)
        output = Path(output)
        output.parent.mkdir(parents=True, exist_ok=True)
        started = time.time()
        request = urllib.request.Request(url, headers={"User-Agent": "extractor-build-verifier/1", **(headers or {})})
        with urllib.request.urlopen(request, timeout=self.remaining(60)) as response, output.open("xb") as stream:
            # Docker blob redirects use content delivery hosts. No credentials are
            # sent to those hosts by this helper: only metadata requests use auth.
            if response.url != url:
                official_url(response.url)
            total = 0
            while block := response.read(1024 * 1024):
                self.remaining()
                total += len(block)
                contract.require(total <= maximum, "official source payload exceeded its bound")
                stream.write(block)
            stream.flush()
            os.fsync(stream.fileno())
            status = response.status
            response_headers = {key.lower(): value for key, value in response.headers.items()
                                if key.lower() in {"content-type", "docker-content-digest", "link", "etag", "last-modified"}}
        ref = contract.reference(self.root, output)
        contract.require(status == 200 and (expected_sha256 is None or ref["sha256"] == expected_sha256),
                         "official source payload differs from its immutable hash")
        self.fetches.append({"url": url, "status": status, "headers": response_headers, "body": ref,
                             "started_at": started, "finished_at": time.time()})
        evidence.atomic_json(self.root / "source-observations.json", self.fetches)
        return output

    def write_package_journal(self):
        path = self.root / "package-fetch-journal.json"
        if self.package_journal_reference is not None:
            contract.verify_reference(self.root, self.package_journal_reference)
        else:
            contract.require(not path.exists(), "unexpected package transport journal already exists")
        value = {"schema_version": 1, "requests": self.package_fetches}
        evidence.atomic_json(path, value)
        reference = contract.reference(self.root, path)
        contract.require(reference["sha256"] == contract.digest(value), "package transport journal changed during persistence")
        self.package_journal_reference = reference

    def fetch_package(self, url, output, digest, size):
        materials.package_archive_url(url)
        contract.require(not self.package_fetches_sealed and isinstance(digest, str) and contract.HEX.fullmatch(digest)
                         and type(size) is int and 0 < size <= MAX_FETCH, "package recovery requires an unsealed exact archive pin")
        output = Path(output)
        expected = contract.reference_descriptor({"path": output.relative_to(self.root).as_posix(), "sha256": digest, "size_bytes": size})
        contract.require(not output.exists() and not output.is_symlink(), "package output already exists")
        output.parent.mkdir(parents=True, exist_ok=True)
        record = {"request_id": uuid.uuid4().hex, "url": url, "output": expected, "outcome": "IN_PROGRESS", "attempts": []}
        self.package_fetches.append(record)
        directory = self.root / "package-fetches" / record["request_id"]
        directory.mkdir(parents=True)
        for ordinal in (1, 2):
            timeout = self.remaining(60)
            attempt = {"ordinal": ordinal, "state": "STARTED", "started_at": time.time(), "finished_at": None,
                       "response_url": None, "status": None, "headers": {}, "error": None, "body": None,
                       "exception_partial_size": 0, "retained_exception_partial_size": 0,
                       "observed_body_size": 0, "body_truncated": False}
            record["attempts"].append(attempt)
            record["outcome"] = "IN_PROGRESS"
            self.write_package_journal()  # Intent must be durable before network dispatch.
            path = directory / (str(ordinal) + ".body")
            transport_error = None
            try:
                with path.open("xb") as stream:
                    try:
                        request = urllib.request.Request(url, headers={"User-Agent": "extractor-build-verifier/1"})
                        try:
                            timeout = self.remaining(60)
                            response = urllib.request.urlopen(request, timeout=timeout)
                        except Exception as error:
                            if isinstance(error, urllib.error.HTTPError):
                                attempt.update(response_url=error.url, status=error.code,
                                    headers={key.lower(): value for key, value in (error.headers or {}).items()
                                             if key.lower() in {"content-type", "docker-content-digest", "link", "etag", "last-modified"}})
                            transport_error = error
                            raise
                        with response:
                            attempt.update(response_url=response.url, status=response.status,
                                headers={key.lower(): value for key, value in response.headers.items()
                                         if key.lower() in {"content-type", "docker-content-digest", "link", "etag", "last-modified"}})
                            official_url(response.url)
                            contract.require(type(response.status) is int and response.status == 200, "package archive response did not succeed")
                            total = 0
                            while True:
                                self.remaining()
                                try:
                                    block = response.read(1024 * 1024)
                                except Exception as error:
                                    transport_error = error
                                    partial = getattr(error, "partial", None)
                                    if not isinstance(partial, bytes) and isinstance(error, urllib.error.URLError):
                                        partial = getattr(error.reason, "partial", None)
                                    # A wrapper can expose the same bytes as its cause. Retain one sequence.
                                    if isinstance(partial, bytes):
                                        attempt["exception_partial_size"] = len(partial)
                                        attempt["observed_body_size"] += len(partial)
                                        attempt["body_truncated"] = len(partial) > size - total
                                        retained = stream.write(partial[:size - total])
                                        attempt["retained_exception_partial_size"] = retained
                                        contract.require(retained == len(partial), "package exception bytes exceeded its exact size")
                                    raise
                                if not block:
                                    break
                                attempt["observed_body_size"] += len(block)
                                attempt["body_truncated"] = len(block) > size - total
                                retained = stream.write(block[:size - total])
                                total += retained
                                contract.require(retained == len(block), "package archive exceeded its exact size")
                    finally:
                        stream.flush()
                        os.fsync(stream.fileno())
                attempt["body"] = contract.reference(self.root, path)
                contract.require(attempt["body"]["sha256"] == digest and attempt["body"]["size_bytes"] == size,
                                 "package archive differs from its exact size or SHA-256")
                os.link(path, output)  # Keep the attempt bytes and publish without overwriting.
            except Exception as error:
                cause = error.reason if isinstance(error, urllib.error.URLError) else error
                retryable = (error is transport_error and type(error) in {ssl.SSLEOFError, urllib.error.URLError}
                             and type(cause) is ssl.SSLEOFError
                             and getattr(cause, "reason", None) == "UNEXPECTED_EOF_WHILE_READING")
                attempt.update(state="FAILED", finished_at=time.time(), error={"type": type(error).__name__,
                    "wrapped_type": type(cause).__name__ if cause is not error else None,
                    "ssl_reason": "UNEXPECTED_EOF_WHILE_READING" if retryable else None, "retryable": retryable})
                if path.is_file():
                    attempt["body"] = contract.reference(self.root, path)
                record["outcome"] = "FAILED"
                self.write_package_journal()  # Failure to retain evidence forbids another dispatch.
                if retryable and ordinal == 1:
                    continue
                raise
            attempt.update(state="SUCCEEDED", finished_at=time.time())
            record["outcome"] = "FIRST_ATTEMPT_SUCCESS" if ordinal == 1 else "RECOVERED_TRANSPORT"
            self.write_package_journal()
            self.fetches.append({"url": url, "status": 200, "headers": attempt["headers"], "body": contract.reference(self.root, output),
                                 "started_at": attempt["started_at"], "finished_at": attempt["finished_at"]})
            evidence.atomic_json(self.root / "source-observations.json", self.fetches)
            return output

    def seal_package_fetches(self, closures):
        contract.require(not self.package_fetches_sealed and self.package_journal_reference is not None,
                         "package transport journal is missing or already sealed")
        journal = contract.verify_reference(self.root, self.package_journal_reference)
        expected = {"schema_version": 1, "requests": self.package_fetches}
        contract.require(journal.read_bytes() == contract.canonical(expected), "package transport journal differs from trusted memory")
        materials.package_fetch_journal(expected, self.root)
        originals = {}
        for closure in closures:
            for index in closure["signed_indexes"]:
                status = materials.signature_status(index, self.root, self.commands)
                contract.require("archive_fetches" not in status, "OS signature log already has transport evidence")
                originals[index["signature_log"]["path"]] = (status, dict(index["signature_log"]))
        # Extend immutable logs only after validating every previously captured binding.
        sealed = self.root / "package-fetches" / "sealed.json"
        contract.require(not sealed.exists(), "sealed package journal already exists")
        evidence.atomic_json(sealed, expected)
        reference = contract.reference(self.root, sealed)
        replacements = {}
        for number, (path, (status, original)) in enumerate(originals.items()):
            output = self.root / "os" / ("transport-signature-" + str(number) + ".json")
            contract.require(not output.exists(), "extended OS signature log already exists")
            evidence.atomic_json(output, {**status, "archive_fetches": reference, "original_signature_log": original})
            replacements[path] = contract.reference(self.root, output)
        for closure in closures:
            closure["signed_indexes"] = [{**index, "signature_log": replacements[index["signature_log"]["path"]]}
                                         for index in closure["signed_indexes"]]
        self.package_fetches_sealed = True
        return reference

    def container(self, step, image, source=None, network="none", package_cache=None, temporary_bytes=None):
        limits = self.plan["limits"]
        options = ["--platform", self.plan["platform"], "--cpus", str(limits["cpus"]), "--memory", str(limits["memory_bytes"]),
                   "--memory-swap", str(limits["memory_bytes"]), "--pids-limit", str(limits["pids"]),
                   "--network", network, "--security-opt", "no-new-privileges:true", "--cap-drop", "NET_RAW",
                   "--mount", "type=bind,src=" + str(self.root) + ",dst=/proof"]
        if source:
            options.extend(["--mount", "type=bind,src=" + str(source) + ",dst=/source,readonly", "--workdir", "/source"])
        if package_cache:
            options.extend(["--mount", "type=bind,src=" + str(package_cache) + ",dst=/var/cache/apt/archives"])
        if temporary_bytes is not None:
            options.extend(["--tmpfs", "/tmp:rw,nosuid,nodev,size=" + str(temporary_bytes)])
        remaining = max(1, int(self.remaining()))
        identifier = build_resources.create(self.spec, self.inventory, step, image, options,
                                            ["sleep", str(remaining)], self.control)
        self.run(["docker", "container", "start", identifier], timeout=30)
        return identifier

    def inside(self, identifier, argv, environment=None, output=None, timeout=3600):
        build_resources.inspect_owned(identifier, self.spec, self.control)
        env_args = [part for key, value in sorted((environment or {}).items()) for part in ("--env", key + "=" + value)]
        return self.run(["docker", "container", "exec", *env_args, identifier, *argv], output=output, timeout=timeout)

    def cleanup(self):
        self.cleaning = True
        try:
            report = build_resources.cleanup(self.spec, self.inventory, self.control)
        finally:
            self.cleaning = False
        self.cleanups.append(report)
        evidence.atomic_json(self.root / "cleanup-history.json", self.cleanups)
        build_resources.validate_cleanup(self.spec, report)
        return report

    def acquire_images(self):
        rows = {}
        accept = "application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json"
        for name, pin in self.plan["images"].items():
            target = self.root / "images" / name
            repository = pin["reference"].split("@", 1)[0].removeprefix("docker.io/")
            token_url = "https://auth.docker.io/token?service=registry.docker.io&scope=" + urllib.parse.quote("repository:" + repository + ":pull", safe="")
            with urllib.request.urlopen(token_url, timeout=self.remaining(30)) as response:
                token = contract.parse_json(response.read(1024 * 1024))["token"]
            # Registry bearer tokens are short-lived public pull tokens. Do not
            # retain one in a manifest or command argument.
            headers = {"Authorization": "Bearer " + token, "Accept": accept}
            base = "https://registry-1.docker.io/v2/" + repository
            index = self.fetch(base + "/manifests/" + pin["reference"].split("@", 1)[1], target / "index.json", headers,
                               pin["reference"].split("sha256:", 1)[1], 16 * 1024 * 1024)
            manifest = self.fetch(base + "/manifests/" + pin["platform_digest"], target / "manifest.json", headers,
                                  pin["platform_digest"].removeprefix("sha256:"), 16 * 1024 * 1024)
            body = contract.parse_json(manifest.read_bytes())
            self.run(["docker", "image", "pull", "--platform", self.plan["platform"], pin["reference"]], timeout=600)
            _, archive, _ = self.run(["docker", "image", "save", "--platform", self.plan["platform"], pin["reference"]], output=target / "image.tar", timeout=300)
            config = target / "config.json"
            with tarfile.open(archive, "r:") as saved:
                metadata = saved.extractfile("manifest.json")
                descriptors = contract.parse_json(metadata.read(1024 * 1024))
                contract.require(type(descriptors) is list and len(descriptors) == 1, "Docker saved an ambiguous image set")
                member = saved.getmember(descriptors[0]["Config"])
                contract.require(member.isfile() and member.size < 16 * 1024 * 1024, "Docker saved an unsafe image config")
                config.write_bytes(saved.extractfile(member).read())
            contract.require("sha256:" + contract.file_hash(config) == body["config"]["digest"], "saved image config differs from its selected OCI manifest")
            _, inspection, _ = self.run(["docker", "image", "inspect", pin["reference"]], output=target / "inspection.json", timeout=30)
            rows[name] = {**pin, **{key: contract.reference(self.root, value) for key, value in
                                  (("index", index), ("manifest", manifest), ("config", config), ("inspection", inspection), ("archive", archive))}}
            materials.image_input(rows[name], self.root, self.plan["platform"])
        return rows

    def archive_source(self, name, cache):
        pin = self.plan["sources"][name]
        cache = Path(cache).resolve()
        target = self.root / "sources" / name
        target.mkdir(parents=True)
        # The cache is only an object source. Its working tree, hooks, ignored
        # files, and local compiler never participate in a build.
        git_options = ["git", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.attributesFile=/dev/null",
                       "-c", "credential.helper="]
        if not cache.is_dir():
            cache = self.root / "source-cache" / name
            cache.parent.mkdir(parents=True, exist_ok=True)
            self.run([*git_options, "init", "--bare", str(cache)], timeout=30)
            self.run([*git_options, "-C", str(cache), "fetch", "--depth=1", "--no-tags",
                      "https://github.com/" + pin["repository"] + ".git", pin["commit"]], timeout=600)
        git = [*git_options, "-C", str(cache)]
        _, identity, _ = self.run([*git, "rev-parse", pin["commit"] + "^{commit}"], timeout=30)
        contract.require(identity.read_text().strip() == pin["commit"], "source cache lacks the selected immutable commit")
        _, archive, _ = self.run([*git, "archive", "--format=tar", pin["commit"]], output=target / "source.tar", timeout=120)
        materials.source_archive(archive, pin["dependency_inputs"])
        source = target / "tree"
        source.mkdir()
        with tarfile.open(archive, "r:") as stream:
            stream.extractall(source, filter="data")
        _, timestamp, _ = self.run([*git, "show", "-s", "--format=%ct", pin["commit"]], timeout=30)
        epoch = int(timestamp.read_text().strip())
        contract.require(0 < epoch <= time.time(), "source commit timestamp is invalid")
        return source, archive, epoch

    def prepare_module_metadata(self, identifier, archive):
        contract.require(identifier in contract.MODULES, "unknown native module metadata owner")
        target = self.root / "sources" / identifier / "metadata"
        target.mkdir()
        for name, data in materials.archived_module_metadata(archive).items():
            with (target / name).open("xb") as stream:
                stream.write(data)


def go_environment(identifier, platform, epoch):
    return {"GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off", "GOOS": "linux", "GOARCH": platform.split("/")[1],
            "CGO_ENABLED": "1", "GOPATH": "/proof/cache/" + identifier + "/gopath", "GOMODCACHE": "/proof/cache/" + identifier + "/mod",
            "GOCACHE": "/proof/cache/" + identifier + "/build", "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org",
            "GOPRIVATE": "", "GONOSUMDB": "", "GONOPROXY": "",
            "GOFLAGS": "-mod=readonly -modfile=/proof/sources/" + identifier + "/metadata/go.mod",
            "SOURCE_DATE_EPOCH": str(epoch), "TZ": "UTC", "LANG": "C.UTF-8"}


def build_arguments(identifier, pin, epoch, graph, cpus):
    commit, version = pin["commit"], pin["version"]
    flags = ["-trimpath", "-buildvcs=false", "-mod=readonly", "-p", str(cpus)]
    if identifier in {"geth", "bsc"}:
        date = datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).strftime("%Y%m%d")
        ldflags = "-X github.com/ethereum/go-ethereum/internal/version.gitCommit=" + commit
        ldflags += " -X github.com/ethereum/go-ethereum/internal/version.gitDate=" + date
        flags += ["-tags", "urfave_cli_no_docs"]
    elif identifier == "bor":
        ldflags = "-X github.com/ethereum/go-ethereum/params.GitCommit=" + commit
        flags += ["-pgo=prod.pprof"]
    elif identifier == "prysm":
        prefix = "github.com/OffchainLabs/prysm/v7/runtime/version."
        ldflags = " ".join("-X " + prefix + key + "=" + value for key, value in
                           (("gitCommit", commit), ("gitTag", version), ("buildDateUnix", str(epoch)),
                            ("buildDate", datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))))
    else:
        resolved = {row["Path"]: row.get("Replace", row).get("Version") for row in graph}
        cosmos, comet = resolved.get("github.com/cosmos/cosmos-sdk"), resolved.get("github.com/cometbft/cometbft")
        contract.require(cosmos and comet, "Heimdall must stamp versions from its own resolved Cosmos/CometBFT graph")
        stamps = {"github.com/0xPolygon/heimdall-v2/version.Name": "heimdall",
                  "github.com/0xPolygon/heimdall-v2/version.ServerName": "heimdalld",
                  "github.com/0xPolygon/heimdall-v2/version.Version": version.removeprefix("v"),
                  "github.com/0xPolygon/heimdall-v2/version.Commit": commit,
                  "github.com/cosmos/cosmos-sdk/version.Name": "heimdall",
                  "github.com/cosmos/cosmos-sdk/version.ServerName": "heimdalld",
                  "github.com/cosmos/cosmos-sdk/version.Version": cosmos,
                  "github.com/cometbft/cometbft/version.TMCoreSemVer": comet}
        ldflags = " ".join("-X " + key + "=" + value for key, value in stamps.items())
    return ["/usr/local/go/bin/go", "build", *flags, "-ldflags", ldflags,
            "-o", "/proof/binaries/" + identifier + "/" + NAMES[identifier], TARGETS[identifier]]


def oci_layout(runner, identifier, container, binary, epoch):
    target = runner.root / "oci" / identifier
    blobs = target / "blobs" / "sha256"
    blobs.mkdir(parents=True)
    _, exported, _ = runner.run(["docker", "container", "export", container], output=target / "rootfs.tar", timeout=300)
    layer = blobs / contract.file_hash(exported)
    os.link(exported, layer)
    config = {"architecture": runner.plan["platform"].split("/")[1], "os": "linux",
              "created": datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
              "config": {"Entrypoint": ["/usr/local/bin/" + NAMES[identifier]],
                         "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
                         "Labels": {"io.extractor.source-commit": runner.plan["sources"][identifier]["commit"],
                                    "io.extractor.lock-sha256": runner.plan["lock_sha256"],
                                    "io.extractor.base-image": runner.plan["images"]["debian-runtime-image"]["reference"]}},
              "rootfs": {"type": "layers", "diff_ids": ["sha256:" + layer.name]}}
    def blob(value):
        data = contract.canonical(value)
        path = blobs / hashlib.sha256(data).hexdigest()
        path.write_bytes(data)
        return path
    def descriptor(path, media_type):
        return {"mediaType": media_type, "digest": "sha256:" + contract.file_hash(path), "size": path.stat().st_size}
    config_path = blob(config)
    manifest = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                "config": descriptor(config_path, "application/vnd.oci.image.config.v1+json"),
                "layers": [descriptor(layer, "application/vnd.oci.image.layer.v1.tar")]}
    manifest_path = blob(manifest)
    index = {"schemaVersion": 2, "manifests": [{**descriptor(manifest_path, "application/vnd.oci.image.manifest.v1+json"),
                                               "platform": {"os": "linux", "architecture": runner.plan["platform"].split("/")[1]}}]}
    index_path = target / "index.json"
    index_path.write_bytes(contract.canonical(index))
    layout = target / "oci-layout"
    layout.write_bytes(contract.canonical({"imageLayoutVersion": "1.0.0"}))
    return {key: contract.reference(runner.root, path) for key, path in
            (("oci_layout", layout), ("oci_index", index_path), ("oci_manifest", manifest_path), ("oci_config", config_path), ("oci_layer", layer))}


def native_builds(runner, source_caches, authenticate_extra=None, package_source_report=None, package_source_report_sha256=None):
    from build_os import Packages
    packages = Packages(runner)
    packages.acquire(package_source_report, package_source_report_sha256)
    compiler_image = runner.plan["images"]["golang-build-image"]["reference"]
    runtime_image = runner.plan["images"]["debian-runtime-image"]["reference"]
    compiler_cache, runtime_cache = (runner.root / "apt-cache" / name for name in ("compiler", "runtime"))
    compiler_cache.mkdir(parents=True)
    runtime_cache.mkdir(parents=True)
    roots = [row["package"] + "=" + row["version"] for row in runner.plan["packages"].values()]
    closures, builds, epochs, runtime_roots = {}, {}, {}, set()
    compiler_closure = None
    for identifier in contract.MODULES:
        source, archive, epoch = runner.archive_source(identifier, Path(source_caches) / identifier)
        runner.prepare_module_metadata(identifier, archive)
        epochs[identifier] = epoch
        container = runner.container("go-" + identifier, compiler_image, source, network="bridge", package_cache=compiler_cache)
        try:
            if packages.signed is None:
                packages.authenticate(container)
                if authenticate_extra:
                    authenticate_extra(container)
            if identifier in contract.NATIVE:
                packages.install(container, compiler_cache, roots)
                observed = packages.inventory(container, compiler_image)
                if compiler_closure is None:
                    compiler_closure = observed
                else:
                    contract.require(observed["installed"]["sha256"] == compiler_closure["installed"]["sha256"],
                                     "native clients used different installed compiler OS closures")
            env = go_environment(identifier, runner.plan["platform"], epoch)
            _, version, _ = runner.inside(container, ["/usr/local/go/bin/go", "version"], environment=env, timeout=30)
            contract.require(version.read_text().strip() == "go version " + runner.plan["compiler"]["version"] + " " + runner.plan["platform"],
                             "selected image did not run the exact requested Go compiler and platform")
            _, downloads, _ = runner.inside(container, ["/usr/local/go/bin/go", "mod", "download", "-json", "all"], environment=env)
            _, modules, _ = runner.inside(container, ["/usr/local/go/bin/go", "list", "-m", "-json", "all"], environment=env)
            _, verification, _ = runner.inside(container, ["/usr/local/go/bin/go", "mod", "verify"], environment=env)
            graph = materials.json_stream(modules.read_text())
            local = materials.local_modules(graph, archive)
            local_path = archive.parent / "local-modules.json"
            local_path.write_bytes(contract.canonical(local))
            downloaded = materials.modules(graph, materials.json_stream(downloads.read_text()), materials.archive_module(archive), local)
            artifacts = []
            for (module, version), row in sorted(downloaded.items()):
                item = {"module": module, "version": version}
                for field, reported in (("zip", "Zip"), ("mod", "GoMod"), ("info", "Info")):
                    path = contract.safe_file(runner.root, row[reported].removeprefix("/proof/"))
                    item[field] = contract.reference(runner.root, path)
                artifacts.append(item)
            closures[identifier] = {"source_commit": runner.plan["sources"][identifier]["commit"], "artifacts": artifacts,
                **{key: contract.reference(runner.root, value) for key, value in
                   (("source_archive", archive), ("downloads", downloads), ("modules", modules),
                    ("local_modules", local_path), ("verification", verification))}}
            for name in ("go.mod", "go.sum"):
                runner.inside(container, ["cat", "/proof/sources/" + identifier + "/metadata/" + name], timeout=30)
            materials.module_metadata(identifier, closures[identifier], runner.root, runner.commands)
            if identifier in contract.NATIVE:
                (runner.root / "binaries" / identifier).mkdir(parents=True)
                arguments = build_arguments(identifier, runner.plan["sources"][identifier], epoch, graph, runner.plan["limits"]["cpus"])
                compile_env = {**env, "GOPROXY": "off", "GOSUMDB": "off"}
                if identifier == "prysm":
                    compile_env["CGO_CFLAGS"] = "-O2 -D__BLST_PORTABLE__"
                _, imports, _ = runner.inside(container, [arguments[0], "list", "-deps", "-json", *arguments[2:arguments.index("-o")], arguments[-1]],
                                                environment=compile_env)
                materials.imported_packages(materials.json_stream(imports.read_text()), materials.archive_module(archive), identifier, local)
                runner.inside(container, arguments, environment=compile_env)
                binary = runner.root / "binaries" / identifier / NAMES[identifier]
                _, info, _ = runner.inside(container, ["/usr/local/go/bin/go", "version", "-m", arguments[arguments.index("-o") + 1]],
                                             environment=env, timeout=30)
                dependencies, links = packages.runtime_requirements(container, arguments[arguments.index("-o") + 1], compiler_closure)
                runtime_roots.update(dependencies)
                builds[identifier] = {"source_commit": runner.plan["sources"][identifier]["commit"], "compiler_image": compiler_image, "repeat_build": "NOT_RUN",
                    "runtime_packages": [{"name": name, "version": version, "architecture": architecture} for name, version, architecture in sorted(dependencies)],
                    **{key: contract.reference(runner.root, value) for key, value in
                       (("binary", binary), ("build_info", info), ("imports", imports), ("dynamic_links", links))}}
            for expected in runner.plan["sources"][identifier]["dependency_inputs"]:
                contract.require(contract.file_hash(source / expected["path"]) == expected["sha256"], "Go changed a selected native dependency input")
            materials.module_metadata(identifier, closures[identifier], runner.root, runner.commands, builds.get(identifier))
        finally:
            runner.cleanup()
    runtime_closure = None
    for identifier in contract.NATIVE:
        container = runner.container("runtime-" + identifier, runtime_image, package_cache=runtime_cache, temporary_bytes=512 * 1024 ** 2)
        try:
            packages.install(container, runtime_cache, [name + "=" + version for name, version, _ in sorted(runtime_roots)])
            observed = packages.inventory(container, runtime_image)
            if runtime_closure is None:
                runtime_closure = observed
            else:
                contract.require(observed["installed"]["sha256"] == runtime_closure["installed"]["sha256"],
                                 "native output images have different resolved runtime closures")
            binary = contract.verify_reference(runner.root, builds[identifier]["binary"])
            runner.run(["docker", "container", "cp", str(binary), container + ":/usr/local/bin/" + NAMES[identifier]], timeout=30)
            runner.inside(container, ["chmod", "0755", "/usr/local/bin/" + NAMES[identifier]], timeout=30)
            version_args = ["version", "--long", "--output", "json", "--home", "/tmp/heimdall-version"] if identifier == "heimdall" else ["--version" if identifier == "prysm" else "version"]
            _, version, _ = runner.inside(container, ["/usr/local/bin/" + NAMES[identifier], *version_args], timeout=30)
            value = version.read_text()
            commit = runner.plan["sources"][identifier]["commit"]
            if identifier == "heimdall":
                info = contract.parse_json(value)
                contract.require(info.get("commit") == commit and info.get("version") == runner.plan["sources"][identifier]["version"].removeprefix("v")
                                 and info.get("name") == "heimdall" and info.get("server_name") == "heimdalld", "Heimdall version smoke did not expose its exact injected metadata")
            elif identifier != "prysm":
                contract.require(commit in value, "native version smoke did not expose its exact injected commit")
            else:
                contract.require(runner.plan["sources"][identifier]["version"] in value and commit in value
                                 and "Local build" not in value and "Moments ago" not in value,
                                 "Prysm version smoke did not expose its exact selected version and commit")
            builds[identifier]["version_output"] = contract.reference(runner.root, version)
            builds[identifier].update(oci_layout(runner, identifier, container, binary, epochs[identifier]))
        finally:
            runner.cleanup()
    runner.seal_package_fetches([compiler_closure, runtime_closure])
    return builds, closures, {"golang-build-image": compiler_closure, "debian-runtime-image": runtime_closure}
