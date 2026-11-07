"""Acquire and install an exact signed Debian closure inside owned containers."""

import contextlib
import gzip
import hashlib
import lzma
import os
from pathlib import Path
import re
import shlex
import shutil
import stat
import time
import urllib.parse

import build_contract as contract
import build_materials as materials


MAX_SOURCE_REPORT = materials.MAX_PACKAGE_SOURCE_REPORT
MAX_PACKAGE_INDEX = 256 * 1024 * 1024
MAX_INRELEASE = 16 * 1024 * 1024


@contextlib.contextmanager
def source_directory(path):
    """Anchor retained inputs without following even an ancestor symlink."""
    path = Path(path).absolute()
    contract.require(".." not in path.parts, "package source directory contains parent traversal")
    descriptor = os.open(path.anchor, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in path.parts[1:]:
            next_descriptor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        yield descriptor
    finally:
        os.close(descriptor)


@contextlib.contextmanager
def source_file(directory, name, maximum):
    contract.require(isinstance(name, str) and "\\" not in name and not name.startswith("/")
                     and all(part not in {"", ".", ".."} for part in name.split("/")), "unsafe package source path")
    descriptor = os.dup(directory)
    try:
        parts = name.split("/")
        for part in parts[:-1]:
            next_descriptor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        file_descriptor = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=descriptor)
    finally:
        os.close(descriptor)
    with os.fdopen(file_descriptor, "rb") as stream:
        status = os.fstat(stream.fileno())
        contract.require(stat.S_ISREG(status.st_mode) and status.st_size <= maximum, "package source is not a bounded regular file")
        yield stream


def copy_source(directory, reference, output, maximum, remaining):
    contract.reference_descriptor(reference)
    contract.require(reference["size_bytes"] <= maximum, "package source exceeds its size bound")
    output.parent.mkdir(parents=True, exist_ok=True)
    digest, size = hashlib.sha256(), 0
    with source_file(directory, reference["path"], maximum) as source, output.open("xb") as target:
        while block := source.read(1024 * 1024):
            remaining()
            size += len(block)
            contract.require(size <= maximum, "package source grew beyond its bound")
            digest.update(block)
            target.write(block)
        contract.require(size == reference["size_bytes"] and digest.hexdigest() == reference["sha256"], "retained package source bytes changed")
        target.flush()
        os.fsync(target.fileno())


APT = ["apt-get", "-o", "Dir::Etc::sourcelist=/proof/config/sources.list", "-o", "Dir::Etc::sourceparts=-",
       "-o", "Acquire::Languages=none", "-o", "Acquire::By-Hash=false", "-o", "Acquire::Retries=0",
       "-o", "APT::Get::List-Cleanup=false"]


