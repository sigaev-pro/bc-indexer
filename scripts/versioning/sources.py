"""Bounded official-source transport and stable version selection."""

import datetime
from contextlib import contextmanager
import functools
import hashlib
import http.client
import math
import os
from pathlib import Path
import re
import signal
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

from core import closed, decode, describe, encode, hex_string, need, ref, sha

HOSTS = {"api.github.com", "raw.githubusercontent.com", "go.dev", "dl.google.com",
         "registry-1.docker.io", "deb.debian.org", "security.debian.org"}
REQUEST_SECONDS = 60
BODY_LIMIT = 1024 ** 3


class RequestDeadline(ValueError):
    pass


class CredentialExposure(ValueError):
    pass


class DeadlineGuardUnavailable(ValueError):
    pass


@contextmanager
def request_timer(deadline, guard=None):
    """Bound one synchronous GET, including body reads, on supported CLI hosts."""
    if threading.current_thread() is not threading.main_thread() or not hasattr(signal, "setitimer"):
        raise DeadlineGuardUnavailable("Official-source request deadline guard is unavailable")
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise RequestDeadline("Official-source request deadline exceeded")
    previous = signal.getsignal(signal.SIGALRM)
    prior_delay, prior_interval = signal.getitimer(signal.ITIMER_REAL)
    prior_due = time.monotonic() + prior_delay if prior_delay else None
    if guard is None:
        guard = {"interrupted": False, "outer_expired": False}

    def alarm(signum, frame):
        guard["interrupted"] = True
        if prior_due is not None and time.monotonic() >= prior_due:
            guard["outer_expired"] = True
            if callable(previous):
                previous(signum, frame)
            elif previous == signal.SIG_DFL:
                signal.signal(signal.SIGALRM, previous)
                signal.raise_signal(signal.SIGALRM)
        raise RequestDeadline("Official-source request deadline exceeded")

    installed = False
    try:
        try:
            signal.signal(signal.SIGALRM, alarm)
            installed = True
            signal.setitimer(signal.ITIMER_REAL, min(remaining, prior_delay) if prior_delay else remaining)
        except (OSError, ValueError):
            raise DeadlineGuardUnavailable("Official-source request deadline guard installation failed") from None
        yield guard
    finally:
        if installed:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)
            if prior_due is not None:
                left = prior_due - time.monotonic()
                if left <= 0:
                    guard["outer_expired"] = True
                if left <= 0 and prior_interval:
                    left = prior_interval - ((-left) % prior_interval)
                signal.setitimer(signal.ITIMER_REAL, max(0, left), prior_interval)


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


def credential_echo(value, secrets):
    """Inspect literal and nested percent-encoded forms within a finite bound."""
    for _ in range(8):
        if any(secret in value for secret in secrets):
            return True
        decoded = urllib.parse.unquote(value)
        if decoded == value:
            return False
        value = decoded
    # Do not forward an encoding depth that the guard has not inspected.
    return True


