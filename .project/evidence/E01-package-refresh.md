# E01 build-package candidate refresh

Issue: [E01-01.builds, #11](https://github.com/sigaev-pro/bc-indexer/issues/11). Author: root, session `d95-package-refresh-author-001`. Base: `fe14767132587d93bac502a7782606879c1a962a`. This is a metadata block under D95; the parent task remains incomplete.

## Finding and change

The 2026-11-06 candidate pinned mutable Debian Security index URLs to hashes that no longer match their current bytes. Keeping that input would correctly fail the exact-hash build check before compilation. Refresh both candidate security indexes and the latest `zlib1g-dev` security package. Preserve native source versions, the registry, all seven profiles, the other 40 component selections, pending constraints, and every runtime verdict. The candidate ID identifies this refresh; its original `base_commit` still authenticates the preserved specification baseline. Original BOOT-06 inputs remain in Git history.

On 2026-11-07, root independently downloaded and decompressed the official indexes. Their bytes match the retained real observations from direct run `run_D95_builds_1794077084`, operation `72685b99f76898cf46c51efccbdb26a9`. That run was stopped for a separate Docker release-selection defect; its overall result is not accepted. This block checks the Debian observations directly and makes no claim about that run's updater acceptance.

- [amd64 security index](https://security.debian.org/debian-security/dists/trixie-security/main/binary-amd64/Packages.xz): SHA-256 `164725afc7af9fc86dfd019c09b84299dbdab681d3f086c94a411bf391b3bff0`, 299972 bytes.
- [arm64 security index](https://security.debian.org/debian-security/dists/trixie-security/main/binary-arm64/Packages.xz): SHA-256 `c3303803f7724416bbc7b2007ee087f3d18650d22eaf4eed3a3a129e39cafd52`, 300444 bytes.
- Both records select `zlib1g-dev` version `1:1.3.dfsg+really1.3.1-1+deb13u1`.
- amd64 package metadata: SHA-256 `88ebb839a7a54c97dceb647e2a2bfa2451d6f440801ceef3d56f654022637eb9`, 919568 bytes.
- arm64 package metadata: SHA-256 `b8f6740af90accda9a571ae7f3e5b0a40a83c2e9d095f4cab56b92b8ae744757`, 916416 bytes.

For reproduction, fetch each official index, calculate SHA-256, decompress XZ, and read the complete stanza with `Package: zlib1g-dev`. Compare its Version, Architecture, Filename, Size, and SHA256 against the candidate. Reject changed bytes; a later repository update requires another explicit candidate refresh. Do not disable hash or signature checks to keep an old candidate running.

## Checks and limits

`python3 .project/bootstrap/components/verify.py --self-test` passes: 41 components, seven profiles, 11 open constraints, eight pending outputs, and all 54 invalid cases rejected. `build_contract.expected_plan` accepts both candidate platforms with the new exact package revision. Documentation passes with 108 documents, 1104 local links, 84 catalog entries, and 16 normative mirrors. Whitespace checks pass.

An initial edit also changed `base_commit`; validation correctly rejected three preserved baseline-file hashes against that newer commit. Restoring the original baseline commit resolved the failure. The documentation checker initially rejected the new unstaged evidence link; staging the file resolved it. Both final checks were rerun and passed.

The package payloads are not marked verified. Cryptographic InRelease verification, package-closure authentication, actual compilation, arm64 execution, profile admission, and NATS runtime remain NOT_RUN or pending their existing owner. The subsequent full build must verify signatures and hashes using the pinned image-owned verifier before package installation. Updating a metadata candidate does not change an active release, skip a gate, or establish platform support.