class Packages:
    def __init__(self, runner):
        self.runner = runner
        self.indexes, self.by_filename, self.by_identity = [], {}, {}
        self.signed = None
        self.acquired = False

    def retained(self, report_path, report_sha256):
        contract.require(isinstance(report_sha256, str) and contract.HEX.fullmatch(report_sha256), "invalid package source report digest")
        report_path = Path(report_path).absolute()
        with source_directory(report_path.parent) as directory:
            self.runner.remaining()
            with source_file(directory, report_path.name, MAX_SOURCE_REPORT) as stream:
                data = stream.read(MAX_SOURCE_REPORT + 1)
            contract.require(len(data) <= MAX_SOURCE_REPORT and hashlib.sha256(data).hexdigest() == report_sha256,
                             "package source report differs from its required digest or exceeds its bound")
            observations = materials.package_source_observations(data)
            pairs = [materials.package_source_pair(observations, pin["url"], pin["sha256"]) for pin in self.runner.plan["package_indexes"]]
            target = self.runner.root / "package-source-report.json"
            with target.open("xb") as output:
                output.write(data)
                output.flush()
                os.fsync(output.fileno())
            report = contract.reference(self.runner.root, target)
            result = []
            for number, (index, signed) in enumerate(pairs):
                packages, inrelease = self.paths(number, index["url"])
                copy_source(directory, index["body"], packages, MAX_PACKAGE_INDEX, self.runner.remaining)
                copy_source(directory, signed["body"], inrelease, MAX_INRELEASE, self.runner.remaining)
                result.append({"mode": "retained-report", "report": report,
                               "index_observation": index, "inrelease_observation": signed})
            return result

    def paths(self, number, url):
        packages = self.runner.root / "mirror" / ("repo-" + str(number)) / "dists" / url.split("/dists/", 1)[1]
        return packages, packages.parents[2] / "InRelease"

    def acquire(self, report_path=None, report_sha256=None):
        contract.require((report_path is None) == (report_sha256 is None), "package source report and digest must be supplied together")
        retained = self.retained(report_path, report_sha256) if report_path is not None else None
        lines = []
        for number, pin in enumerate(self.runner.plan["package_indexes"]):
            base, relative = pin["url"].split("/dists/", 1)
            distribution = relative.split("/", 1)[0]
            prefix = "mirror/repo-" + str(number)
            path, inrelease = self.paths(number, pin["url"])
            if retained is None:
                self.runner.fetch(pin["url"], path, expected_sha256=pin["sha256"], maximum=MAX_PACKAGE_INDEX)
                self.runner.fetch(base + "/dists/" + distribution + "/InRelease", inrelease, maximum=MAX_INRELEASE)
            self.indexes.append({"url": pin["url"], "sha256": pin["sha256"], "base": base, "prefix": prefix,
                                 "packages": path, "inrelease": inrelease,
                                 "acquisition": retained[number] if retained is not None else {"mode": "live-fetch"}})
            lines.append("deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] file:/proof/" + prefix + " " + distribution + " main")
            opener = gzip.open if str(path).endswith(".gz") else lzma.open
            with opener(path, "rt", encoding="utf-8") as stream:
                for record in materials.fields(stream):
                    if not {"Package", "Version", "Architecture", "Filename", "SHA256", "Size"} <= set(record):
                        continue
                    record = {key: record[key] for key in ("Package", "Version", "Architecture", "Filename", "SHA256", "Size")}
                    filename = record["Filename"]
                    contract.require(filename.startswith("pool/") and ".." not in Path(filename).parts
                                     and contract.HEX.fullmatch(record["SHA256"]) and record["Size"].isdigit(), "invalid signed package record")
                    selected = {"index": number, "record": record}
                    key = (record["Package"], record["Version"], record["Architecture"])
                    self.by_identity.setdefault(key, selected)
                    self.by_filename[(number, filename)] = selected
        config = self.runner.root / "config"
        config.mkdir()
        (config / "sources.list").write_text("\n".join(lines) + "\n")
        self.acquired = True

    def authenticate(self, container):
        contract.require(self.acquired, "package acquisition is incomplete")
        contract.require(self.signed is None, "signed package indexes were already authenticated")
        keyring = self.runner.root / "os" / "debian-archive-keyring.pgp"
        keyring.parent.mkdir(parents=True)
        self.runner.run(["docker", "container", "cp", container + ":/usr/share/keyrings/debian-archive-keyring.pgp", str(keyring)], timeout=30)
        verifier = self.runner.root / "os" / "sqv"
        self.runner.run(["docker", "container", "cp", container + ":/usr/bin/sqv", str(verifier)], timeout=30)
        result = []
        for number, item in enumerate(self.indexes):
            release = item["inrelease"].with_name("Release")
            self.runner.inside(container, ["/usr/bin/sqv", "--keyring=/usr/share/keyrings/debian-archive-keyring.pgp",
                    "--output=/proof/" + release.relative_to(self.runner.root).as_posix(), "--cleartext",
                    "/proof/" + item["inrelease"].relative_to(self.runner.root).as_posix()], timeout=30)
            materials.release(release.read_text(), item["url"], item["sha256"], item["packages"].stat().st_size, time.time())
            log = self.runner.root / "os" / ("signature-" + str(number) + ".json")
            log.write_bytes(contract.canonical({"tool": "sqv", "exit_code": 0, "command_index": len(self.runner.commands) - 1,
                "verifier_sha256": contract.file_hash(verifier), "keyring_sha256": contract.file_hash(keyring),
                "inrelease_sha256": contract.file_hash(item["inrelease"]), "release_sha256": contract.file_hash(release),
                "acquisition": item["acquisition"]}))
            result.append({"url": item["url"], **{key: contract.reference(self.runner.root, path) for key, path in
                          (("inrelease", item["inrelease"]), ("release", release), ("packages", item["packages"]),
                           ("signature_log", log), ("keyring", keyring), ("verifier", verifier))}})
        self.signed = result
        return result

    def archive(self, item):
        index, record = self.indexes[item["index"]], item["record"]
        path = self.runner.root / index["prefix"] / record["Filename"]
        if path.exists():
            contract.require(path.is_file() and not path.is_symlink() and path.stat().st_size == int(record["Size"])
                             and contract.file_hash(path) == record["SHA256"], "retained Debian package changed")
        else:
            self.runner.fetch(index["base"] + "/" + record["Filename"], path,
                              expected_sha256=record["SHA256"], maximum=max(1, int(record["Size"])))
        contract.require(path.stat().st_size == int(record["Size"]), "Debian package size differs from its signed index")
        return path

    def download_plan(self, text, cache):
        for line in text.splitlines():
            if not line.startswith("'"):
                continue
            fields = shlex.split(line)
            contract.require(len(fields) == 4 and fields[0].startswith("file:/proof/mirror/repo-")
                             and Path(fields[1]).name == fields[1] and fields[2].isdigit(), "APT returned an unexpected archive request")
            local = urllib.parse.unquote(urllib.parse.urlsplit(fields[0]).path).removeprefix("/proof/")
            prefix, filename = local.split("/pool/", 1)
            number = int(prefix.removeprefix("mirror/repo-"))
            selected = self.by_filename.get((number, "pool/" + filename))
            contract.require(selected is not None and int(selected["record"]["Size"]) == int(fields[2]),
                             "APT requested a package outside the exact signed indexes")
            archive = self.archive(selected)
            target = Path(cache) / fields[1]
            if target.exists():
                contract.require(contract.file_hash(target) == selected["record"]["SHA256"], "APT cache contains another archive")
            else:
                shutil.copyfile(archive, target)

    def install(self, container, cache, requested):
        contract.require(self.signed is not None, "package installation requires prior cryptographic verification")
        self.runner.inside(container, [*APT, "update"], timeout=180)
        # Preserve the pinned base. Install only the task's exact roots and their
        # resolved dependencies; inventory() still requires every base archive.
        for operation in (["install", *requested],) if requested else ():
            _, plan, _ = self.runner.inside(container, [*APT, "--yes", "--download-only", "--print-uris", *operation], timeout=180)
            self.download_plan(plan.read_text(), cache)
            self.runner.inside(container, [*APT, "--yes", "--no-download", *operation],
                               environment={"DEBIAN_FRONTEND": "noninteractive"}, timeout=600)

    def inventory(self, container, image_reference):
        _, output, _ = self.runner.inside(container, ["dpkg-query", "--show", "--showformat=${Package}\t${Version}\t${Architecture}\n"], timeout=30)
        installed = materials.installed(output.read_text(), self.runner.plan["platform"].split("/")[1])
        archives = []
        for key in sorted(installed):
            selected = self.by_identity.get(key)
            contract.require(selected is not None, "installed OS package has no archive in the selected signed indexes: " + repr(key))
            path = self.archive(selected)
            archives.append({"name": key[0], "version": key[1], "architecture": key[2],
                             "index_url": self.indexes[selected["index"]]["url"], "archive": contract.reference(self.runner.root, path)})
        return {"image_reference": image_reference, "installed": contract.reference(self.runner.root, output),
                "signed_indexes": self.signed, "packages": archives}

    def runtime_requirements(self, container, binary, installed):
        _, links, _ = self.runner.inside(container, ["ldd", binary], timeout=30)
        text = links.read_text()
        contract.require("not found" not in text, "compiled binary has an unresolved dynamic library")
        paths = sorted(set(re.findall(r"(?:=>\s*)?(/[A-Za-z0-9_./+-]+)", text)))
        required = set()
        available = {(row["name"], row["architecture"]): row["version"] for row in installed["packages"]}
        for path in paths:
            _, owners, _ = self.runner.inside(container, ["dpkg-query", "--search", "*/" + Path(path).name], timeout=30)
            found = False
            for line in owners.read_text().splitlines():
                owner, filename = line.split(": ", 1)
                if Path(filename).name != Path(path).name:
                    continue
                name, separator, architecture = owner.partition(":")
                architecture = architecture if separator else self.runner.plan["platform"].split("/")[1]
                version = available.get((name, architecture))
                if version:
                    required.add((name, version, architecture))
                    found = True
            contract.require(found, "dynamic library has no package in the resolved OS closure: " + path)
        return required, links
