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

## Actual derivative diagnostic

Both independent code reviews passed on exact head
`046d1cb0a45c3592c6cfc07742a6fc454b4381b0`; exact-head CI `42449578253`,
check `128635710293`, app `15368`, passed. Spec independently ran six
focused methods and a scaled timing control; Standards ran the same six
methods in a disposable exact-commit copy and a separate descendant/deadline
control. No review findings remained.

The separately reviewed helper `materialized-preflight-smoke.py`, SHA256
`19381ae1946f2ab2989ffda26b30a695de6c6b639cd3eb30b04f3705f590a02f`, ran
against that frozen source and product
`8c7f10ea83ba6c640a37e787ffeef4aa5f2c64d4`. Supervised operation
`run_D95_materialized_preflight_1794114608 / 47d65615515dbff21712af3558`
used a finite 1,800-second outer limit. Request-file SHA256:
`a1ae2bec231b516984169785b426fb62d0aefd1cf862daa97bf09af988849ced`.
The supervisor's canonical request digest is
`4b5216d85161979749fbc47225849281b2d58daf1882e5a12118017f98bc4503`.

The helper copied 12,870 unique references totaling 7,578,016,311 bytes in
39.880 seconds without hardlinks. It refreshed only the copied cleanup
reference to the final journal already described above. Seven product
preflights passed: Ethereum Mainnet 36.438 seconds, Sepolia 37.830, Hoodi
36.268, BSC Mainnet 36.831, BSC Testnet 36.816, Polygon Mainnet 37.333 and
Amoy 38.399. A separate manifest with a changed Geth binary hash returned
exit 2 and `Artifact bytes changed` as required.

The operation exited zero with complete cleanup in 326.506 seconds. Result
SHA256: `d4a3898618b98fb6810c63a638cee143273c6032945dfec49284eb70039997a5`.
Diagnostic-proof SHA256:
`70b58097d64033d911f0c597896e1778f2260cf170472889383882655821dab6`.
Root independently confirmed supervisor 2831, worker group 2845 and all
eight recorded candidate groups absent, with no containers bearing nonce
`33e73af1feaafca4498132d010fe0ee5882b1f21ce3e40fecbf5ffb7fea92302`.
The original manifest, command history and final cleanup journal retain
their pinned hashes. No original artifact was repaired or replaced.

This establishes actual product-preflight timing and rejection behavior on
an explicit derivative copy. It does not replay complete native provenance,
replace the failed operation, complete parent #11 or establish native/NATS
runtime acceptance. Independent Spec and Standards diagnostic reviews both
passed: all eight retained control records bind, all 12,870 copied files
have separate inodes, original hashes remain unchanged and cleanup records
are complete. Standards also confirmed current process/container queries
are empty. No reviewer reran the diagnostic or changed artifacts.

The result checkpoint changes only this evidence and project status.
Documentation checks passed 115 files, 1,123 links, 84 catalog tasks and
16 mirrors; original v2 and executable code remain unchanged. Final
exact-head review and CI, protected repair merge, product synchronization,
and a fresh full operation remain required.
