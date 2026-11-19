# Prerequisite controls audit

These controls test Python rejection logic, not native acceptance.

| Guard | Negative control | Single-check mutation |
| --- | --- | --- |
| Schema integer/version and exact scope | Boolean/wrong version and full-import scope | Each semantic comparison removed separately |
| No import/public/complete initialization credit | Each NOT_RUN field changed to PASS | Shared boundary comparison removed |
| Required ordered method rows | Missing, non-object or wrongly named row | Method comparison removed |
| Independent initialization before/after | Every selected state field changed separately | Each whole snapshot comparison removed separately |
| Independent deposit before/after | Every selected state field changed separately | Each whole snapshot comparison removed separately |
| Native transaction identity format | Malformed hash | Hash format check removed |
| Complete pinned bytecode map | Empty map or changed validator digest | Exact digest-map equality removed |
| Current-fork context | Wrong height or timestamp | Each context comparison removed separately |
| Prepared tool kind/native source | Wrong kind or source revision | Each identity check removed separately |
| Prepared probe/driver/provenance binding | Wrong reference for each input | Each binding removed separately |

Structural container/length guards prevent malformed reports from reaching
field access. They are covered by negative input controls; the mutation table
targets the independent semantic comparisons, not redundant structural checks.
Existing preparation filesystem, deadline, Docker cleanup, source archive and
compiler guards are retained from the accepted component driver/harness.
This audit does not reclassify their earlier native proof as a new run.

The first mutation attempt retained a real failure: state dictionaries shared
an alias, and tool identity controls reached an unrelated Git-state rejection.
The corrected tests use distinct snapshots and mock only unrelated record/
binary/runner effects. Their baseline still uses the real report/identity guards.

Retained evidence under this isolated worktree's `.project/automation/runs/`:

- `bsc-modern-init-mutants-01/mutants.json`, source `abebcd9`: FAIL,
  baseline PASS, 11 killed and 7 survived; SHA256
  `081164b7249f73e8238ef5ee60fdf08945c8e0306f85e89903f879585c2629bc`.
- `bsc-modern-init-mutants-02/mutants.json`, source `8416b81`: PASS,
  baseline PASS, 18 killed, no surviving/equivalent/build-failed mutants;
  SHA256 `0553e36a9506e627b781edef8828304ac66b991b3a840ee4d94a235a23a0983a`.

Both reports retain commands, source identity and individual logs. Command:

```sh
python3 test/mutate.py test/type_paths/bsc/modern_init/native.mutants.json --output OUTPUT
```

The report hashes bind the numeric claims above. No native execution result is
asserted here. Full current-fork import remains outstanding.
