# Component candidates

BOOT-06 records the initial component inventory and exact candidate inputs checked on 2026-11-06. It does not replace the specification's existing source pins or promote a runtime release. The full updater remains an E01/E07 task under [TESTING_CONTRACT](../../docs/TESTING_CONTRACT.md).

`registry.json` defines each component's official source, stable channel, pin method, profile scope, update rule, and verification requirement. `candidate-lock.json` binds that registry by SHA-256 and records 41 components, seven profiles, 11 open compatibility constraints, and eight generated outputs that later tasks must produce. The baseline section identifies the unchanged specification bytes and existing documented candidates. Separate agency source/tool locks are referenced without changing them.

Run the offline check from the repository root:

```bash
python3 .project/bootstrap/components/verify.py --self-test
```

A structural PASS means the inventory is internally consistent. The result always reports runtime NOT_RUN and release_ready false. The command does not access the network, download packages, run a node, or modify files. Its negative checks reject missing profiles/components, floating pins, bad hashes, mutable image references, crossed native dependency inputs, wrong Polygon L1 ownership, unlocked sidecars, stale registry hashes, and false acceptance claims.

## Candidate choices

The native source candidates are Geth 1.17.10, Prysm 7.3.1, BSC 1.8.2, Bor 2.11.0, and Heimdall 0.13.2. Source tags resolve to exact commits. Common transport/storage candidates and each generator have independent versions. In particular, protoc-gen-go-grpc is 1.6.2; it is not the grpc-go SDK version.

Use Go 1.27.3 as the candidate compiler and the pinned official Go/Debian images for build preparation. Use Debian 13 stable GCC/G++ 14, CMake, Make, and the listed system development packages. This avoids introducing another compiler distribution. The package list includes both stable and security updates; OpenSSL development packages select the newer security revision. Full signed OS closures and final runtime libraries remain pending. Package versions differ by architecture where Debian publishes different revisions.

Linux amd64 and arm64 descriptors are recorded as build candidates. Descriptor availability does not establish supported execution. Every selected platform still needs its build, ABI, and runtime proof. The solc candidate supports project-owned test fixtures; each fixture must specify its EVM target and verify the downloaded compiler's architecture.

NATS server and chart versions are independent pins. The candidate explicitly disables chart `reloader`, `natsBox`, and `promExporter`; none is required for the agreed data-delivery boundary. Enabling one later requires its own official source/update rule, immutable image pin, and tests. JetStream configuration, account identity, and publication guarantees still come from the existing specification.

## Compatibility that remains open

Hoodi requires the releases that cancel the previously scheduled Amsterdam/Gloas activation. Bor 2.11.0 is needed after Uburu on both Polygon networks, and its changed StateSync behavior needs new capture proofs. BSC testnet already runs the Jenner schedule; mainnet schedules it for December 8. The case corpus must cover the applicable new paths and fork boundaries.

Bor and Heimdall retain different Cosmos/CometBFT replacements. Their source `go.mod` and `go.sum` files are independently hashed. These are upstream inputs, not resolved build closures: `go.sum` can contain unused versions. Adapter integration and generated bindings must be resolved and tested in each native module. The newer grpc-go compiler requirement and the RocksDB 11.13.2/grocksdb 1.11.0 pair also need build and behavioral proof.

Every profile has its own open constraints and NOT_RUN results. Polygon mainnet keeps a dedicated Ethereum-mainnet L1 pair; Amoy keeps a dedicated Sepolia pair. Neither shares the main Ethereum extractor's instance. Current client releases do not prove pruning, capture, finality, recovery, or testnet compatibility.

## Evidence and promotion

Evidence types distinguish GitHub release/tag metadata, hashed source bytes, artifact checksums supplied by the release API, OCI index bytes, package-index bytes, and downloaded BSC archives. Most binary payloads have not been downloaded. OCI platform descriptors were read from verified index bytes; platform layers were not executed. Debian index signatures still need verification during E01 preparation.

The BSC release prose has incorrect ZIP checksums. The API digests match the actual downloaded mainnet/testnet ZIP bytes in this lock. Both bundled genesis files omit `jennerTime`; validate effective native fork configuration after init instead of trusting archive presence.

Generated patched-client/supporting images, project binaries, complete module/OS closures, seven profile manifests, bootstrap payloads, and fixture bytecode have explicit pending owners. Exact content hashes prevent silent substitution, but source availability must still be checked and required inputs cached during preparation. A missing source, changed digest, failed build, or incompatible profile blocks promotion. It does not justify an older silent fallback or a compatibility PASS.

The future `scripts/refresh-versions` must check official sources, create a separate candidate, produce an UpdateReport, run the affected gates, and promote atomically. This offline validator is not that updater. See [BOOT-06 evidence](../../evidence/BOOT-06.md) for the checks performed here.
