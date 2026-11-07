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
stale input hashes, an invalid fixture transport, or unsafe output paths.
Check mode exposes proposed exact pins but never creates a candidate. Failed
update mode never retains a newly created candidate. Existing output files are
preserved rather than overwritten; choose fresh paths for another attempt.

Outputs are first written to owned temporary files in their destination
directories, flushed, synchronized and closed. An exclusive hard link then
publishes each complete file without replacing an existing name. The updater
retains an open handle to each created inode through the output transaction.
If report publication fails, rollback removes only outputs that still belong
to those retained inodes. A concurrent replacement is preserved. Temporary
files and retained handles are cleaned on completion or a handled failure.

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
