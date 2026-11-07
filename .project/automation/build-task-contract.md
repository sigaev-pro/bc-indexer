# Isolated build acceptance interface

Status: DRAFT for AUT-03, [issue 98](https://github.com/sigaev-pro/bc-indexer/issues/98). This document does not enable a recipe or accept E01-01.builds. The implementation, independent checks, and exact-head review must resolve and freeze this interface before configuration handoff.

## Existing requirements

The product task is [E01-01.builds](../docs/TASK_DECOMPOSITION.md#task-e01-01-builds), issue 11. Its assigned criteria are AC-OPS-10-01, AC-OPS-10-03, and AC-DEV-03-01. D83 also assigns the standalone version updater to this task. The accepted [testing contract](../docs/TESTING_CONTRACT.md) and specification remain authoritative.

The task produces separate source, patch, compiler, dependency, and artifact records for native clients and supporting inputs. It must reject an incorrect artifact or profile before native start. Native client dependency graphs remain separate. The updater covers the complete versioned component registry, discovers official stable releases, creates exact candidate locks, and never promotes an active lock before the required later gates.

A compiled binary is evidence of a build. It does not prove profile bootstrap, consensus compatibility, capture correctness, testnet transactions, NATS delivery, or a full stage gate. Every such result retains its own owner and NOT_RUN state until executed. Metadata acquisition is not compilation. A generated report cannot substitute for materialized output bytes or execution evidence.

## Trusted boundary

Use a task-specific adapter selected by the exact task ID. Preserve the existing evidence adapter and already retained receipts. There is no fallback that accepts an unknown product task through bootstrap CI.

Freeze the build contract, independent driver, oracle inputs, task recipe, accepted component registry, and candidate-lock inputs in the reviewed source snapshot. A source or recipe change requires the existing safe handoff. The proof binds the exact candidate head and base, accepted verifier source, command requests and results, materialized output hashes, and separate independent review. Public proof binding and protected merge checks remain mandatory.

The implementation must retain negative proof that stale task IDs, candidate hashes, base commits, source packs, profiles, output files, and public bindings are rejected. Existing evidence-task proof checks must continue to pass unchanged.

## Product surface to define and verify

Keep product outputs in `build/native/`, updater code under `scripts/refresh-versions` and a clearly named helper directory if needed, independent product tests under `test/builds/`, the task evidence report, and the status document. The final recipe must list exact required files and commands. Do not grant write access to the trusted automation or bootstrap inventory through this product task.

The build interface must provide explicit operations for preparation, building a selected component, validating artifacts, and reporting a complete requested build set. Every operation identifies its platform, selected lock, source directory, output directory, and resource/time bounds. Inputs must not depend on an author's hidden running node or an unrecorded local compiler. Test fixtures must be explicitly synthetic; they cannot be used as actual build evidence.

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

Run real build commands through owned supervision with the original D92 absolute deadline. Preparation is outside the 1,800-second standard runtime-test budget; it is still inside the launch deadline. Each command needs a bounded timeout and resource limits. Do not erase a failed attempt with a successful retry.

Docker daemon resources outlive the host CLI. Record immutable owned container IDs and task/run labels before execution. On success, failure, interruption, or timeout, reconcile and clean only those resources, then confirm their state. A killed host process group alone is insufficient. Retain uncertainty and block further dispatch if ownership or cleanup cannot be proved. Never use global prune or touch unrelated images, containers, or volumes.

The independent driver must combine behavioral failure mutations with actual build provenance checks. A self-reported PASS, a matching version string, a closed issue, or a fixture lookup table is insufficient. Commands, materialized outputs, source inputs, and their exact identities must agree. The adapter may be tested with synthetic fixtures before product implementation, but that does not accept an actual build.

## First local preparation

The canonical checkout already has five clean pinned native source caches and three pinned Linux amd64 images. They are preparation only. The observed Docker host is Linux x86_64 with 16 CPUs and about 8 GiB RAM; native builds should run sequentially within explicit limits. Recheck capacity before execution. No native source has yet been built or started during D92.

Finish the concrete interface and independent tests in AUT-03, obtain exact-head review and protected acceptance, then safely hand off the accepted adapter on the existing D92 run. Product implementation and acceptance remain separate from this maintenance task.
