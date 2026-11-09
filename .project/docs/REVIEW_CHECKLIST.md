# Review checklist

The author applies this checklist before requesting review, and each Spec and
Standards reviewer applies it to the exact head. It does not replace either
review axis, the issue's acceptance criteria or [D83](DECISIONS.md) retry rules.
Every item comes from findings that cost an extra review round in PRs #128
through #130.

## 1. Every number is backed by a file

- Generate run figures with `test/harness/evidence_summary.py RUN_DIR`; do not
  copy hashes, timings, counts or byte totals by hand. It verifies the run's
  absolute references (its help states the exact scope) and prints nothing on
  a mismatch.
- A figure the tool does not print (for example a test count from a local
  command) cites a retained log; pass that log with `--test-log`.
- Quote counts from the retained record, not from memory or an earlier run.

Past findings: the evidence named 9 ProfileManifest tests instead of 8 (#129);
"sequences 0–5" instead of 1–5 (#128); a mutant claim with no retained record
(#130).

## 2. Every new branch has a control

- Each new `if`, `require`, conjunct, loop bound and error path has a test that
  fails when that one check is removed or weakened.
- Prove it with `test/mutate.py TABLE --output RUN_DIR`. Keep the mutant table
  as `*.mutants.json` next to the code it targets, retain the run directory and
  cite its report.
- A changed head needs a fresh mutation run for the changed checks.

Past findings: 10 of 20 `initial_sample` mutants survived (#128); 34 of 90 and
then 16 of 25 runner mutants survived (#129); three queue fields, the JSZ status
check and the leader fingerprint had no control (#130).

## 3. Signals and errors leave nothing behind

- An exception or signal at any point still stops owned child process groups,
  runs teardown, restores handlers, releases locks and writes the run record.
- Check the kill and reap paths for `PermissionError` and transient states:
  Darwin returns EPERM from `killpg` while a group leader is still exiting.
- Test each path with a real signal or an injected error, not only by reading
  the code.

Past findings: an exception after admission skipped teardown and the record; a
signal orphaned the child group; a signal during the final record write escaped
the `finally` block (#129).

## 4. Nothing is written into an output the run does not own

- A run creates its output directory and refuses an existing one; a refused
  run writes nothing there, not even a FAIL record.
- Cleanup removes only resources carrying this run's own nonce or label.
- Review and evidence tools only read retained runs.

Past findings: a run refused for an existing output wrote a FAIL
`run-record.json` into it, which a D83 retry could cite (#129); teardown
removed every `extractor.harness` container on the host (#129).

## 5. Every survivor is explained

- A mutant must compile before it can count: `test/mutate.py` reports a build
  failure as `BUILD_FAILED` and fails, never as a kill.
- A kill names the failing test or control; keep its assertion lines.
- A survivor passes only if the table labeled it equivalent, with a reason,
  before the run. Any other survivor needs a new control or a stated limitation.

Past findings: mutation record 08 counted 10 compile failures as kills (#130).

## 6. Claims do not overreach

- State only what the retained evidence proves: name the commit, profile, run
  and scope. A partial block does not close its parent issue.
- Cite the authoritative source for a fact (pinned source code, a decision),
  not samples that happen to agree.
- Record an owner decision only with the owner's actual words; never widen or
  narrow it during implementation.

Past findings: D96 text narrowed without owner acceptance and an implementer
sentence presented as an owner answer (#129); raftz field names justified by
samples that omit both the old and the new names (#130); mutation coverage
wording that overstated the result (#129).
