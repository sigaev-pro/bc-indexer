# Official component version checks

`scripts/refresh-versions` is a standalone Python CLI implementing
`component-update-v1`. It does not import trusted automation, run a build,
start Docker, sign transactions, or promote a lock.

```sh
python3 -I scripts/refresh-versions check --registry /absolute/registry.json --active-lock /absolute/candidate-lock.json --expected-lock-sha256 HASH --report /absolute/new-report.json
python3 -I scripts/refresh-versions update --registry /absolute/registry.json --active-lock /absolute/candidate-lock.json --expected-lock-sha256 HASH --report /absolute/new-report.json --output /absolute/new-candidate.json
```

Use the explicit accepted registry and lock paths. The expected hash binds the
active lock's raw bytes. Output files must be new, distinct regular paths in
existing directories. The candidate must be under the report's directory so
its artifact reference stays relative. Symlink destinations and aliases to
governed inputs are rejected. Governed external locks are read from this
checkout and fingerprinted before and after discovery. No mode writes an
active input, even with identical bytes.

Exit 0 means complete source discovery. Exit 1 produces an incomplete report
with concrete blocked reasons and no candidate. Exit 2 means invalid arguments,
stale input hashes, an invalid fixture transport, unsafe output paths, or an
output I/O or publication failure.
Check mode exposes proposed exact pins but never creates a candidate. Failed
update mode never publishes an owned file at the requested candidate path.
Existing output files are
preserved rather than overwritten; choose fresh paths for another attempt.

Outputs are first prepared in the invocation's owned evidence directory,
flushed, synchronized and closed. At most two preparation blobs are retained,
each limited to 64 MiB and named `unpromoted-candidate-...json` or
`unpromoted-report-...json`. They are diagnostic preparation evidence, distinct
from the requested candidate output. Failed partial preparation files are
removed only within that owned directory. Completed blobs remain with the
other discovery evidence for the caller to archive or clean.

An exclusive hard link publishes the report first. The candidate's exclusive
link is the final commit point, after all fallible preparation and report work.
No fallible cleanup follows that link. Final output paths are never deleted
or replaced, including on collisions or concurrent replacements. All writer
handles are closed before either publication.

On exit 2, a report may remain even though the candidate was not published.
Its `complete` field describes source discovery, and its candidate reference
describes the intended bytes; neither proves publication. Consumers must
require exit 0 and verify the actual candidate file against the report's
reference. A missing or different candidate, an exit-2 report, or an unpromoted
preparation blob cannot establish a successful update. The report is retained
without rewriting or deleting it after a failed candidate commit.

## Selection and retained evidence

All 41 registry entries appear in every discovery report. GitHub release and
module-tag pagination is followed within the same source identity. Draft,
prerelease, nightly and floating references cannot become pins. Annotated tags
are peeled to immutable commits. Native-owned forks use replacements from
their selected owner's go.mod, including commit-and-timestamp-verified
pseudo-versions. Dependency input paths retain their independent owner and
are rehashed from the newly selected commit.

GitHub may link a named repository page to `/repositories/{id}`. Before using
that alias, the updater fetches and retains metadata from the exact configured
`/repos/{owner}/{repo}` endpoint. The metadata must contain a positive integer
`id` (not a boolean), a `full_name` exactly equal to the configured owner/repo,
and the exact canonical `https://api.github.com/repos/{owner}/{repo}` URL.
Matching is case-sensitive; missing fields, malformed values and foreign
identities fail. No known repository ID is embedded in the implementation.
The numeric page must use that ID, the original endpoint tail and the same
official API origin. Named and numeric aliases share a logical page identity
for loop detection, including normalized query ordering. The 64-page limit
and one-next-link rule remain enforced.

Repository metadata is acquired only when a numeric alias needs it and appears
in the component's retained source references. Named-only paging does not add
an unnecessary metadata request. Non-GitHub page identity rules and the
credential-aware redirect guard remain unchanged.

Go source and both Linux archives are selected from official checksums. Update
mode also downloads and hashes their bytes. Official OCI tags must match the
selected Go/NATS version or stable Debian 13 slim channel; index and both
platform manifest bytes are hashed. Debian packages use all six accepted
architecture/channel indexes and Debian epoch/upstream/revision ordering.
Update mode verifies selected package archives against exact size and SHA-256.
NATS chart metadata must match the selected chart and server versions.
Malformed or truncated gzip/XZ package indexes produce blocked source rows
and an incomplete report in both modes; they never create a candidate.

HTTPS requests are restricted to registered official hosts. Transport follows
validated redirects, bounds pagination, rejects duplicate JSON keys and
nonfinite numbers, and retains response status, headers, acquisition time and
body references. Docker Hub public tokens are held only in memory and are not
written to reports. Each socket operation has a maximum 30-second timeout.
A logical GET has one 60-second monotonic deadline covering setup, both
attempts and body reads, within the 1800-second discovery deadline. Retained
responses have a 1 GiB byte limit per attempt.
JSON metadata is limited to 64 MiB. Failure observations are retained beside
the report in its uniquely named source directory; the caller owns that
directory and may archive it with the report. There are no background workers.