class Redirect(urllib.request.HTTPRedirectHandler):
    def __init__(self, secret_values=lambda: ()):
        super().__init__()
        self.secret_values = secret_values

    def redirect_request(self, request, response, code, message, headers, newurl):
        secrets = list(self.secret_values())
        authorization = request.get_header("Authorization")
        if authorization:
            secrets.append(authorization.split(" ", 1)[-1])
        try:
            need(not credential_echo(newurl, secrets)
                 and not any(credential_echo(k, secrets) or credential_echo(v, secrets) for k, v in headers.items()),
                 "Redirect response contains credential data or excessive encoding")
            official(newurl)
            redirected = super().redirect_request(request, response, code, message, headers, newurl)
            if origin(newurl) != origin(request.full_url):
                redirected.remove_header("Authorization")
            return redirected
        except BaseException:
            if response is not None:
                response.close()
            raise


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
        self.request_deadline = self.deadline
        self.network_error = None
        self.persistence_failed = False
        self.outer_expired = False
        self.tokens = {}
        self.github_checked = False
        self.github_token = None
        self.opener = None if self.synthetic else urllib.request.build_opener(Redirect(self.secret_values))
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
        if self.outer_expired:
            raise RequestDeadline("Official-source outer operation deadline expired")
        remaining = min(self.deadline, self.request_deadline) - time.monotonic()
        if remaining <= 0:
            raise RequestDeadline("Official-source discovery or request deadline exceeded")
        return min(30, remaining)

    def write_journal(self, path, value, previous):
        if previous is not None:
            ref(self.root, previous)
        else:
            need(not path.exists(), "Source acquisition journal already exists")
        payload = encode(value)
        need(len(payload) <= 1024 * 1024, "Source acquisition journal exceeds its bound")
        descriptor, temporary = tempfile.mkstemp(prefix=".acquisition-", dir=path.parent)
        try:
            with os.fdopen(descriptor, "wb") as stream:
                need(stream.write(payload) == len(payload), "Incomplete acquisition journal write")
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, path)
            directory = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            Path(temporary).unlink(missing_ok=True)
        result = describe(self.root, path)
        need(result["sha256"] == hashlib.sha256(payload).hexdigest(), "Source acquisition journal changed during persistence")
        return result

    def persist(self, path, value, previous):
        try:
            return self.write_journal(path, value, previous)
        except BaseException:
            self.persistence_failed = True
            raise

    def failure(self, error, status):
        cause = error.reason if type(error) is urllib.error.URLError else error
        network = error is self.network_error and status in (None, 200)
        code = "REJECTED"
        if network and type(cause) is TimeoutError:
            code = "NETWORK_TIMEOUT"
        elif network and type(cause) is ssl.SSLEOFError and getattr(cause, "reason", None) == "UNEXPECTED_EOF_WHILE_READING":
            code = "SSL_EOF"
        elif isinstance(error, RequestDeadline):
            code = "REQUEST_DEADLINE"
        elif network:
            code = "UNKNOWN_TRANSPORT"
        elif isinstance(error, OSError):
            code = "LOCAL_IO"
        def name(value):
            value = type(value).__name__
            return value if re.fullmatch(r"[A-Za-z_][A-Za-z_0-9]{0,127}", value) and not self.contains_secret(value) else "Error"
        number = getattr(cause, "errno", None)
        return dict(type=name(error), reason_type=name(cause) if cause is not error else None,
                    code=code, errno=number if type(number) is int and -4096 <= number <= 4096 else None,
                    retryable=code in {"NETWORK_TIMEOUT", "SSL_EOF"})

    def fetch(self, url):
        need(not self.persistence_failed, "Source evidence persistence failed; further requests are blocked")
        self.request_deadline = min(self.deadline, time.monotonic() + REQUEST_SECONDS)
        self.timeout()
        parsed = official(url)
        if not self.synthetic and origin(url) == ("https", "api.github.com", 443) and not self.github_checked:
            self.github_token = github_credential(self.timeout())
            self.github_checked = True
        need(not self.contains_secret(url), "Official source URL contains credential data")
        self.used.add(url)
        if url in self.rows:
            return ref(self.root, self.rows[url]["body"])
        need(len(self.rows) < 4096, "Official-source observation limit exceeded")
        directory = self.directory / (hashlib.sha256(url.encode()).hexdigest() + ".acquisition")
        directory.mkdir()
        # Retain both new directory entries before the first request intent.
        try:
            for parent in (self.directory, self.root):
                descriptor = os.open(parent, os.O_RDONLY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
        except BaseException:
            self.persistence_failed = True
            raise
        journal = dict(schema_version=1, url=url, synthetic=self.synthetic, transport="urllib",
                       outcome="IN_PROGRESS", attempts=[])
        journal_path, journal_ref = directory / "journal.json", None
        for ordinal in (1, 2):
            self.timeout()
            observed = time.time()
            attempt = dict(ordinal=ordinal, state="STARTED", started_at=observed, finished_at=None,
                           observed_at=observed, status=None, headers={}, body=None, error=None,
                           observed_bytes=0, truncated=False, process=None)
            journal["attempts"].append(attempt)
            journal["outcome"] = "IN_PROGRESS"
            journal_ref = self.persist(journal_path, journal, journal_ref)
            destination = directory / (str(ordinal) + ".body")
            self.network_error = None
            failure = None
            guard = {"interrupted": False, "outer_expired": False}
            try:
                # Admission is checked again after the intent has reached disk.
                self.timeout()
                with request_timer(self.request_deadline, guard):
                    if self.synthetic:
                        need(url in self.fixture, "Synthetic transport has no response for " + url)
                        fixture_root, row = self.fixture[url]
                        source = ref(fixture_root, row["body"])
                        attempt.update(status=row["status"], headers={k.lower(): v for k, v in row["headers"].items()},
                                       observed_at=row["observed_at"])
                        with source.open("rb") as stream, destination.open("xb") as output:
                            self.copy(stream, output, attempt)
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
                        except Exception as error:
                            self.network_error = error
                            raise
                        with response, destination.open("xb") as output:
                            attempt["status"] = response.status
                            headers = {k.lower(): v for k, v in response.headers.items()}
                            if any(self.contains_secret(k) or self.contains_secret(v) for k, v in headers.items()):
                                raise CredentialExposure("Official response exposed credential data")
                            attempt["headers"] = {k: v for k, v in headers.items()
                                                  if k not in {"authorization", "proxy-authorization", "set-cookie"}}
                            self.copy(response, output, attempt, network=True)
                if guard["outer_expired"]:
                    raise RequestDeadline("Official-source outer operation deadline expired")
                need(type(attempt["status"]) is int and attempt["status"] == 200,
                     "Official source did not return HTTP 200")
            except (ValueError, OSError, http.client.HTTPException) as error:
                failure = self.failure(error, attempt["status"])
                if guard["interrupted"]:
                    failure.update(code="REQUEST_DEADLINE", retryable=False)
                if isinstance(error, CredentialExposure):
                    destination.unlink(missing_ok=True)
                    attempt["truncated"] = attempt["observed_bytes"] > 0
            finally:
                if guard["outer_expired"]:
                    self.outer_expired = True
            # File persistence errors are outside network classification. Never
            # send another request when its preceding evidence cannot be kept.
            try:
                if destination.exists():
                    with destination.open("rb") as stream:
                        os.fsync(stream.fileno())
                    attempt["body"] = describe(self.root, destination)
            except BaseException:
                self.persistence_failed = True
                raise
            attempt.update(state="FAILED" if failure else "SUCCEEDED", error=failure, finished_at=time.time())
            journal["outcome"] = "FAILED" if failure else ("FIRST_ATTEMPT_SUCCESS" if ordinal == 1 else "RECOVERED_TRANSPORT")
            journal_ref = self.persist(journal_path, journal, journal_ref)
            if failure and failure["retryable"] and ordinal == 1:
                continue
            try:
                if failure:
                    destination = directory / "failure.body"
                    destination.write_bytes(("Source acquisition failed: " + failure["type"]).encode())
                    with destination.open("rb") as stream:
                        os.fsync(stream.fileno())
                body_ref = describe(self.root, destination)
            except BaseException:
                self.persistence_failed = True
                raise
            self.rows[url] = dict(url=url, status=0 if failure else attempt["status"],
                                 headers={} if failure else attempt["headers"], body=body_ref,
                                 observed_at=attempt["observed_at"], acquisition=journal_ref)
            need(failure is None, "Official source unavailable (status 0): " + url)
            return destination

    def secret_values(self):
        return [value for value in [self.github_token, *self.tokens.values()] if isinstance(value, str) and value]

    def contains_secret(self, value):
        return credential_echo(value, self.secret_values())

    def copy(self, source, target, attempt, network=False):
        total = 0
        secrets = [value.encode() for value in self.secret_values()]
        overlap = max((len(value) - 1 for value in secrets), default=0)
        tail = b""
        def retain(block):
            nonlocal total, tail
            attempt["observed_bytes"] += len(block)
            scan = tail + block
            if any(secret in scan for secret in secrets):
                raise CredentialExposure("Official response exposed credential data")
            tail = scan[-overlap:] if overlap else b""
            kept = block[:max(0, BODY_LIMIT - total)]
            target.write(kept)
            total += len(kept)
            attempt["truncated"] = len(kept) != len(block)
            need(not attempt["truncated"], "Official artifact exceeds the 1 GiB acquisition limit")
        while True:
            self.timeout()
            try:
                block = source.read(64 * 1024)
            except Exception as error:
                if network:
                    self.network_error = error
                partial = getattr(error, "partial", None)
                if not isinstance(partial, bytes) and type(error) is urllib.error.URLError:
                    partial = getattr(error.reason, "partial", None)
                if isinstance(partial, bytes):
                    retain(partial)
                raise
            if not block:
                return
            retain(block)

    def data(self, url):
        path = self.fetch(url)
        need(self.rows[url]["status"] == 200, "Official source returned a failed observation")
        need(path.stat().st_size <= 64 * 1024**2, "Official metadata exceeds 64 MiB")
        return path.read_bytes()

    def json(self, url):
        return decode(self.data(url))

    def pages(self, url, repository=None):
        initial = official(url)
        initial_origin = origin(url)
        named = (re.fullmatch(r"/repos/([^/]+)/([^/]+)(/.*)", initial.path)
                 if repository is None and initial_origin == ("https", "api.github.com", 443) else None)
        repository_id = None
        seen, values = set(), []
        while url:
            target = official(url)
            need(len(seen) < 64, "Official pagination exceeds the page limit")
            if named is not None:
                need(origin(url) == initial_origin, "GitHub pagination changed origin")
                if target.path != initial.path:
                    alias = re.fullmatch(r"/repositories/([1-9][0-9]*)(/.*)", target.path)
                    need(alias is not None and alias[2] == named[3], "GitHub pagination changed repository endpoint")
                    if repository_id is None:
                        metadata_url = urllib.parse.urlunsplit((initial.scheme, initial.netloc,
                                                               "/repos/" + named[1] + "/" + named[2], "", ""))
                        metadata = self.json(metadata_url)
                        need(type(metadata) is dict and type(metadata.get("id")) is int and metadata["id"] > 0,
                             "GitHub repository metadata has no positive integer identity")
                        configured_name = named[1] + "/" + named[2]
                        need(metadata.get("full_name") == configured_name
                             and metadata.get("url") == "https://api.github.com/repos/" + configured_name,
                             "GitHub repository metadata differs from the configured named identity")
                        repository_id = metadata["id"]
                    need(alias[1] == str(repository_id), "GitHub numeric pagination belongs to another repository")
                # Named and numeric aliases identify the same logical page.
                page_key = (initial.path, tuple(sorted(urllib.parse.parse_qsl(target.query, keep_blank_values=True))))
            else:
                need((target.scheme, target.netloc, target.path) == (initial.scheme, initial.netloc, initial.path),
                     "Invalid official pagination")
                page_key = url
            need(page_key not in seen, "Official pagination contains a loop")
            seen.add(page_key)
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
        if (entry["id"] == "docker-engine" and kind == "github_releases"
                and source.get("repository") == "moby/moby" and source["channel"] == "stable"):
            # Moby also publishes API and client tags; only Engine tags belong here.
            match = re.fullmatch(r"(?:docker-)?v([0-9]+)\.([0-9]+)\.([0-9]+)", version) if isinstance(version, str) else None
            score = tuple(int(part) for part in match.groups()) if match else None
        else:
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
