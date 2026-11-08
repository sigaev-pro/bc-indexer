"""Two-attempt official GET acquisition for the independent source refresh."""

from contextlib import contextmanager
import hashlib
import os
from pathlib import Path
import selectors
import signal
import ssl
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

import build_contract as contract
import build_execution
import build_source_evidence as source_evidence
import evidence


REQUEST_SECONDS = 60
SOCKET_SECONDS = 30
MAX_BODY = 256 * 1024 * 1024


class RequestDeadline(TimeoutError):
    """The original request deadline or operation stop forbids more work."""


class GhAPIError(ValueError):
    """A retained GitHub CLI request did not complete successfully."""


class CallerInterrupted(BaseException):
    """Carry a caller alarm exception without treating it as a socket error."""

    def __init__(self, original):
        self.original = original


def credential_echo(value, secrets):
    if not secrets:
        return False
    value = value.encode() if isinstance(value, str) else value
    for _ in range(4):
        if any(secret and secret in value for secret in secrets):
            return True
        decoded = urllib.parse.unquote_to_bytes(value)
        if decoded == value:
            break
        value = decoded
    return False


@contextmanager
def request_alarm(until):
    """Bound blocking reads and restore any earlier caller alarm."""
    contract.require(threading.current_thread() is threading.main_thread() and hasattr(signal, "setitimer"),
                     "bounded source acquisition requires the supported POSIX main thread")
    previous_handler = signal.getsignal(signal.SIGALRM)
    previous_delay, interval = signal.getitimer(signal.ITIMER_REAL)
    previous_until = time.monotonic() + previous_delay if previous_delay else None

    def arm():
        next_time = min(until, previous_until) if previous_until is not None else until
        signal.setitimer(signal.ITIMER_REAL, max(0.000001, next_time - time.monotonic()))

    def interrupted(number, frame):
        nonlocal previous_until
        now = time.monotonic()
        if previous_until is not None and now >= previous_until:
            previous_until = now + interval if interval else None
            if callable(previous_handler):
                try:
                    previous_handler(number, frame)
                except BaseException as error:
                    raise CallerInterrupted(error) from error
                raise CallerInterrupted(RequestDeadline("caller alarm ended source acquisition"))
            elif previous_handler == signal.SIG_DFL:
                signal.signal(signal.SIGALRM, previous_handler)
                os.kill(os.getpid(), signal.SIGALRM)
        if time.monotonic() >= until:
            raise RequestDeadline("source request reached its original deadline")
        arm()

    signal.signal(signal.SIGALRM, interrupted)
    try:
        arm()
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_handler)
        if previous_until is not None:
            signal.setitimer(signal.ITIMER_REAL, max(0.000001, previous_until - time.monotonic()), interval)


class RedirectPolicy(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, newurl):
        build_execution.official_url(newurl)
        if request.has_header("Authorization"):
            token = request.get_header("Authorization").split(" ", 1)[-1].encode()
            contract.require(not credential_echo(newurl, [token])
                             and not any(credential_echo(key, [token]) or credential_echo(value, [token]) for key, value in headers.items()),
                             "authenticated source redirect exposed credential data")
            before, after = urllib.parse.urlsplit(request.full_url), urllib.parse.urlsplit(newurl)
            contract.require((before.scheme, before.netloc) == (after.scheme, after.netloc),
                             "authenticated source redirect changed origin")
        return super().redirect_request(request, response, code, message, headers, newurl)


