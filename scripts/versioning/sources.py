"""Bounded official-source transport and stable version selection."""

import datetime
import functools
import hashlib
import math
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

from core import closed, decode, describe, hex_string, need, ref, sha

HOSTS = {"api.github.com", "raw.githubusercontent.com", "go.dev", "dl.google.com",
         "registry-1.docker.io", "deb.debian.org", "security.debian.org"}


def origin(url):
    parsed = urllib.parse.urlsplit(url)
    return parsed.scheme.lower(), parsed.hostname, parsed.port or (443 if parsed.scheme.lower() == "https" else 80)


def valid_token(value):
    # Permit bearer-token characters, never whitespace, controls or separators.
    return value if isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9._~+/=-]{1,4096}", value) else None


def github_credential(timeout_seconds):
    for name in ("GH_TOKEN", "GITHUB_TOKEN"):
        value = os.environ.get(name)
        if value:
            return valid_token(value)
    environment = dict(os.environ)
    for name in ("GH_TOKEN", "GITHUB_TOKEN"):
        environment.pop(name, None)
    environment.update(GH_HOST="github.com", GH_PROMPT_DISABLED="1", GH_NO_UPDATE_NOTIFIER="1")
    try:
        result = subprocess.run(["gh", "auth", "token", "--hostname", "github.com"],
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=min(5, timeout_seconds), env=environment, check=False)
        if result.returncode != 0 or len(result.stdout) > 4098:
            return None
        value = result.stdout.removesuffix(b"\n").removesuffix(b"\r").decode("ascii")
        return valid_token(value)
    except (OSError, subprocess.SubprocessError, UnicodeError):
        # A helper error may contain captured credentials. Never stringify it.
        return None


def official(url):
    parsed = urllib.parse.urlsplit(url)
    need(parsed.scheme == "https" and parsed.hostname in HOSTS and parsed.username is None
         and parsed.password is None and parsed.port in (None, 443) and not parsed.fragment, "Nonofficial source URL")
    return parsed


class Redirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, newurl):
        official(newurl)
        redirected = super().redirect_request(request, response, code, message, headers, newurl)
        if origin(newurl) != origin(request.full_url):
            redirected.remove_header("Authorization")
        return redirected


