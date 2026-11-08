# Local native import slice

This is the first partial output of E01-01.harness. Its public seam is a CLI
request that admits immutable inputs, imports prepared blocks with the accepted
Geth binary, and compares offline native observations with a separately authored
oracle. It does not require an undocumented running node.

The slice covers a local value transfer followed by an empty block. It does not
complete the `quick` suite, a public network profile, capture, consensus, delivery,
or any lifecycle acceptance case. All seven public profile results remain
`NOT_RUN`. Individual local scenario results do not close AC-CAP-01-01, whose
owner is E01-02.lifecycle_seams.

## Public seam and trust boundary

`python3 -I test/harness/run.py check REQUEST.json` validates inputs without
creating state or invoking Docker. `run REQUEST.json --output NEW_DIRECTORY`
performs a measured run in a fresh owned directory. The caller supplies the
accepted BuildManifest path and exact SHA256; the harness checks its selected
Geth binary, source, runtime image, and profile association. Supplying a digest
does not independently establish that a build has been accepted.

The versioned local request and proof are separate from CaseResult and
ProfileManifest. Artifact references hash raw bytes, including binary and RLP
files. Public `ethereum-mainnet` build association is separate from private
fixture chain ID, genesis hash, and the digest of the complete local fork config.
No custom genesis claims the public chain identity or ENV_READY.

Preparation is separate from the measured run. Pinned Geth code generates input
blocks; those block hashes and state roots are input bindings, not an independent
semantic oracle. A human-readable, independently worked oracle fixes balances,
nonces, gas, transaction counts, and fees before native import. Both block states
and the complete exported chain must match, so a valid prefix cannot pass.

The measured deadline includes admission, initialization, import, observation,
comparison, and owned cleanup. It is finite and at most 1800 seconds. Native
commands have smaller finite caps. Containers have no network, bounded CPU,
memory and PIDs, a read-only root, read-only admitted inputs, and a unique owned
writable state directory. The importer never uses `--nocompaction`, which can
mask native import errors in this pinned client.

Each attempt has a new output identity. Failures and their logs are retained.
An admission-tamper rejection differs from a native-invalid-block test: the latter
must explicitly authorize the new corrupt input digest before dispatch. No
retry changes an earlier result or silently creates a successful suite result.

## Run it

Use Python 3.12 or newer on Linux or macOS, Docker with the accepted Linux/amd64
compiler and Debian runtime images already present, and an accepted actual-build
artifact directory. Preparation authenticates the selected source archive and
all retained Geth module downloads, extracts a new source tree, and runs native
module verification against the read-only cache. It uses separate writable Go
metadata and a fresh build cache. It performs no network requests or image pulls.

```sh
python3 -I test/harness/prepare.py \
  --build-manifest "$BUILD_ARTIFACTS/manifest.json" \
  --sha256 "$ACCEPTED_BUILD_MANIFEST_SHA256" \
  --output "$HARNESS_PREPARATION"
python3 -I test/harness/run.py check "$HARNESS_PREPARATION/request.json"
python3 -I test/harness/run.py run "$HARNESS_PREPARATION/request.json" \
  --output "$HARNESS_RUN"
```

All paths above must be absolute, with no symlink ancestors. Output directories
must be new and outside admitted build/fixture storage. Accepted inputs stay
unchanged. Preparation uses at most 900 seconds, 2 CPUs and 4 GiB. The default
measured run uses 300 seconds, 2 CPUs and 768 MiB per command. Each native command
has a 120-second cap clipped to the shared run deadline. A 35-second reserve
covers process/container cleanup. The request can lower or raise the total bound
up to 1800 seconds; insufficient remaining time rejects dispatch.

For explicit real controls, run:

```sh
python3 -I test/harness/exercise.py "$HARNESS_PREPARATION/request.json" \
  --output "$HARNESS_CONTROLS"
```

This separately records a positive import, a newly authorized truncated RLP input
that native import must reject, a wrong independent balance that comparison must
reject, and changed binary/genesis/RLP bytes that admission must reject before
creating native state. Real container timeout and parent SIGTERM/SIGKILL controls
test resource ownership with `/bin/sleep`; they are not Ethereum scenarios.
Expected negative native attempts retain `result: FAIL`; only the control's
expectation gets `control_result: PASS`. The control campaign has a separate
900-second bound and never becomes a quick-suite result.

Portable tests run with `python3 -B -m unittest discover -s test/harness` and in
bootstrap CI. They use explicit synthetic admission data and a fake Docker
executable at the process boundary. They do not claim native runtime evidence.

## Input and proof records

The closed request has `schema_version: 1`, `build_manifest`,
`profile_association`, `fixture`, and integer `deadline_seconds`. A raw artifact
reference has exactly `path`, lowercase hexadecimal `sha256`, and integer
`size_bytes`. Duplicate JSON keys, unknown request/fixture fields, symlinks, and
changed bytes fail admission. The fixture binds genesis JSON, import RLP, full
chain RLP, generator source, independent expected JSON, private identity, and two
prepared block identities. The config digest uses sorted compact JSON over the
complete native config. The fixed fixture uses chain ID 31337 and historical
London proof-of-work rules; it makes no claim about current public fork heads.

`proof.json` records the local run identity, source snapshots, admitted and copied
input references, selected build/profile, actual fixture identity, both observed
states, complete exported chain, command/stdout/stderr/cleanup records, measured
duration, scenario results, and remaining coverage. `run_id` is an evidence
identity; `native_owner_nonce` identifies its owned Docker commands. These are
distinct fields, never interchangeable. The source snapshots keep executed Python
bytes available even after a later checkout changes. Native block hashes/roots
bind input; balances/nonces/gas/fee expectations come from
[the independent oracle](fixtures/ORACLE.md).

The ownership guard keeps the child group leader unreaped with `waitid(WNOWAIT)`
until termination. This covers a leader that exits before its descendants. It
then reaps the leader, proves process-group absence, and separately verifies that
its exact named container is absent. It never signals a reaped PID. A parent
pipe remains open through normal execution; parent loss causes cleanup. Failed
or uncertain cleanup fences all later commands. Process visibility is required;
an unavailable absence check cannot produce a cleanup PASS.

Preparation writes `preparation.json`, including generator binary/source, retained
orchestration bytes, oracle-before-generation, command ownership, and separate
preparation duration. A corrected preparation may use `--previous-attempt` and
`--correction`; the referenced earlier FAIL remains intact. Failed preparation is
not a native runtime CaseResult. There is no implicit retry in the measured runner.

The pure comparison boundary is `compare_state(observed, expected, state_root)`
and `compare(admitted, dumps, exported)` in `compare.py`. It validates literal
expectations, requires every expected address, and compares the full raw chain.
Record validation and artifact hashes provide reproducibility and internal
consistency. Independent review and an observed real invocation establish the
execution claim; a caller-created JSON record alone does not.

## Remaining work in issue 12

BSC and Bor native fixture adapters, their authenticated system state, all other
required fixture families and forks, shared ProfileManifest/RunPlan resolution,
supporting services, public mainnet/testnet smoke, and integration with future
capture and Gateway components remain unimplemented here. The collector and NATS
are absent by design at this first native seam. E03 delivery and later composed
gates will add their assigned runtime requirements. The accepted corpus and
product contracts are unchanged.