class Acquisition:
    def __init__(self, runner, url, root, headers=None, maximum=MAX_BODY):
        contract.require(not getattr(runner, "source_acquisition_blocked", False), "a terminal source acquisition already fenced this runner")
        build_execution.official_url(url)
        self.runner, self.url, self.root = runner, url, Path(root)
        self.maximum, self.headers = maximum, dict(headers or {})
        self.secrets = [value.split(" ", 1)[-1].encode() for key, value in self.headers.items() if key.lower() == "authorization"]
        contract.require(all(len(value) <= source_evidence.MAX_HEADERS for value in self.secrets), "source credential exceeds its bound")
        self.tail = b""
        contract.require(type(maximum) is int and 0 < maximum <= MAX_BODY, "source body limit is invalid")
        self.remember_caller_deadline()
        self.until = min(time.monotonic() + min(REQUEST_SECONDS, runner.remaining(REQUEST_SECONDS)),
                         getattr(runner, "source_caller_deadline", float("inf")))
        self.remaining()
        self.directory = self.root / "independent"
        self.directory.mkdir(exist_ok=True)
        self.key = hashlib.sha256(url.encode()).hexdigest()
        self.path = self.directory / (self.key + ".acquisition.json")
        contract.require(not self.path.exists(), "source URL acquisition was already dispatched")
        kind = "gh" if urllib.parse.urlsplit(url).hostname == "api.github.com" else "urllib"
        self.journal = {"schema_version": 1, "url": url, "synthetic": False, "transport": kind,
                        "outcome": "IN_PROGRESS", "attempts": []}
        self.opener = urllib.request.build_opener(RedirectPolicy())
        self.network_error = None
        self.current = None

    def remember_caller_deadline(self):
        if hasattr(signal, "getitimer"):
            delay, _ = signal.getitimer(signal.ITIMER_REAL)
            if delay:
                self.runner.source_caller_deadline = min(getattr(self.runner, "source_caller_deadline", float("inf")),
                                                         time.monotonic() + delay)

    def remaining(self, cap=SOCKET_SECONDS):
        try:
            contract.require(not getattr(self.runner, "source_acquisition_blocked", False), "source acquisition is fenced")
            available = min(cap, self.until - time.monotonic(), self.runner.remaining(cap),
                            getattr(self.runner, "source_caller_deadline", float("inf")) - time.monotonic())
        except ValueError as error:
            raise RequestDeadline("source operation stopped or expired") from error
        if available <= 0:
            raise RequestDeadline("source request reached its original deadline")
        return available

    def persist(self):
        try:
            payload = contract.canonical(self.journal)
            contract.require(len(payload) <= source_evidence.MAX_JOURNAL, "source attempt journal exceeds its bound")
            evidence.atomic_json(self.path, self.journal)
            contract.require(self.path.read_bytes() == payload, "source attempt journal changed during persistence")
        except BaseException:
            self.runner.source_acquisition_blocked = True
            raise

    def retain(self, stream, block):
        self.current["observed_bytes"] += len(block)
        scan = self.tail + block
        if credential_echo(scan, self.secrets):
            self.current["truncated"] = True
            raise ValueError("official source exposed credential data")
        overlap = max((len(value) * 9 for value in self.secrets), default=0)
        self.tail = scan[-overlap:] if overlap else b""
        available = max(0, self.maximum - stream.tell())
        stream.write(block[:available])
        self.current["truncated"] = len(block) > available
        contract.require(len(block) <= available, "source payload exceeds its bound")

    def urllib_get(self, body):
        request = urllib.request.Request(self.url, headers={"User-Agent": "extractor-build-verifier/1", **self.headers}, method="GET")
        try:
            response = self.opener.open(request, timeout=self.remaining())
        except urllib.error.HTTPError as error:
            response = error
        except (OSError, urllib.error.URLError) as error:
            self.network_error = error
            raise
        with response, body.open("xb") as stream:
            try:
                build_execution.official_url(response.url)
                self.current["status"] = response.status
                contract.require(not credential_echo(response.url, self.secrets)
                                 and not any(credential_echo(key, self.secrets) or credential_echo(value, self.secrets)
                                             for key, value in response.headers.items()), "official source exposed credential data")
                self.current["headers"] = {key.lower(): value for key, value in response.headers.items()
                                           if key.lower() in source_evidence.SAFE_HEADERS}
                contract.require(source_evidence.headers(self.current["headers"]), "source response headers exceed their bound")
                while True:
                    self.remaining()
                    try:
                        block = response.read(64 * 1024)
                    except (OSError, urllib.error.URLError) as error:
                        self.network_error = error
                        reason = error.reason if type(error) is urllib.error.URLError else error
                        partial = getattr(error, "partial", None)
                        if not isinstance(partial, bytes):
                            partial = getattr(reason, "partial", None)
                        if isinstance(partial, bytes):
                            self.retain(stream, partial)
                        raise
                    if not block:
                        break
                    self.retain(stream, block)
                contract.require(response.status == 200, "official source returned an unavailable status")
            finally:
                stream.flush()
                os.fsync(stream.fileno())

    def gh_get(self, body):
        ordinal = self.current["ordinal"]
        stdout_path = self.directory / (self.key + "." + str(ordinal) + ".stdout")
        stderr_path = self.directory / (self.key + "." + str(ordinal) + ".stderr")
        argv = ["gh", "api", "--include", "--method", "GET", self.url]
        process_row = {"argv": argv, "exit_code": None, "stdout": None, "stderr": None}
        self.current["process"] = process_row
        self.persist()
        self.remaining()
        process = None
        try:
            with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr, selectors.DefaultSelector() as selector:
                try:
                    with request_alarm(self.until):
                        # Stay in the already supervised driver group; no detached child.
                        process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                        for pipe, output, bound in ((process.stdout, stdout, self.maximum + source_evidence.MAX_HEADERS),
                                                    (process.stderr, stderr, source_evidence.MAX_STDERR)):
                            selector.register(pipe, selectors.EVENT_READ, (output, bound))
                        while selector.get_map():
                            self.remaining()
                            for key, _ in selector.select(min(0.05, self.remaining())):
                                block = os.read(key.fileobj.fileno(), 64 * 1024)
                                if not block:
                                    selector.unregister(key.fileobj)
                                    continue
                                output, bound = key.data
                                available = max(0, bound - output.tell())
                                output.write(block[:available])
                                contract.require(len(block) <= available, "GitHub source output exceeds its bound")
                        process.wait(timeout=self.remaining())
                finally:
                    if process is not None:
                        try:
                            if process.poll() is None:
                                process.kill()
                            process.wait(timeout=5)
                            process_row["exit_code"] = process.returncode
                        finally:
                            process.stdout.close()
                            process.stderr.close()
                    for stream in (stdout, stderr):
                        stream.flush()
                        os.fsync(stream.fileno())
        finally:
            for field, path in (("stdout", stdout_path), ("stderr", stderr_path)):
                if path.is_file():
                    process_row[field] = contract.reference(self.root, path)
        status, headers, offset = source_evidence.gh_header(stdout_path)
        self.current.update(status=status, headers=headers)
        with stdout_path.open("rb") as stream, body.open("xb") as target:
            stream.seek(offset)
            while block := stream.read(64 * 1024):
                self.remaining()
                self.retain(target, block)
            target.flush()
            os.fsync(target.fileno())
        if process_row["exit_code"] != 0:
            raise GhAPIError("GitHub source request failed")
        contract.require(status == 200, "independent GitHub observation did not succeed")

    def classify(self, error):
        cause = error.reason if type(error) is urllib.error.URLError else error
        code = "REJECTED"
        if isinstance(error, (RequestDeadline, CallerInterrupted)):
            code = "REQUEST_DEADLINE"
        elif error is self.network_error and type(cause) is TimeoutError:
            code = "NETWORK_TIMEOUT"
        elif error is self.network_error and type(cause) is ssl.SSLEOFError and getattr(cause, "reason", None) == "UNEXPECTED_EOF_WHILE_READING":
            code = "SSL_EOF"
        elif error is self.network_error:
            code = "UNKNOWN_TRANSPORT"
        elif type(error) is GhAPIError:
            process = self.current["process"]
            diagnostic = contract.verify_reference(self.root, process["stderr"]).read_bytes()
            if process["exit_code"] == 1 and diagnostic == ('Get "' + self.url + '": EOF\n').encode():
                code = "GH_EOF"
        elif isinstance(error, OSError):
            code = "LOCAL_IO"
        number = getattr(cause, "errno", None)
        result = {"type": type(error).__name__, "reason_type": type(cause).__name__ if cause is not error else None,
                  "code": code, "errno": number if type(number) is int and -4096 <= number <= 4096 else None, "retryable": False}
        result["retryable"] = source_evidence.retryable(result, self.journal["transport"], self.current["status"])
        return result

    def fetch(self):
        try:
            self.remember_caller_deadline()
            self.until = min(self.until, getattr(self.runner, "source_caller_deadline", float("inf")))
            return self._fetch()
        except BaseException:
            self.runner.source_acquisition_blocked = True
            raise

    def _fetch(self):
        for ordinal in (1, 2):
            self.remaining()
            now = time.time()
            body = self.directory / (self.key + "." + str(ordinal) + ".body")
            self.current = {"ordinal": ordinal, "state": "STARTED", "started_at": now, "finished_at": None,
                            "observed_at": now, "status": None, "headers": {}, "body": None, "error": None,
                            "observed_bytes": 0, "truncated": False, "process": None}
            self.tail = b""
            self.journal["attempts"].append(self.current)
            self.journal["outcome"] = "IN_PROGRESS"
            self.persist()
            self.remaining()
            failure = None
            try:
                if self.journal["transport"] == "gh":
                    self.gh_get(body)
                else:
                    with request_alarm(self.until):
                        self.urllib_get(body)
                self.remaining()
            except (ValueError, OSError, urllib.error.URLError, CallerInterrupted) as error:
                failure = error
                self.current.update(state="FAILED", error=self.classify(error))
            else:
                self.current["state"] = "SUCCEEDED"
            self.current["finished_at"] = time.time()
            if body.is_file():
                self.current["body"] = contract.reference(self.root, body)
            success = failure is None
            self.journal["outcome"] = ("FIRST_ATTEMPT_SUCCESS" if ordinal == 1 else "RECOVERED_TRANSPORT") if success else "FAILED"
            self.persist()
            if success:
                row = {"url": self.url, **{key: self.current[key] for key in ("status", "headers", "body", "observed_at")},
                       "acquisition": contract.reference(self.root, self.path)}
                source_evidence.validate_observation(row, self.root, False)
                self.remaining()
                return row
            if ordinal == 2 or not self.current["error"]["retryable"]:
                if isinstance(failure, CallerInterrupted):
                    raise failure.original
                raise failure
            self.remaining()
        raise AssertionError("source attempt bound was exceeded")


def fetch(runner, url, directory, headers=None, maximum=MAX_BODY):
    return Acquisition(runner, url, directory, headers, maximum).fetch()
