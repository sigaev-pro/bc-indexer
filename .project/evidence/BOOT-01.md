# BOOT-01 baseline verification

Task: [BOOT-01](https://github.com/sigaev-pro/bc-indexer/issues/1).
Date: 2026-11-06. Publication target: `https://github.com/sigaev-pro/bc-indexer.git`, branch `main`.

## Pinned input

The approved development baseline is commit `eaa257234297a2d62f308a145ab64244bd82df83`. The starting working specification and English issue catalog are pinned by commit `4554628a6b47395f6fc10a9aef4bf4df5780a4fd`. The baseline publication retains both commits and their ancestors. D86 authorizes BOOT only and requires a stop before E01.

The immutable original `.project/evm-transaction-extractor-tz-v2.md` has SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`.

## Verified before publication

- `git remote -v` identifies only the selected repository. `git ls-remote origin` succeeds with no refs. The GitHub API reports the same public repository, default branch `main`, and size zero.
- The starting local branch is `master`, with 65 commits and 70 tracked files. The original development baseline is an ancestor of HEAD. No history rewrite is required.
- All 84 local task markers match 84 unique GitHub issues. Their dependency lists match after parsing the Markdown headings. Every dependency exists and the complete graph is acyclic.
- A scan of all 619 historical blobs found no private-key headers, GitHub tokens, AWS access-key IDs, OpenAI key patterns, or credentials embedded in HTTP URLs. Tracked paths contain no credential filenames. This is a pattern scan, not a claim to detect every possible secret.
- `.gitignore` excludes local credentials, signing files, run output, controller state, worktrees, native stores, and caches. `git check-ignore --no-index --stdin` confirms representative paths for each sensitive category.
- `git diff --check` passes. The runbook's Bash examples pass `bash -n`. The bootstrap launcher no longer starts the product controller.
- Documentation checks pass: all 16 normative mirrors, the full executor specification, 63 requirements, 217 acceptance cases, 68 product tasks, and local Markdown links/anchors remain consistent.

## Publication and safe retry

Publish the reviewed HEAD with `git push origin HEAD:refs/heads/main`, without force. Before the first publication, repeat `git ls-remote origin`. If a remote history has appeared, fetch it and reconcile through a normal branch/PR; do not overwrite it. A repeated push of the same commit is a no-op. Before adding later commits, use the normal PR workflow and preserve any existing remote work.

Publication is complete only when `git ls-remote origin refs/heads/main` equals the reviewed commit. Record that exact commit and the independent review in issue #1. This document records pre-publication checks; the issue carries the final publication result. Product and controller runtime checks remain NOT_RUN.
