# Native build interfaces

These product interfaces implement `build-artifacts-v1` for E01-01.builds.
They use the Python standard library and work under `python3 -I` from any
working directory. They never execute a native build or start a service.

## Plan

Send one JSON object to `python3 -I build/native/plan.py` on standard input:

```json
{"contract":"build-artifacts-v1","kind":"BuildPlan","registry_path":"/absolute/source/.project/bootstrap/components/registry.json","lock_path":"/absolute/source/.project/bootstrap/components/candidate-lock.json","platform":"linux/amd64","limits":{"cpus":4,"memory_bytes":6442450944,"pids":256,"command_seconds":3600}}
```

The closed response binds the raw input hashes, five native/support clients,
ten separate Go module roots, exact source and dependency inputs, compiler,
three immutable image references, platform-specific package pins and indexes,
seven profiles, and later-owned outputs. Polygon profiles retain their own L1
bindings. Geth, BSC, and Bor never share their internal dependency graph.
Source-contained replacements stay with their owning source archive and
`local_modules` closure. The initial patch list is empty.

Both candidate platforms can be planned. Planning arm64 does not execute it.
Only the coordinator's trusted driver executes the initial linux/amd64 build.
Build controls must use `GOTOOLCHAIN=local`; the driver owns fixed commands,
finite timeouts and resource limits. Do not invoke floating `@latest`
generator targets. The plan contains no author-supplied shell command.

Success exits 0 and prints one JSON object. Invalid input exits 2 with an
English diagnostic on standard error. Duplicate JSON keys, nonfinite numbers,
booleans in integer fields, unknown fields, and out-of-range limits fail.

## Materialized preflight

Send a `VerifyManifest` request to `python3 -I build/native/verify.py`.
Its exact fields are `contract`, `kind`, `synthetic`, `root`, `manifest_path`,
`manifest_sha256`, `profile_id`, `registry_path`, `lock_path`, and `identity`.
The root and input paths are absolute. The manifest path is relative to the
artifact root. Identity contains `run_id`, `operation_id`, `nonce`, `head_sha`,
`base_sha`, and `verifier_source_commit`.

The preflight checks the raw manifest hash before parsing it. It reconstructs
the plan from the selected input bytes, checks canonical plan equality and its
digest, and checks every artifact reference incrementally. References contain
exactly `path`, `sha256`, and integer `size_bytes`. Missing files, changed bytes,
symlinks, traversal, source substitutions, missing closures, wrong profile
bindings, failed commands, and changed active locks fail. Actual native
binaries must have the selected 64-bit Linux ELF architecture.

The versioned JSON Schema describes the closed manifest and nested records.
Cross-field equality, canonical hashing, filesystem safety, and materialized
bytes require the executable preflight in addition to schema validation.
Canonical JSON is ASCII-escaped, key-sorted compact JSON followed by one
newline. Existing input files and artifact references use raw-byte hashes.

Responses contain exactly `valid` and a nonempty English `reason`. Valid input
exits 0; rejected input exits 2. Synthetic mode allows explicitly labeled
synthetic protocol evidence, while preserving all structural, hash, identity,
coverage, and profile checks. Actual mode rejects synthetic evidence.

Preflight is one acceptance layer. The independent driver additionally checks
source archives, ELF/Go build information, package signatures, OCI contents,
local replacement graphs, actual command provenance and owned-resource cleanup.
The product checks the materialized references to those proofs; it cannot
independently establish the absence of a Docker process from a saved report.
Do not use a preflight success as native execution or ownership proof.

## Evidence and ownership

The driver owns source acquisition, native build execution, signature checks,
Docker resources, full logs and final cleanup confirmation. Never import the
trusted driver into product code. Each command and output binds its platform,
source and task identity. Cleanup touches only resources owned by that run.
Preserve failures and diagnostic retries separately.

The independent tests under `test/builds/` create explicitly synthetic bytes
in temporary directories. They establish protocol behavior only. Native
builds, repeat builds, arm64 ABI/runtime, profile bootstrap, consensus, capture,
NATS delivery and promotion remain NOT_RUN until their own actual checks.

Run product checks with:

```sh
python3 -I -m unittest discover -s test/builds -p 'test_*.py'
python3 .project/automation/ci.py
```
