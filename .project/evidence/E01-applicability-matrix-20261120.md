# E01 applicability/path matrix reconciliation — 2026-11-20

Independent read-only Spec investigator report, produced for the D108 E01
completion. Sources: PROJECT_STATUS.md (D108), DECISIONS.md (D82, D96, D98,
D100, D101, D106–D108), the D107 campaign report ("What must happen next"),
the five E01-05.type_paths* evidence files, the E01-04.early_faults evidence
files, the test/type_paths trees (main and worktrees), the shared handoff
catalog, and the live PR states.

## Family × obligation matrix

| Family | Obligation | Status | Evidence / PR | Accepted |
|---|---|---|---|---|
| ETH | #20 fault handoffs (4) | Native + review complete; merge pending | records 03 run09 PASS at `9b33e15`; review 002 SPEC/Standards PASS | Draft PR #182 open |
| ETH | #21 crossings Berlin/London/Cancun/Prague; types 0–4; unknown envelope | Accepted | PR #186 `da59a1a`; inventory PR #183 | Yes |
| BSC | #20 fault handoffs (3, incl. skip-block) | Accepted | part B run02 `883d3fe`; PR #179 | Yes |
| BSC | #21 component decode/signer/classify; modern-init prerequisite | Accepted | PR #185 `6cb31bc`; PR #188 `de4e297` | Yes (prerequisite only) |
| BSC | #21 current system-state import (registration, bootstrap, election, types 0–4) | First judged PASS, selected-state scope | records 2026-11-20 run09 at `54d048c` | Draft PR #191 open |
| BSC | #21 importer activation and unknown-type controls | Implemented at `35fa77c`; native verification in preparation10/run10 | this change | Draft PR #191 open |
| POLY | #20 fault handoffs (6) | Accepted | attempt 05 `6d1b51d`; PR #181 | Yes |
| POLY | #21 current-fork proof incl. StateSync refusals; preactivation refusals | Accepted | PR #189 `b704f76`; PR #193 `0b518f9` | Yes |
| POLY | #21 crossings Berlin, London/Jaipur, Prague/Bhilai | Partial, unaccepted | draft PR #194: native diagnostic at `b3b51d8`, mutation audit FAIL (74 kills/64 survivors at stop; judge repaired to `694a22c` offline), paired runtime NOT_RUN | No |
| POLY | #21 Madhugiri/MadhugiriPro body-StateSync crossing | NOT_RUN — no fixture exists | — | No |

## Minimal remaining set for G.E01 (#78) under D108

1. ETH: owner merge approval of PR #182 at `9b33e15`; record this
   reconciliation. No new ETH evidence.
2. BSC (draft PR #191): importer activation/unknown controls (in flight at
   `35fa77c`); the remaining Parlia system-transaction state rows (run09 is
   selected-state); independent Spec/Standards review; owner merge approval.
   The named Hertz/Haber/Pascal "applicable crossings" question — the fixture
   activates the timestamp forks together rather than crossing each fork on
   its own chain — is a scope question the PR #191 Spec review must
   adjudicate against D108's "applicable" wording and the "no new blanket
   crossing gates" rule.
3. POLY (draft PR #194, original session only per D108): finish the crossing
   judge repair and pass a complete mutation audit; run the paired
   London/Jaipur and Prague/Bhilai native crossings plus the three isolated
   gates under the repaired judge; add the Madhugiri/MadhugiriPro
   body-StateSync crossing fixture; reviews and merge.
4. Independent review of G.E01 (#78) after #20 and #21 close.

Explicit non-blockers (D96/D98/D100/D106/D108): every public-profile cell
NOT_RUN; historical/public upgrades beyond the named applicable crossings;
Polygon normal-profile type-3 refusal; full semantics (E04); combined
reorg/finality faults (E05); native `Synced` GoLive (E06-09).

## Ownership of the remaining obligations

| Obligation | Owner | Tree / session |
|---|---|---|
| PR #182 merge approval; reconciliation record | Owner; coordinator records | PR #182 `codex/e01-04-geth-b` at `9b33e15` |
| BSC activation/unknown controls; remaining system-state rows | Coordinator (producer) + independent BSC oracle implementer | `.worktrees/bsc-full-type-import`; oracle `.worktrees/bsc-full-type-oracle`; serialized native slot |
| Polygon crossings (judge repair, paired runtime, Madhugiri fixture) | Polygon transition implementer — original session only; not reassigned | `.worktrees/type-paths-poly-crossing` and `-judge`; `test/type_paths/poly/crossing/**` |
| G.E01 (#78) review | Independent reviewer | after #20/#21 |
