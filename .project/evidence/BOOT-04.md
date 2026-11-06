# BOOT-04 runner and access verification

Task: [BOOT-04](https://github.com/sigaev-pro/bc-indexer/issues/3). Date: 2026-11-06.
Dependency: accepted BOOT-01. BOOT-02 supplies the verified project CLI and roles.

## Capabilities proved

- Git 2.50.1, gh 2.101.0, Python 3.13.5, and project-local Codex 0.167.0 run. Codex uses the owner's existing ChatGPT authentication and configured model. `gh api user --jq .login` identifies `sigaev-pro`; repository push access and GitHub Actions access were checked without printing credentials.
- Docker client/server 29.8.2, context `orbstack`, are reachable. The daemon was initially stopped. The owner approved starting OrbStack; no existing containers, images, volumes, or runtime configuration were removed or changed.
- `python3 .project/automation/check_environment.py --container` passes. The existing image `alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce` ran with no pull, no network, a read-only root, user 65534, dropped capabilities, no-new-privileges, and bounded memory/CPU/PIDs/tmpfs. It verified its user, root permissions, absence of a default route, and temporary-file IO. This image is a capability probe, not a product dependency candidate.
- A real Codex session used `workspace-write`, `approval_policy="never"`, and `sandbox_workspace_write.network_access=true` in the dedicated `boot/04-access-probe` worktree. It wrote only `.project/evidence/BOOT-04-access-probe.txt`, committed it, read remote main, and read the Docker version. Actual file contents, changed paths, commit, and clean status were verified independently of its JSON response.
- A second session used the same settings to push the existing probe commit without force through the existing gh credential helper. No prompt, new commit, file edit, or auth/config change was needed. The remote ref was verified afterward.

Durable access proof: commit [`24268af0f6cc384cc759174177e43b8038dc075c`](https://github.com/sigaev-pro/bc-indexer/commit/24268af0f6cc384cc759174177e43b8038dc075c) on `boot/04-access-probe`. Session IDs: `writer-access-probe-01` and `publish-access-probe-01`. Role revision: `fedc63d3525f26be57b3e939c02a3f29e842c9ce910ba41d2b251a46a3f6f01d`. The branch is evidence only and is not merged into product code.

## Scope and limits

The host is Darwin x86_64 with 16 logical CPUs and 16 GiB RAM. Docker reports 16 CPUs and 8,398,995,456 bytes of RAM. Available disk was approximately 232 GB during the check. These measurements do not prove capacity for all seven profiles or the 1800-second standard-run budget. E01/E07 must measure a configured RunPlan; begin with bounded, sequential workloads.

Docker reports `DOCKER_INSECURE_NO_IPTABLES_RAW is set`. The offline probe does not certify isolation for exposed services or production. No host mounts or published ports were used. Existing unrelated Docker resources were preserved.

System idle sleep is configured at one minute on battery and fifteen minutes on AC. A short `caffeinate -i -s` probe created the expected idle/system sleep assertions and exited cleanly. The macOS launcher now holds that guard only for its process lifetime. Lid close, shutdown, power loss, network loss, and account quota remain external limits; restart recovery belongs to BOOT-03.

Testnet signers and funds are not provisioned. They are separate prerequisites before signed public-smoke actions, not authority to spend mainnet funds. Secrets remain outside Git and reports. The probe does not claim product/runtime acceptance or controller readiness.

## Reproduction

Run `check_environment.py --container` for the bounded host/container checks. For the access probe, create a new task-owned worktree from an accepted main commit, provide the DevOps role with only the probe file as an allowed path, and use the explicit CLI settings above. Require actual command results, a commit containing only that file, and a verified task-owned remote ref. If any operation is denied, report BLOCKED_ACCESS; do not change security controls or route around the denial. The exact push uses `git -c credential.helper= -c 'credential.helper=!gh auth git-credential' push origin COMMIT:refs/heads/PROBE_BRANCH`.
