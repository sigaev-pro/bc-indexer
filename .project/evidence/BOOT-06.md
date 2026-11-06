# BOOT-06 component inventory evidence

Task: [BOOT-06](https://github.com/sigaev-pro/bc-indexer/issues/6).
Date: 2026-11-06. Base: `6b8e1dd561d8be39f80b00ef5860d35b7db95d24`.
Scope: initial registry and metadata-verified candidate; no active lock or specification changes.

## Artifacts

- [Registry](../bootstrap/components/registry.json): 41 components with official sources, stable/bundle policies, exact pin methods, and profile scope.
- [Candidate lock](../bootstrap/components/candidate-lock.json): exact commits, source-file hashes, artifact checksums, OCI index/platform digests, per-platform package versions, all seven profiles, 11 open compatibility constraints, and eight pending generated outputs.
- [Guide](../bootstrap/components/README.md): candidate choices, evidence limits, and promotion boundary.
- [Offline validator](../bootstrap/components/verify.py): structural checks and unsafe-input rejection; no downloads or runtime admission.

## Acquisition performed

Read official GitHub releases and resolve selected tags to commits, including annotated tags. Read immutable `go.mod`/`go.sum` files and hash their exact bytes. Separate native dependency inputs and generators. Select stable candidates by their component namespace and semantic version, not by the publication time of a maintenance backport. Official release/tag URLs are retained per component.

Read official Go download metadata and release-asset checksums for Go, Prysm, protoc, protoc-gen-go, Solidity, and the NATS chart. These checksums are metadata evidence; those binary payloads were not executed or claimed as downloaded. Read the NATS Chart.yaml bytes and verify chart version and appVersion independently.

Fetch the official Docker OCI indexes for NATS, Debian, and Go. Their computed SHA-256 values match the registry's content digests. Record the Linux amd64/arm64 descriptors without claiming platform execution. Read and hash Debian stable, security, and update package indexes for both architectures. Signed-index verification and full dependency resolution remain pending E01.

Download the two small BSC v1.8.2 bootstrap ZIPs into memory, hash them, and inspect their filenames and genesis JSON. Mainnet is chain 56 and testnet is chain 97. The API checksums match bytes; release prose checksums do not. Record exact archive/genesis hashes. Native init and effective fork configuration remain NOT_RUN.

## Checks

`python3 .project/bootstrap/components/verify.py --self-test` exits 0: structure PASS; 41 components, seven profiles, 11 constraints, eight pending outputs; all 17 unsafe mutations rejected. Runtime is NOT_RUN and release_ready is false.

The mutations cover floating versions, mutable commits, missing/duplicate components, missing profiles, false runtime PASS, stale registry hashes, malformed artifact hashes, mutable image pulls, mismatched NATS versions, enabled untracked sidecars, crossed native dependency inputs, wrong Polygon L1 ownership, missing generated outputs, unofficial artifact hosts, false release readiness, and duplicate package indexes.

The validator checks baseline source hashes against the exact base commit, the immutable original v2 hash, and generated-output task IDs against the task catalog. Additional pre-commit checks verify changed-file scope, local Markdown links, JSON/Python parsing, English-authored text, and `git diff --check`.

## Result and limits

Proposed status: READY_FOR_REVIEW. Independent review and task acceptance are pending. The registry is ready as a build-planning input; the selected clients and libraries are not an accepted runtime combination. Current source candidates differ from the specification baseline, which remains unchanged. E01/E06 own build outputs, admission, signed closures, and profile manifests; E07 owns full applicable acceptance. No product process, testnet transaction, deployment, or active version promotion ran in this task.