An official-source GET may restart from byte zero once after an exact typed
`TimeoutError`, or an exact `ssl.SSLEOFError` whose reason is
`UNEXPECTED_EOF_WHILE_READING`. The same types may be the explicit `reason`
of a `urllib.error.URLError`. A failed exception must come from the network
open/read stage, before HTTP status or with status 200. Unknown wrapper causes,
generic TLS errors, certificate failures, HTTP errors, credentials, redirects,
parsing, size/hash checks, file I/O, cancellation and expired deadlines never
permit a retry. OCI credential setup is outside this retry classification.
Parsing and authenticated artifact checks still run after acquisition.

Every output observation adds an `acquisition` artifact reference to its own
closed version-1 journal. It has `schema_version`, `url`, `synthetic`,
`transport` (`urllib`), `outcome` and `attempts`. Outcomes distinguish
`FIRST_ATTEMPT_SUCCESS`, `RECOVERED_TRANSPORT`, `FAILED` and unfinished
`IN_PROGRESS`. Each of at most two attempts records `ordinal`, `state`,
`started_at`, `finished_at`, `observed_at`, `status`, `headers`, `body`, `error`,
`observed_bytes`, `truncated` and `process` (null for urllib). Error objects
contain only sanitized `type`, `reason_type`, `code`, `errno` and `retryable`.
No raw exception message, authorization header, token or credential helper
output is serialized. Wall timestamps record observations, not elapsed-time
proof; the actual deadline uses the monotonic clock.

The journal is limited to 1 MiB and its intent is flushed and synchronized
before each dispatch. Admission is checked again after persistence. Each
attempt has a separate bounded body file. Available read or exception partial
bytes remain as evidence, including a bounded prefix and truncation count for
oversized responses; inaccessible TLS buffers are not claimed. Failed bytes
are never prepended to a successful retry. An earlier journal is verified
against its captured reference before replacement. A persistence failure
blocks all further requests in that transport. Interruption leaves the durable
unfinished intent and cannot silently retry.

A successful observation exactly binds the final attempt's status, headers,
body and observation time. A failed observation retains status 0 and a safe
diagnostic body; its journal separately retains any available response bytes.
The main-thread POSIX deadline guard restores signal handlers and the remaining
outer timer on exit; an earlier outer timer is never extended or classified as
a retryable network timeout. SIGTERM and SIGINT handlers are unchanged. An
unavailable guard fails explicitly before dispatch.

Live GitHub API requests reuse existing credentials in this order: nonempty
`GH_TOKEN`, then nonempty `GITHUB_TOKEN`, then the existing `github.com`
authentication returned by `gh auth token --hostname github.com`. The helper
runs at most once per transport, with no interactive input, prompts disabled,
captured output and a maximum five-second timeout within the discovery deadline.
It does not start a login or refresh credentials. Tokens never appear in command
arguments. Malformed selected tokens are rejected without trying a lower-priority
identity. If credentials are absent or the helper fails, requests remain
unauthenticated; source failures and rate-limit responses still produce honest
incomplete results.

GitHub authorization is sent only to the exact `https://api.github.com` origin
(including its equivalent explicit port 443). Cross-origin redirects remove
authorization; credentials are never attached to raw-source or download hosts.
Before urllib can follow a redirect, the guard checks its raw response headers
and destination for literal and percent-encoded credential echoes. It inspects
up to eight decoding layers and rejects deeper unresolved encoding. Rejected
redirect responses are closed without issuing a follow-up request. Actual
offline opener tests cover these boundaries as well as valid safe redirects.
Token values reject whitespace, controls and header injection. Live transport
errors retain only an error type, never exception text that could contain a
credential. Responses that echo a known token in headers or body bytes fail
acquisition, including tokens split across read blocks. Such response bytes
are discarded; retained error observations contain no token. Synthetic
transport never reads credential variables, builds an HTTP opener, or runs the
credential helper.

Each row distinguishes current version, latest stable version, selected exact
pin, compatibility NOT_RUN, notices, blockers and source URLs. No compatibility
or security clearance is inferred from a stable version. InRelease bytes are
retained, but Debian signature authentication belongs to the trusted driver's
pinned verifier/keyring. Live acceptance independently refreshes source
observations; an updater report alone cannot establish build or runtime safety.

Update produces a separate `PREPARED_CANDIDATE`, tied to the registry and
previous active-lock hashes. Release readiness stays false, profiles retain
their exact identities with build/runtime reset to NOT_RUN, and generated
outputs retain their owners and pending status. Later gates own promotion.

## Explicit synthetic transport

Add `--fixture-transport /absolute/transport.json` for offline protocol tests.
The closed version-1 object has `schema_version`, `synthetic: true`, and an
`observations` list. Each observation has exactly `url`, integer `status`,
string-to-string `headers`, numeric Unix `observed_at`, and `body` with a safe
relative path, raw SHA-256 and byte size. Body paths are relative to the
transport file's directory. Every descriptor is checked before discovery.
Exact URL lookup includes pagination. Missing URLs fail; there is no network
fallback. Bodies are copied into the output evidence directory and rehashed.
Reports and candidates remain explicitly synthetic regardless of timestamps.
Generated synthetic observations also have acquisition journals marked
`synthetic: true`, with the fixture's original observation time. This records
local fixture replay, not a real network request. Fixture inputs keep the
five-field observation shape described above. Old retained reports remain
historical evidence; this change does not invent acquisition journals for them.

Independent fixtures in `test/builds/fixtures.py` invent small source responses
and artifacts. They do not use oracle answers, real compiled binaries, or
trusted adapter fixtures. Synthetic success never satisfies live-source,
signature, native build, ABI or runtime acceptance.
