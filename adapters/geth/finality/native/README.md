# Geth finality wiring (E05-01.eth_finality, Geth block — planned)

This directory is Geth-only and reserved for the native block that joins
the shared finality core ([`../README.md`](../README.md)) to the accepted
Geth tree (`1df30b18`), on top of the Geth lifecycle wiring. It will hold
`patches.json` and the installed files that instrument the actual
`SetSafe`/`SetFinalized` callers and the rewind clear of the pinned source,
never an RPC status or a chain event alone:

- the mapped setter callers (`geth.set-safe`, `geth.set-finalized`,
  `geth.engine-forkchoice`, `geth.set-head`) call `finality.Tracker.Set`
  and `Clear` inside their gate pass, before the native setter mutates
  state, and `Commit` in the final authoritative batch of the change;
- the startup reconcile caller (`geth.startup-reconcile`) runs the
  `finality.Tracker.Startup` refresh loop after the native startup
  restore, without a new head event;
- the observation guard covers the consistent read of head, safe and
  finalized; the ancestry oracle reads headers error-aware.

No patch exists yet: this block was delivered as block 0 of
E05-01.eth_finality (the shared core, its vectors, mutants and harness).
Every AC-LIF-05-01 and AC-LIF-05-04 profile cell stays NOT_RUN until this
block measures it on the native tree.
