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
written to reports. Each request has a maximum 30-second timeout, discovery
has an 1800-second deadline, and retained responses have a 1 GiB byte limit.
JSON metadata is limited to 64 MiB. Failure observations are retained beside
the report in its uniquely named source directory; the caller owns that
directory and may archive it with the report. There are no background workers.

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

Independent fixtures in `test/builds/fixtures.py` invent small source responses
and artifacts. They do not use oracle answers, real compiled binaries, or
trusted adapter fixtures. Synthetic success never satisfies live-source,
signature, native build, ABI or runtime acceptance.
