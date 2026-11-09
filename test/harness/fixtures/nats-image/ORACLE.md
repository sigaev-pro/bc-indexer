# Unchanged-image NATS expectations

These expectations are fixed before driver compilation or native execution.
They describe a limited private standalone/R3 fixture. The offline predicate
alone establishes supplied-observation consistency. A successful image proof
also requires authenticated preparation and actual native execution; none
of the three retained failed measured attempts proves the terminal fence.

The accepted production executable remains unchanged, from
`nats@sha256:afae2fdbe2e326c65e1246cb83b9d05d319188734f3bd577ac8072daae78a10f`,
source `5a815f11a911efa2c88fd1dbfb18ed89a449bfc1`. Its SHA256 is
`33f3fe3fed80b2b229bd9fe4b735257ad8583cd2349452a0c74f3d768b593e66`.
The runner must authenticate the accepted build, prepared driver and
accepted source-native conformance separately. Supplied hashes or JSON values
alone do not establish those prerequisites.

The closed plan declares both `standalone` and `R3`, 2–256 logical messages,
16–4,096 payload bytes and one measured parent budget of 60–1,800 seconds.
Preparation is separate. Publication, closure, restart, native recovery,
collection, export, comparison and cleanup all consume that parent budget.

Logical message `i` has the exact byte prefix `00 0d 0a ff 80`, followed by its
four-byte unsigned big-endian index. Remaining byte `j`, starting at zero after
that prefix/index, is `(37*j + 17*i) mod 256`. Payloads contain NUL, CR/LF and
invalid UTF-8 even at the minimum size. Their length and raw bytes must survive
ordinary PUB framing and native sequence export unchanged; text decoding is
not an acceptable comparison.

One declared publisher connection sends logical messages in order `0,1,1,2,...`.
The server response to the first attempt at logical message 1 is observed before
being withheld from the application, which times out. A single explicit retry
uses identical publication data and a new reply subject. There is no deduplication
header: both acknowledged attempts append and retain distinct sequences. Expected
stored sequences are exactly `1..messages+1`, with no gaps or extra messages.
The census retains complete PUB/MSG frames, connection/frame ordinals, reply
correlation and server/application event order. A PONG, timeout, different reply
or duplicate JSON field cannot stand in for the original server response.

Each connection has a distinct `_INBOX.IMAGE.<role>.C<ordinal>` prefix.
Subscriptions end in `.>` within that prefix; role ACLs allow only that role's
namespace. Readiness connections use the reader role, so simultaneous readers
also have distinct subscriptions. Management/export replies end in `.R<n>`,
publication replies in `.P<n>`, and the denied publication in `.DENIED`.
The complete raw stream for every connection must match its declared exchanges,
including the initial INFO, PONG and CONNECT INFO. Both INFO frames must match
the selected native process name, ID, listen host/port, protocol 1 and configured
payload limit with strict JSON types. Uncorrelated frames are never discarded.

The supported standalone has never registered a Raft node. Its account inventory
requests may return native HTTP 404 with exactly `No Raft nodes registered`,
without a newline. Empty, generic or filter-miss 404 bodies are not evidence of
that condition; R3 requires successful complete inventories and group samples.

API configuration responses include exactly three metadata strings:
`_nats.req.level=0`, `_nats.level=5`, and `_nats.ver=2.15.2`. Stored/JSz
configuration includes only `_nats.req.level=0`. These are different native
surfaces, not configuration drift: pinned `jetstream_versioning.go:45–50` removes
dynamic fields before storage, and lines 94–107 add them to a response copy.
API call sites are `jetstream_api.go:1644,1749,2193`; monitoring returns the
stored/target configuration at `monitor.go:3190–3202`. Validate exact metadata
keys, string types and values before projecting to the stored form; compare all
other fields exactly. Unknown, changed or missing metadata must reject. Retain
the complete original API and JSz responses.

CREATE completion is not an all-voter materialization barrier. Before publishing,
wait for one complete collection containing the same expected empty stream,
creation identity, stored configuration and Raft group on every voter. Polling
has a 60-second cap nested in the original measured deadline; each HTTP request
is clipped to that cap. Only an explicitly valid zero-stream APP inventory may
wait. Invalid status, identity, configuration, type or group fails immediately.
Retain every sample and freeze only the final complete initial inventory. This
does not replace any terminal recovery/frontier requirement.

Each topology uses explicit `image-S-N` names and their native member IDs, APP
and SYS accounts, loopback endpoints and distinct `/proof/stores/image-S-N`
directories. The operating configuration permits only the declared publisher,
stream-management and reader subjects. The restart configuration removes the
publisher and manager. Every surviving client has explicit permissions;
`allow_responses` is false, and no anonymous, websocket, leaf or gateway ingress
is permitted. Internal R3 routes remain fixed. The reader may publish only
`$JS.API.STREAM.INFO.FENCE` and `$JS.API.STREAM.MSG.GET.FENCE`, with its declared
reply inbox subscription. Native configuration compatibility and actual
permission rejection still require the future runtime proof.

All old processes must exit cleanly after publisher closure and seal, before
any recovered generation starts. An exit code of zero cannot conceal a Wait
error or forced kill. Native INFO IDs and process instances change; names,
member IDs and original store directory identities remain bound. Store contents
may legitimately change during recovery. Any referenced before-restart bytes
must therefore be copied into separate immutable evidence after old processes
exit, while recovery opens the original store paths. Final stores are recorded
only after recovered processes exit.

Complete native group enumeration, healthy per-group frontiers, bracketed exact
export, no later growth, authenticated child lifecycle and outer cleanup remain
required implementation work. These predicates do not complete issue #12,
public profiles, product capture, standardized payloads or runtime admission.
