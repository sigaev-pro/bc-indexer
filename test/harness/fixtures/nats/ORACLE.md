# Source-native NATS conformance expectations

These expectations are fixed before compilation or native execution. They apply
only to the accepted NATS source commit
`5a815f11a911efa2c88fd1dbfb18ed89a449bfc1`. They are independent of generated
reports, selected stream/group IDs and native election timing.

Each case creates a fresh FileStorage stream named `FENCE`, subject
`fence.input`, LimitsPolicy, DiscardNew, no expiration, deletion or purge, and
no sources, mirrors, scheduling, atomic or batch publishing. A case publication
contains exactly the 20 bytes `source-fence-payload`. Its first stored message
must have sequence 1 and those exact subject/payload bytes. Management requests
and direct sequence reads do not create consumers.

- `TestExtractorFenceStandalone` runs one native server. StoreMsg is paused while
  the native stream mutex is held. The seal call starts before release and can
  complete only after release. The lock observation and source lock coverage are
  part of this proof; a quiet interval is not. Final native configuration must
  be sealed with DiscardNew, no age expiration, deletion, purge or rollup.
- `TestExtractorFencePreProposal` runs three native voting servers. The ordinary
  publication has passed its captured pre-seal check when delegated Propose is
  paused. The native seal acknowledgment precedes gate release. The already
  admitted publication then completes and is present at sequence 1. This exposes
  why sealing alone is not a terminal fence.
- `TestExtractorFenceDelayedApply` runs three voters. A real StoreRawMsg call is
  held after dequeue and before native Applied progress. Retain a raw sample with
  committed greater than applied even though proposal/entry/apply queues are
  empty. The independent frontier predicate must reject it. After release, both
  complete per-voter samples must meet the declared common frontier.
- `TestExtractorFenceLostAck` runs three voters. Record the exact raw PUB frame,
  reply subject and actual MSG acknowledgment frame before deliberately withholding
  that response from the test application channel. The application times out;
  the retained response and stored message still identify the same sequence-1
  attempt. PONG, client timeout or an acknowledgment for another attempt cannot
  replace the observed server response.
- `TestExtractorFenceLeaderChange` runs three voters. Request native StepDown to
  a different member. Retain the old and new complete observations: the leader
  changes and the term increases. The predicate rejects a collection spanning
  that change; a separate complete stable collection must then validate.

Source anchors, relative to the pinned archive: `server/stream.go:6518–6520`
holds the mutex across native storage at7380/7384; the ordinary clustered path
captures and releases its pre-seal snapshot at `jetstream_cluster.go:13080–13097`
and rejects the captured sealed state at13125; delegated proposal occurs in
`jetstream_batching.go:1078`. Dequeue and Applied are separate in
`jetstream_cluster.go:4619–4670`. Sealed configuration adjustments are in
`stream.go:2625–2631`. Raftz's native fields and independent per-group locking
are in `monitor.go:4234–4388`. Server names determine member IDs through
`events.go:1161–1177`; INFO IDs are fresh native server identities.

The owned copy excludes and records only `server/*_test.go` before adding this
overlay. This avoids upstream test init changes to elections, reconnect timing
and message tracing. All other source bytes, including non-Go compiler inputs,
go.mod and go.sum, must remain unchanged. Native constructors use production
defaults and natural metadata election. `NoSigs=true` assigns signal handling
to the single owned test process; it does not change execution or consensus.
Every finite gate opens before cleanup. Only a still-owned wrapper is restored,
so a newer native assignment is preserved.

This is limited source-native coverage. It does not establish old OS-process
exit, unchanged-image recovery, a complete multi-group collection or exhaustive
production ingress. Standalone acknowledgment loss remains required in the later
unchanged-image block. The complete terminal fence and runtime admission remain
unfinished even when these five cases pass.