class Transport:
    def __init__(self, directory, fixture=None, deadline_seconds=1800):
        self.directory = Path(directory)
        self.directory.mkdir(parents=True, exist_ok=False)
        self.root = self.directory.parent
        self.synthetic = fixture is not None
        self.fixture = {}
        self.rows = {}
        self.used = set()
        self.deadline = time.monotonic() + deadline_seconds
        self.tokens = {}
        self.github_checked = False
        self.github_token = None
        self.opener = urllib.request.build_opener(Redirect())
        if fixture:
            path = Path(fixture)
            value = decode(path.read_bytes())
            closed(value, "schema_version synthetic observations", "Fixture transport")
            need(type(value["schema_version"]) is int and value["schema_version"] == 1 and value["synthetic"] is True,
                 "Fixture transport must be explicitly synthetic version 1")
            need(type(value["observations"]) is list and 1 <= len(value["observations"]) <= 4096, "Invalid fixture observation count")
            for row in value["observations"]:
                closed(row, "url status headers body observed_at", "Source observation")
                official(row["url"])
                need(row["url"] not in self.fixture and type(row["status"]) is int
                     and type(row["headers"]) is dict and all(isinstance(k, str) and isinstance(v, str) for k, v in row["headers"].items())
                     and type(row["observed_at"]) in (int, float) and math.isfinite(row["observed_at"]), "Invalid fixture observation")
                ref(path.parent, row["body"])
                self.fixture[row["url"]] = (path.parent, row)

    def timeout(self):
        remaining = self.deadline - time.monotonic()
        need(remaining > 0, "Official-source discovery deadline exceeded")
        return min(30, remaining)

    def fetch(self, url):
        parsed = official(url)
        if not self.synthetic and origin(url) == ("https", "api.github.com", 443) and not self.github_checked:
            self.github_token = github_credential(self.timeout())
            self.github_checked = True
        need(not self.contains_secret(url), "Official source URL contains credential data")
        self.used.add(url)
        if url in self.rows:
            return ref(self.root, self.rows[url]["body"])
        need(len(self.rows) < 4096, "Official-source observation limit exceeded")
        destination = self.directory / (hashlib.sha256(url.encode()).hexdigest() + ".body")
        status, headers, observed = 0, {}, time.time()
        try:
            if self.synthetic:
                need(url in self.fixture, "Synthetic transport has no response for " + url)
                fixture_root, row = self.fixture[url]
                source = ref(fixture_root, row["body"])
                status, headers, observed = row["status"], {k.lower(): v for k, v in row["headers"].items()}, row["observed_at"]
                with source.open("rb") as stream, destination.open("xb") as output:
                    self.copy(stream, output)
            else:
                request_headers = {"User-Agent": "extractor-component-updater/1", "Accept": "application/json"}
                if origin(url) == ("https", "api.github.com", 443) and self.github_token is not None:
                    request_headers["Authorization"] = "Bearer " + self.github_token
                if parsed.hostname == "registry-1.docker.io":
                    repository = parsed.path.split("/v2/", 1)[1].rsplit("/", 2)[0]
                    token = self.tokens.get(repository)
                    if token is None:
                        query = urllib.parse.urlencode(dict(service="registry.docker.io", scope="repository:" + repository + ":pull"))
                        with urllib.request.urlopen("https://auth.docker.io/token?" + query, timeout=self.timeout()) as response:
                            token = decode(response.read(1024 * 1024))["token"]
                        self.tokens[repository] = token
                    request_headers.update(Authorization="Bearer " + token, Accept="application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json")
                request = urllib.request.Request(url, headers=request_headers)
                try:
                    response = self.opener.open(request, timeout=self.timeout())
                except urllib.error.HTTPError as error:
                    response = error
                with response, destination.open("xb") as output:
                    status = response.status
                    headers = {k.lower(): v for k, v in response.headers.items()}
                    need(not any(self.contains_secret(k) or self.contains_secret(v) for k, v in headers.items()),
                         "Official response exposed credential data")
                    headers = {k: v for k, v in headers.items() if k not in {"authorization", "proxy-authorization", "set-cookie"}}
                    self.copy(response, output)
        except (ValueError, OSError, urllib.error.URLError) as error:
            if destination.exists():
                destination.unlink()
            detail = str(error) if self.synthetic else type(error).__name__
            destination.write_bytes(("Source acquisition failed: " + detail).encode())
            headers = {}
            status = 0
        self.rows[url] = dict(url=url, status=status, headers=headers, body=describe(self.root, destination), observed_at=observed)
        need(status == 200, "Official source unavailable (status " + str(status) + "): " + url)
        return destination

    def secret_values(self):
        return [value for value in [self.github_token, *self.tokens.values()] if isinstance(value, str) and value]

    def contains_secret(self, value):
        return any(secret in value for secret in self.secret_values())

    def copy(self, source, target):
        total = 0
        secrets = [value.encode() for value in self.secret_values()]
        overlap = max((len(value) - 1 for value in secrets), default=0)
        tail = b""
        while True:
            self.timeout()
            block = source.read(1024 * 1024)
            if not block:
                return
            total += len(block)
            need(total <= 1024**3, "Official artifact exceeds the 1 GiB acquisition limit")
            scan = tail + block
            need(not any(secret in scan for secret in secrets), "Official response exposed credential data")
            tail = scan[-overlap:] if overlap else b""
            target.write(block)

    def data(self, url):
        path = self.fetch(url)
        need(self.rows[url]["status"] == 200, "Official source returned a failed observation")
        need(path.stat().st_size <= 64 * 1024**2, "Official metadata exceeds 64 MiB")
        return path.read_bytes()

    def json(self, url):
        return decode(self.data(url))

    def pages(self, url, repository=None):
        initial = official(url)
        seen, values = set(), []
        while url:
            target = official(url)
            need(url not in seen and len(seen) < 64 and (target.scheme, target.netloc, target.path) == (initial.scheme, initial.netloc, initial.path), "Invalid official pagination")
            seen.add(url)
            body = self.json(url)
            if repository is not None:
                need(type(body) is dict and body.get("name") == repository, "Image tag source belongs to another repository")
                body = body.get("tags")
            need(type(body) is list, "Official listing is not an array")
            values.extend(body)
            links = re.findall(r'<([^>]+)>;\s*rel="next"', self.rows[url]["headers"].get("link", ""))
            need(len(links) <= 1, "Ambiguous pagination")
            url = urllib.parse.urljoin(url, links[0]) if links else None
        return values


