# E01-01.builds: Finite materialized preflight budget

This scoped trusted-orchestration repair starts from accepted
`5edbe3e5feb46fc381520b36d6c5abd97fdbaf9f`. It supports issue #11 and parent
draft PR #107. It changes no product code, public schema, client pin, native
recipe, or original v2 requirement.

## Retained failure and measurement

Full operation `run_D95_builds_1794109506 / c2f5e8372cde4279d846d9f68488`
compiled all five native clients, verified all ten module closures, and passed
initial trusted materialized validation before its first actual product
preflight exceeded the unconditional 10-second control limit. Its result
SHA256 is `20fd70567be575280454b300e9720057325a2e958659018f2dac1549de73d5e3`.
The coordinator confirmed cleanup of the supervisor, worker, all 54 recorded
groups, and owned containers. Original artifacts and the failed result remain
unchanged. Parent acceptance and downstream task #12 remain pending.

The retained manifest has 13,041 reference occurrences and about 8.526 GiB of
declared referenced data. A separate bounded measurement of the unchanged
product preflight took 33.228 seconds and correctly returned exit 2 with
`Artifact bytes changed`. Its result SHA256 is
`009ab35209815209ce8f3e0ff01d51a8874cfd1042d1969a70206732e001934d`.
Failure cleanup appended to the old journal after the intermediate manifest was
written: the manifest references 48,724 bytes at
`83f99a4dc482631b5260acbdab3b084b1a06c75c652e8dc8c4cfce4d0eb90c8e`,
while the final journal has 52,910 bytes at
`a8d3cb85779f3140ff115b3243c8a20b3fffdfb3de574df3b2da0ccfaa826fdb`.
The intermediate manifest hash remains
`dfa5b303659d83454eb8ad5494eecdcfd289f39445e477101493dd69a845d495`.
This is correct stale-reference rejection, not positive preflight evidence or
a second product defect. This author read the original result and intermediate
manifest hashes without modifying either.

## Change and controls

The adapter now names the small 10-second cap and the actual-materialized
300-second cap. Only the trusted final profile loop explicitly passes
`materialized=True`; all plan and synthetic controls keep 10 seconds. Request
fields cannot authorize the larger budget. The adapter's existing outer-deadline
clipping, monotonic command supervision, process ownership/cleanup, protected
input fingerprints, response validation, and artifact checks are unchanged.
The final trusted materialized check still runs after all product preflights.

The direct red regression recorded seven actual-profile calls at 10 seconds
instead of the required 300. All six focused methods then passed in 7.744
seconds, covering trusted budget selection, unchanged synthetic/plan budgets,
the actual-mode synthetic rejection control, invalid internal choices,
candidate size/kind/time claims, outer deadline clipping, real supervised
timeout and confirmed group cleanup, expired-deadline no-dispatch, malformed
responses, and post-candidate artifact mutation. Small disposable processes and
synthetic artifacts were used; no native compiler, container, or live source
acquisition ran. All 178 affected build tests then passed in 68.105 seconds
(68.535 seconds including the wrapper), with two existing opt-in Docker skips
and no ResourceWarning. Documentation checks passed 115 files, 1,122 local
links, 84 catalog entries, and 16 normative mirrors; original v2 bytes remain
unchanged. Whitespace checks passed. Remote exact-head CI and independent review
remain required after publication.

## Separate diagnostic boundary

The coordinator's proposed derivative diagnostic copies referenced files without
hardlinks, refreshes only the copied cleanup reference, and preserves the old
identity and underlying data. It must identify itself as derivative preflight
timing evidence. It may invoke
`commands.preflight(identifier, copied_manifest, copied_root, profile_id, materialized=True)`
for each profile under its own original finite deadline. This author does not
create or run that diagnostic. Such results do not repair the failed operation,
establish new trusted native provenance, or complete issue #11.
