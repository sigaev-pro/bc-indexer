# Isolated build acceptance interface

Status: AUT-03 was accepted through [PR #100](https://github.com/sigaev-pro/bc-indexer/pull/100), closing [issue #98](https://github.com/sigaev-pro/bc-indexer/issues/98). The contract is `build-artifacts-v1`; the updater contract is `component-update-v1`. Only an accepted configuration handoff enables the product task. Native builds remain NOT_RUN until separately executed and accepted.

## Existing requirements

The product task is [E01-01.builds](../docs/TASK_DECOMPOSITION.md#task-e01-01-builds), issue 11. Its assigned criteria are AC-OPS-10-01, AC-OPS-10-03, and AC-DEV-03-01. D83 also assigns the standalone version updater to this task. The accepted [testing contract](../docs/TESTING_CONTRACT.md) and specification remain authoritative.

The task produces separate source, patch, compiler, dependency, and artifact records for native clients and supporting inputs. It must reject an incorrect artifact or profile before native start. Native client dependency graphs remain separate. The updater covers the complete versioned component registry, discovers official stable releases, creates exact candidate locks, and never promotes an active lock before the required later gates.

A compiled binary is evidence of a build. It does not prove profile bootstrap, consensus compatibility, capture correctness, testnet transactions, NATS delivery, or a full stage gate. Every such result retains its own owner and NOT_RUN state until executed. Metadata acquisition is not compilation. A generated report cannot substitute for materialized output bytes or execution evidence.

## Trusted boundary

Use a task-specific adapter selected by the exact task ID. Preserve the existing evidence adapter and already retained receipts. There is no fallback that accepts an unknown product task through bootstrap CI.

Freeze the build contract, independent driver, oracle inputs, task recipe, accepted component registry, and candidate-lock inputs in the reviewed source snapshot. A source or recipe change requires the existing safe handoff. The proof binds the exact candidate head and base, accepted verifier source, command requests and results, materialized output hashes, and separate independent review. Public proof binding and protected merge checks remain mandatory.

The implementation must retain negative proof that stale task IDs, candidate hashes, base commits, source packs, profiles, output files, and public bindings are rejected. Existing evidence-task proof checks must continue to pass unchanged.

## Product surface

Keep product outputs in `build/native/`, updater helpers under `scripts/versioning/`, independent product tests under `test/builds/`, the task evidence report, and the status document. Required files are `build/native/plan.py`, `build/native/verify.py`, `build/native/build-manifest.schema.json`, `build/native/README.md`, `scripts/refresh-versions`, `scripts/versioning/README.md`, and `test/builds/test_build_contract.py`. The recipe does not grant product write access to trusted automation or the bootstrap inventory.

The product provides a constrained build plan, artifact preflight, and the standalone updater. The trusted driver owns preparation, fixed native commands, complete build execution, and evidence assembly. Candidate code cannot supply a shell build command or replace the driver. Inputs must not depend on an author's hidden running node or an unrecorded local compiler. Test fixtures are explicitly synthetic and cannot establish actual build evidence.

The versioned build manifest must identify:

- The exact selected registry and lock, task/run identity, platform, source/patch/compiler inputs, command arguments, safe environment overrides, timestamps, exit results, and full log references.
- Each client's own source and resolved dependency graph, including native-owned module replacements. Geth, BSC, and Bor sharing an upstream module path does not permit sharing their internal type graph.
- Materialized artifacts by safe relative path, byte size, and content digest, with the build information needed to compare them with the declared inputs. Hash large files incrementally. Reject symlinks, traversal, missing/changed files, floating image references, and profile substitutions.
- All seven concrete profile bindings. Reuse of one compiled binary must be explicit in each applicable profile record; it does not transfer runtime results between profiles.
- Supporting images, toolchains, and resolved OS/module dependencies required by the build. Distinguish acquired inputs, compiled outputs, and outputs owned by later tasks. An unfinished required build closure blocks this task; a later-owned output cannot be silently marked complete.

The accepted component inventory has 41 entries and explicit generated-output ownership. Preserve that coverage. The adapter must derive required coverage from the accepted registry, task ownership, and selected inputs; it must not remove difficult entries or relabel required work as optional to obtain PASS.

## Version updater

`scripts/refresh-versions check` must inspect all registered official sources without changing active locks. Its report distinguishes current, latest stable, selected candidate, source observation time, compatibility/security notices, blocked reasons, and incomplete source checks. An unavailable or malformed source produces a nonzero incomplete result. Drafts, prereleases, nightly builds, floating branches, and `latest` do not become selected pins.

`update` creates a separate exact candidate. Resolve source tags to immutable commits, selected images to content digests, artifacts to verified hashes, and dependency inputs to exact locks. Preserve native bundles and their recorded replacements unless a separately verified update changes them. Do not silently substitute another artifact, update the active lock, or transfer old runtime results. Unresolved compatibility remains explicit.

Use independent controlled official-source fixtures to test success and failure paths without requiring the public internet for every unit test. An explicit fixture transport must mark its results synthetic and cannot satisfy live-source acceptance. The final task also needs actual official-source observations and immutable evidence. Tests must cover unavailable sources, missing coverage, stale or changed responses, unstable versions, tag/commit confusion, image/tag mismatch, and accidental active-lock mutation.

## Actual execution and cleanup

Run real build commands through owned supervision. D93 removes the campaign deadline after explicit adoption; each build verification still has a finite timeout of at most 7,200 seconds, and each command retains its own timeout and resource limits. Historical D92 executions remain bounded by their original absolute deadline. Preparation is outside the 1,800-second standard runtime-test budget. Do not erase a failed attempt with a successful retry.

Docker daemon resources outlive the host CLI. Record immutable owned container IDs and task/run labels before execution. On success, failure, interruption, or timeout, reconcile and clean only those resources, then confirm their state. A killed host process group alone is insufficient. Retain uncertainty and block further dispatch if ownership or cleanup cannot be proved. Never use global prune or touch unrelated images, containers, or volumes.

The independent driver must combine behavioral failure mutations with actual build provenance checks. A self-reported PASS, a matching version string, a closed issue, or a fixture lookup table is insufficient. Commands, materialized outputs, source inputs, and their exact identities must agree. The adapter may be tested with synthetic fixtures before product implementation, but that does not accept an actual build.

## First local preparation

The canonical checkout already has five clean pinned native source caches and three pinned Linux amd64 images. They are preparation only. The observed Docker host is Linux x86_64 with 16 CPUs and about 8 GiB RAM; native builds should run sequentially within explicit limits. Recheck capacity before execution. No native source has yet been built or started during D92.

AUT-03 was accepted through PR #100. Adopt the accepted configuration and D93 policy on the existing run through the [continuous-work procedure](continuous-work.md), then execute E01-01.builds. Product implementation and acceptance remain separate from maintenance acceptance.

## Normative field definitions

[build_contract.py](build_contract.py) is the executable field definition for `BuildPlan`, artifact references, command records, and the nested build manifest. Its constants, `expected_plan`, `limits`, `reference_descriptor`, `command_record`, and `validate_manifest` define the exact closed shapes and cross-field constraints. [build_updates.py](build_updates.py) defines `UpdateReport`, source observations, component selection rules, and the prepared candidate shape. These two modules are interface definitions that implementers may read. Implement product behavior independently; do not import trusted automation at runtime, inspect the oracle fixture modules or tests, or special-case their values.

All protocol JSON rejects duplicate object keys and nonfinite numbers. Hash canonical JSON as UTF-8 `json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False)` followed by one newline. Hash existing input and artifact files as their raw bytes. Do not confuse these domains. Artifact references contain exactly `path`, `sha256`, and `size_bytes`; paths are relative POSIX paths to regular files under the declared root, without symlink traversal. Each referenced file must exist and match its size and digest.

Each module closure also references `local_modules`, a canonical JSON map keyed by the original module path. It is empty when the graph has no source-contained replacement. Each entry contains the source-relative replacement `path`, the original requested `version`, and `files`, a map of archived source-relative file paths to `sha256` and `size_bytes`. The trusted runner derives these entries from regular files in the exact source archive, verifies the local go.mod identity, and separately retains downloaded module artifacts. Absolute paths, parent traversal, links within a local replacement, and package directories outside the verified replacement fail. This supports the pinned Prysm go-bip39 replacement without sharing another client's source or cache.

Every candidate command runs under a trusted guard that remains its process-group leader. A pipe ties that guard to the driver: closing the pipe, including after forced driver death, stops the guard's entire group. The driver keeps its guard child unreaped until it has stopped remaining descendants and confirmed the group has no live members. Only then may it reap the leader and record complete cleanup. Outer supervision and its watchdog also inspect the durable candidate-group inventory; missing registration or uncertain absence blocks cleanup. They never signal numeric group IDs read from old files. All governed external locks are fingerprinted alongside the active inputs before and after commands. After every candidate preflight has finished, the trusted runner checks the materialized files again before emitting its report. A candidate's success response cannot override changed bytes or uncertain process cleanup.

`BuildPlan` and the manifest have integer `schema_version: 1`, `task_id: E01-01.builds`, and `contract: build-artifacts-v1`. Integer fields reject booleans. Unknown fields, missing coverage, malformed hashes, and changed input bindings fail validation.

## Build plan CLI

Run `python3 -I build/native/plan.py`. Read one JSON object from stdin and emit one JSON object on stdout. The request contains exactly `contract`, `kind: BuildPlan`, `registry_path`, `lock_path`, `platform`, and `limits`. Paths identify explicit input files; the request does not depend on the current directory. Exit 0 with the complete plan or 2 for an invalid request. Send diagnostics to stderr.

The output is the exact projection defined by `expected_plan`: registry and lock raw hashes; both candidate platforms and the other unexecuted platform; five native component IDs; ten module component IDs; each exact source repository/commit/version/dependency-input set; an empty initial patch list; the selected compiler, three image references and platform digests; selected Debian package revisions/artifacts/indexes; all seven profile bindings and dedicated Polygon L1 references; explicit later-owned outputs; `runtime: NOT_RUN`; and `promotion: FORBIDDEN_UNTIL_REQUIRED_GATES`.

Resource limits contain exactly `cpus`, `memory_bytes`, `pids`, and `command_seconds`. Defaults are 4 CPUs, 6 GiB, 256 processes, and 3,600 seconds per command. Allowed ranges are 1–4 CPUs, 512 MiB–6 GiB, 32–256 processes, and 30–3,600 seconds. The launch deadline is an additional upper bound. A plan supports either candidate platform; the current trusted execution route selects linux/amd64. Planning arm64 does not prove an arm64 build.

## Artifact preflight CLI

Run `python3 -I build/native/verify.py`. The stdin request contains exactly `contract`, `kind: VerifyManifest`, `synthetic`, `root`, `manifest_path`, `manifest_sha256`, `profile_id`, `registry_path`, `lock_path`, and `identity`. `manifest_path` is relative to the absolute artifact root. Verify the raw manifest digest before accepting its contents, then require the exact requested identity and profile binding. The identity fields are `run_id`, `operation_id`, `nonce`, `head_sha`, `base_sha`, and `verifier_source_commit`.

Return exactly `{"valid": true, "reason": "..."}` with exit 0 for valid input, or `{"valid": false, "reason": "..."}` with exit 2 for invalid input. The reason is a nonempty English string. Verify every reference and all structural/cross-field invariants in the normative manifest definition. The manifest's top-level fields are exactly `schema_version`, `task_id`, `contract`, `evidence_kind`, `plan`, `plan_sha256`, `builds`, `identity`, `image_inputs`, `host_tools`, `module_closures`, `os_closures`, `profile_bindings`, `updater`, `cleanup`, `commands`, and `runtime`. The JSON Schema must declare this closed versioned root and describe the nested contract.

Synthetic mode permits an explicitly synthetic manifest for protocol controls. It does not relax hashes, coverage, identity, profile, or cleanup requirements. Actual mode rejects synthetic evidence. Actual acceptance also requires the trusted driver's independent ELF, Go graph, signed package, OCI, command, source, and cleanup checks; a product preflight pass alone is insufficient.

## Updater CLI and source transport

Run `python3 -I scripts/refresh-versions check|update --registry PATH --active-lock PATH --expected-lock-sha256 HASH --report PATH`. Update mode additionally requires `--output PATH` for a separate candidate. Exit 0 only for complete successful discovery; exit 1 with an incomplete report for unavailable, malformed, or unresolved sources; exit 2 for invalid arguments, a stale expected active-lock hash, or an output that could overwrite an active input. Check mode and all failed modes create no candidate. Never modify active inputs or separately governed external locks, including rewriting identical bytes.

The optional `--fixture-transport PATH` selects an explicit offline source transport. Its closed object contains integer `schema_version: 1`, `synthetic: true`, and `observations`. Each observation has `url`, integer `status`, string-to-string `headers`, a file `body` reference relative to the transport's directory, and numeric Unix-seconds `observed_at`. Resolve official source requests by exact URL, including pagination. Retain output report bodies as references relative to the report directory. This mode marks the report and any candidate synthetic and never satisfies live-source acceptance.

GitHub may paginate a configured `/repos/{owner}/{repo}/...` endpoint through
`/repositories/{id}/...`. Retain an HTTP 200 metadata observation from the exact
configured `https://api.github.com/repos/{owner}/{repo}` endpoint before using
that alias. Its positive integer `id`, matching `full_name`, and canonical named
API `url` bind the identity; both strings must match the configured spelling exactly. Only that
numeric ID with the original endpoint tail and origin is allowed. The trusted
validator also authenticates metadata when a report includes it without an
alias, and independently refreshes used metadata during live verification.
Keep the 64-page limit, loop and ambiguous-next-link rejection, and unchanged
path/origin rules for non-GitHub sources. A numeric URL alone is not identity
proof, and metadata from another repository endpoint is not a substitute.
Loop detection uses the bound repository/endpoint identity and normalized query
ordering, so changing from a named URL to its numeric alias or rearranging query
parameters cannot make the same page appear new.

`UpdateReport` contains exactly the fields in `REPORT_KEYS`: its version/contract/mode/synthetic status, registry and before/after active-lock hashes, completeness and observation time, all 41 component rows, source observations, candidate reference or null, generated-output owners, and external-lock records. Each component row has `id`, `current`, `latest_stable`, `selected`, `compatibility: NOT_RUN`, `notices`, `blocked_reasons`, and `sources`. An incomplete report has no candidate and at least one concrete blocked reason. Complete check mode exposes proposed pins but creates no candidate file.

Use official release/tag, Go download, Docker registry, raw source, and Debian channel inputs as defined in the registered source rules. Follow pagination; resolve annotated tags; honor namespaced generator tags; derive native-owned replacements from the selected owner's go.mod, including valid owner-selected pseudo-versions. Apply Debian version ordering, preserve both candidate architectures, and verify package/image/source byte bindings. Live observations must be fresh; synthetic timestamps remain explicitly synthetic. The trusted runner independently refreshes live observations and authenticates retained Debian signatures with the pinned image-owned verifier/keyring.

Update mode writes a `PREPARED_CANDIDATE` tied to the registry and previous active-lock hashes. It has `release_ready: false`, `active_lock_changed: false`, the exact selected component objects, all existing profiles with build/runtime reset to NOT_RUN, and generated outputs still pending. Neither old runtime evidence nor an untested deployment tool or architecture is promoted by this operation.

## Execution scope

The five native/support clients are Geth, BSC, Bor, Prysm, and Heimdall. The ten separate module roots additionally include NATS server, gRPC Go, NATS Go, Protobuf Go, and grocksdb. Full compiler/runtime OS closures belong to the Go and Debian images. Gateway/reader images, bootstrap manifests, and fixture contracts retain their later task owners.

The current execution route builds linux/amd64 and binds every one of the seven network profiles explicitly. Arm64 build, ABI, and runtime results remain NOT_RUN. The selected descriptors are candidates, not established support. Record actual host Docker/Git/Python/GitHub CLI evidence separately from candidate deployment-tool versions. One actual fixed build and complete immutable inputs are required; a second forced build is not mandatory, and `repeat_build` remains NOT_RUN without that execution. Native chain execution and NATS delivery remain separate gates.