def version_key(text, vendor=False):
    if not isinstance(text, str):
        return None
    tail = text.rsplit("/", 1)[-1]
    for prefix in ("nats-", "go", "v"):
        if tail.startswith(prefix):
            tail = tail[len(prefix):]
    pattern = r"([0-9]+)\.([0-9]+)(?:\.([0-9]+))?"
    if vendor:
        pattern += r"(?:-polygon(?:[.-][0-9]+)*)?"
    match = re.fullmatch(pattern, tail)
    return tuple(int(v or 0) for v in match.groups()) if match else None


def select_release(entry, transport):
    source = entry["source"]
    kind = source["kind"]
    candidates = []
    rows = transport.json(source["url"]) if kind == "official_downloads" else transport.pages(source["url"])
    need(type(rows) is list, "Official release listing is not an array")
    for row in rows:
        need(type(row) is dict, "Malformed release entry")
        if kind == "official_downloads":
            version = row.get("version")
            if row.get("stable") is not True:
                continue
        elif kind == "git_module_tags":
            tag = row.get("ref", "")
            if not isinstance(tag, str) or not tag.startswith("refs/tags/cmd/protoc-gen-go-grpc/"):
                continue
            version = tag.removeprefix("refs/tags/")
        else:
            need(type(row.get("draft")) is bool and type(row.get("prerelease")) is bool, "Malformed release stability flags")
            if row["draft"] or row["prerelease"]:
                continue
            version = row.get("tag_name")
        score = version_key(version, source["channel"] == "upstream-bundle")
        if score is not None and ("tag_filter" not in source or re.fullmatch(source["tag_filter"], version)):
            candidates.append((score, version))
    need(candidates, "Official source has no stable selectable release")
    return max(candidates)[1]


def commit_for(repository, version, transport):
    prefix = "https://api.github.com/repos/" + repository
    pseudo = re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+-(?:0\.)?([0-9]{14})-([a-f0-9]{12})(?:\+incompatible)?", version)
    if pseudo:
        row = transport.json(prefix + "/commits/" + pseudo[2])
        commit = row["sha"]
        date = datetime.datetime.fromisoformat(row["commit"]["committer"]["date"].replace("Z", "+00:00"))
        need(hex_string(commit, 40) and commit.startswith(pseudo[2]) and date.astimezone(datetime.timezone.utc).strftime("%Y%m%d%H%M%S") == pseudo[1], "Pseudo-version commit or timestamp differs")
        return commit
    target = transport.json(prefix + "/git/ref/tags/" + urllib.parse.quote(version, safe=""))["object"]
    seen = set()
    while target["type"] == "tag":
        commit = target["sha"]
        need(hex_string(commit, 40) and commit not in seen and len(seen) < 8, "Invalid annotated tag chain")
        seen.add(commit)
        target = transport.json(prefix + "/git/tags/" + commit)["object"]
    need(target["type"] == "commit" and hex_string(target["sha"], 40), "Tag did not resolve to an immutable commit")
    return target["sha"]


def debian_order(a, b):
    def pieces(value):
        epoch, sep, rest = value.partition(":")
        if not sep:
            epoch, rest = "0", value
        need(epoch.isdigit(), "Invalid Debian epoch")
        upstream, sep, revision = rest.rpartition("-")
        return int(epoch), upstream if sep else rest, revision if sep else "0"
    def compare(left, right):
        def order(char):
            if char == "~":
                return -1
            if not char:
                return 0
            return ord(char) if char.isascii() and char.isalpha() else ord(char) + 256
        while left or right:
            while (left and not left[0].isdigit()) or (right and not right[0].isdigit()):
                l = left[0] if left and not left[0].isdigit() else ""
                r = right[0] if right and not right[0].isdigit() else ""
                if order(l) != order(r):
                    return (order(l) > order(r)) * 2 - 1
                left, right = left[len(l):], right[len(r):]
            l, r = re.match(r"[0-9]*", left)[0], re.match(r"[0-9]*", right)[0]
            ln, rn = l.lstrip("0"), r.lstrip("0")
            if (len(ln), ln) != (len(rn), rn):
                return ((len(ln), ln) > (len(rn), rn)) * 2 - 1
            left, right = left[len(l):], right[len(r):]
        return 0
    ae, au, ar = pieces(a)
    be, bu, br = pieces(b)
    return ((ae > be) * 2 - 1) if ae != be else compare(au, bu) or compare(ar, br)
