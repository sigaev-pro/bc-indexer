# Working on extractor

## Project scope

D84 authorizes implementation of the accepted specification: native execution data through confirmed delivery to NATS. D64's earlier documentation-only restriction is superseded. Code, builds, runtime tests, and the test environment are allowed through the task and gate process. Production deployment requires separate authorization.

Create the complete issue backlog before product code. Complete BOOT, then E01 through E07 in dependency order. Follow [DEVELOPMENT_AUTOPILOT](../DEVELOPMENT_AUTOPILOT.md) and [ISSUE_CATALOG](docs/ISSUE_CATALOG.md) in `sigaev-pro/bc-indexer`. A written specification or an open issue is not proof that work is done. Change runtime NOT_RUN only when actual evidence supports the new result.

## Project language

All project-authored text must use clear, simple American English. This is mandatory for issue titles, descriptions, and comments; pull requests and reviews; code comments and docstrings; documentation and agent instructions; commit messages; test names and descriptions; CLI help, errors, logs, reports, and other automation output. Use English words for new human-readable identifiers where the external API does not prescribe the name.

Use direct sentences, familiar words, consistent technical terms, and American spelling. Avoid British expressions and spelling, regional idioms, slang, ornate phrasing, and vague promotional language. Write `behavior`, `color`, and `initialize`, not their British spelling variants. Keep claims specific and supported by evidence.

Preserve exact protocol fields, stable IDs, API names, commands, addresses, hashes, and original payload bytes. Do not translate data that the product must preserve or change quoted source evidence. Write the surrounding explanation in English. Existing historical sources are inputs, not templates for new prose; the original v2 specification remains immutable. This language policy does not require a bulk translation of historical files in an unrelated task.

Every writer must check the language before saving or publishing. Every reviewer must reject newly authored non-English text or unclear wording. Issue generation and updates must use the English ISSUE_CATALOG and preserve its task IDs and technical meaning. A character scan is a useful check, not proof of good English. Conversation with the owner may use the owner's language; project artifacts must use English.

## Start and handoff

At the start of work, after resuming, and before a handoff, read [PROJECT_STATUS.md](PROJECT_STATUS.md). It records the current task, verified results, and next action.

For planning, read [DECISIONS](docs/DECISIONS.md). For implementation, read the task, dependencies, and gate in [DEVELOPMENT_PLAN](docs/DEVELOPMENT_PLAN.md) and [TASK_DECOMPOSITION](docs/TASK_DECOMPOSITION.md). Use [CONTEXT](CONTEXT.md) for project terms. Provide these rules explicitly to every worker and reviewer, including sessions in separate worktrees.

Before a substantial task, record its ID and status in PROJECT_STATUS.md. Record accepted user decisions in DECISIONS.md as soon as they arrive. After the task, record the result, checks, and next action. Distinguish proposals from accepted decisions.

## Changes, verification, and commits

After completing a task or subtask, update the status documents, run the required checks, and commit all related changes. Move to the next task only after the commit succeeds. This rule also applies to documentation-only changes. Check `git status` and report the commit hash. If committing fails, record the stopping point and resolve the failure before continuing. Push only when separately authorized, including an explicit launch of the workflow that grants that permission.

Keep `evm-transaction-extractor-tz-v2.md` unchanged. The working specification is `evm-transaction-extractor-tz-v3-codex.md`. Update it from accepted decisions and keep unresolved questions visible.

Make and record technical decisions within the accepted requirements without asking the owner to repeat an existing approval. Do not guess changes to the user's guarantees or scope: record BLOCKED_DECISION and continue independent authorized work. An implementer cannot accept their own work. Require an independent review and checks for the exact commit. Use D83 for test retries and DEVELOPMENT_AUTOPILOT for orchestration attempts. Never advance through a failed or incomplete gate.
