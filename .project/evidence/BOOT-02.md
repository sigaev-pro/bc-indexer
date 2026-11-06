# BOOT-02 agent configuration

Task: [BOOT-02](https://github.com/sigaev-pro/bc-indexer/issues/2). Date: 2026-11-06.
Dependency: BOOT-01 accepted at `145f91e5c8cb7b855fc70db59a9decb065b41842`.

## Configuration

Eight adapted project roles are stored in `.project/agency/`. The source lock pins agency-agents commit `b859c515d813efcfc4755d3471dd9e7686c7706d`, nine source Markdown files, and their SHA-256 digests. The upstream MIT license is preserved byte-for-byte. The roles use explicit `codex exec` prompts; no global agents or custom-agent discovery are required.

Every rendered prompt contains project rules, the shared execution contract, the selected role, the task handoff, and a digest of its instruction inputs. Handoffs include exact commits, allowed paths, read-only mode, acceptance criteria, session/run identity, and a saved stage limit. D86 cannot open E01. Reviewer sessions are read-only and separate from implementers. The controller remains responsible for actual path enforcement, leases, evidence, and acceptance.

## Actual checks

- `python3 .project/agency/roles.py verify`: PASS, eight roles.
- `python3 -m unittest discover -s .project/agency -p 'test_*.py'`: six tests pass. They cover every role, unknown roles, missing criteria, stage escalation, reviewer writes, and unsafe paths.
- Independent review of candidate `9cb609ec4ecd990ba8ad758c153e5abfb678ec1c` found that `./` and `././` bypassed the root-path rejection. The loader now rejects the normalized root, and both counterexamples are included in the boundary tests. The earlier candidate is not accepted; the corrected commit requires fresh review and probes.
- Initial real probe with host Codex 0.145.0: FAIL, server rejects the owner's configured `gpt-6-astra` model as requiring a newer CLI. This failure is preserved; no model substitution was used.
- Installed exact `@openai/codex@0.167.0` under the ignored project cache. Its package integrity and darwin-x64 package integrity match the recorded toolchain lock. The global CLI, authentication, and model configuration were unchanged. Version command returns `codex-cli 0.167.0`.
- Real read-only Reviewer and DevOps probes with the project CLI: PASS. Each returned the exact schema values for its role, American English, BOOT-only stage, no self-acceptance, and no E01 launch. Both completed their turn with exit code zero; the response values were checked separately.

Reproduce the real checks with `python3 .project/agency/probe.py --role reviewer` and `--role devops`. Each result records the exact base commit, role digest, and unique session ID. Final candidate results and independent review belong in issue #2 and its PR. These probes establish prompt delivery and model access, not unrestricted filesystem access, container capacity, or product acceptance. BOOT-04 verifies those separate capabilities.

Source references: [pinned agency-agents](https://github.com/msitarzewski/agency-agents/tree/b859c515d813efcfc4755d3471dd9e7686c7706d), [Codex release 0.167.0](https://github.com/openai/codex/releases/tag/rust-v0.167.0), and [official CLI guidance](https://developers.openai.com/codex/cli).
