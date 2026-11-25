package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
)

// headerOf supplies the header of one session. Both session kinds of the
// tests provide it as their header method.
type headerOf func() *SessionHeader

// dataCall is one data RPC of the wire, sent with one session and one batch
// sequence. call returns the typed error of the answer and fails the test
// on a transport failure.
type dataCall struct {
	name string
	call func() *Error
}

// dataCalls returns the data RPCs of the wire, the Prepare and Publish
// steps. Each is independent of the others and creates no state when it is
// refused, so all of them can name the same batch sequence.
func dataCalls(t *testing.T, h *harness, header headerOf, seq uint64) []dataCall {
	t.Helper()
	ctx := context.Background()
	digest := sha32([]byte("manifest"))
	call := func(name string, send func() (*Error, error)) dataCall {
		return dataCall{name: name, call: func() *Error {
			t.Helper()
			e, err := send()
			return wireErr(t, e, err)
		}}
	}
	return []dataCall{
		call("BeginPrepare", func() (*Error, error) {
			r, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: header(), Batch: batchMsg(seq),
				ManifestDigest: digest, EventCount: 1, DescriptorBytesTotal: 8})
			return r.GetError(), err
		}),
		call("BeginPrepare IMPORT_SEALED", func() (*Error, error) {
			r, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: header(), Batch: batchMsg(seq),
				ManifestDigest: digest, EventCount: 1, DescriptorBytesTotal: 8,
				Mode: PrepareMode_PREPARE_IMPORT_SEALED, ImportSeed: []byte("seed")})
			return r.GetError(), err
		}),
		call("PrepareChunk", func() (*Error, error) {
			r, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{Header: header(), Batch: batchMsg(seq),
				ManifestDigest: digest, Offset: 0, ChunkSha256: sha32([]byte("bytes")), Data: []byte("bytes")})
			return r.GetError(), err
		}),
		call("FinalizePrepare", func() (*Error, error) {
			r, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{Header: header(), Batch: batchMsg(seq),
				ManifestDigest: digest})
			return r.GetError(), err
		}),
		call("BeginPublish", func() (*Error, error) {
			r, err := h.client.BeginPublish(ctx, &BeginPublishRequest{Header: header(), Batch: batchMsg(seq),
				Seed: []byte("seed"), PlanDigest: digest, LayoutManifestDigest: digest, EntryCount: 1})
			return r.GetError(), err
		}),
		call("PublishChunk", func() (*Error, error) {
			stream, err := h.client.PublishChunk(ctx)
			if err != nil {
				return nil, err
			}
			if err := stream.Send(&PublishChunkRequest{Header: header(), Batch: batchMsg(seq), PlanDigest: digest,
				Offset: 0, ChunkSha256: sha32([]byte("x")), Data: []byte("x")}); err != nil {
				return nil, err
			}
			resp, err := stream.Recv()
			if err != nil {
				return nil, fmt.Errorf("the stream closed instead of answering: %w", err)
			}
			// The refusal answers that message and leaves the stream open.
			if err := stream.CloseSend(); err != nil {
				return nil, err
			}
			if _, err := stream.Recv(); err != io.EOF {
				return nil, fmt.Errorf("stream end: %v, want io.EOF", err)
			}
			return resp.GetError(), nil
		}),
		call("FinalizePublish", func() (*Error, error) {
			r, err := h.client.FinalizePublish(ctx, &FinalizePublishRequest{Header: header(), Batch: batchMsg(seq),
				PlanDigest: digest, ManifestDigest: digest, LayoutManifestDigest: digest})
			return r.GetError(), err
		}),
		call("SealImport", func() (*Error, error) {
			r, err := h.client.SealImport(ctx, &FinalizePublishRequest{Header: header(), Batch: batchMsg(seq),
				PlanDigest: digest, ManifestDigest: digest, LayoutManifestDigest: digest})
			return r.GetError(), err
		}),
	}
}

// assertNoBatch fails when the store holds any record of one batch sequence
// of the test run.
func assertNoBatch(t *testing.T, st storage.Storage, seq uint64) {
	t.Helper()
	id := jid(seq).Bytes()
	prefix := append([]byte("batch/"), id[:]...)
	keys := 0
	err := st.Iterate(nil, nil, func(k, _ []byte) error {
		if len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix) {
			keys++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if keys != 0 {
		t.Fatalf("the store holds %d record(s) of batch %d", keys, seq)
	}
}

// refuseData sends every data RPC with one session and one batch sequence
// and fails unless each answers DRAIN_REQUIRED and creates no state.
func refuseData(t *testing.T, h *harness, header headerOf, seq uint64) {
	t.Helper()
	calls := dataCalls(t, h, header, seq)
	if len(calls) != 8 {
		t.Fatalf("this test knows %d data RPCs, want 8: the wire changed", len(calls))
	}
	for _, c := range calls {
		wantCode(t, c.name, c.call(), ErrorCode_DRAIN_REQUIRED, RetryAction_RECONNECT)
	}
	assertNoBatch(t, h.st, seq)
}

// sealOverWire seals one admission generation over the wire and returns
// the SealAck.
func sealOverWire(t *testing.T, h *harness, header headerOf, bindingGeneration, admission, highwater uint64) *SealAck {
	t.Helper()
	resp, err := h.client.SealGeneration(context.Background(), &SealGenerationRequest{Header: header(),
		BindingGeneration: bindingGeneration, AdmissionGeneration: admission, DrainId: testDrainID, BatchHighwater: highwater})
	if err != nil || resp.GetError() != nil || resp.GetSeal() == nil {
		t.Fatalf("seal: %v %+v", err, resp.GetError())
	}
	return resp.GetSeal()
}

// resumeOverWire sends one Resume and returns its answer with the typed
// error, which is nil when the run resumed.
func resumeOverWire(t *testing.T, h *harness, header headerOf, old *SealAck, resumeID []byte, binding [32]byte, next uint64) (*ResumeResponse, *Error) {
	t.Helper()
	resp, err := h.client.Resume(context.Background(), &ResumeRequest{Header: header(), OldSeal: old, ResumeId: resumeID,
		ActiveBindingDigest: binding[:], NextAdmissionGeneration: next})
	if err != nil {
		t.Fatal(err)
	}
	return resp, resp.GetError()
}

// preparedOverWire runs one batch to PREPARED with one session and returns
// its reservation.
func preparedOverWire(t *testing.T, h *harness, header headerOf, seq uint64) journal.Reservation {
	t.Helper()
	ctx := context.Background()
	manifest := []byte("extractor.manifest.v2\x00admission")
	digest := sha32(manifest)
	begin, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: header(), Batch: batchMsg(seq),
		ManifestDigest: digest, EventCount: 1, DescriptorBytesTotal: uint64(len(manifest))})
	if err != nil || begin.GetError() != nil {
		t.Fatalf("begin prepare: %v %+v", err, begin.GetError())
	}
	for off := 0; off < len(manifest); off += 5 {
		end := off + 5
		if end > len(manifest) {
			end = len(manifest)
		}
		if _, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{Header: header(), Batch: batchMsg(seq),
			ManifestDigest: digest, Offset: uint64(off), ChunkSha256: sha32(manifest[off:end]), Data: manifest[off:end]}); err != nil {
			t.Fatal(err)
		}
	}
	fin, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{Header: header(), Batch: batchMsg(seq), ManifestDigest: digest})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize prepare: %v %+v", err, fin.GetError())
	}
	info, err := h.j.Batch(jid(seq))
	if err != nil || info.Reservation == nil {
		t.Fatalf("reservation of batch %d: %+v %v", seq, info, err)
	}
	return *info.Reservation
}

var resumeFive = []byte{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}

// Data waits for the Resume (DELIVERY_STATE_MACHINE, the drain and cutover
// section; acceptance case AC-DLV-11-02). After a seal no data RPC of the
// run runs in any generation, until a Resume opens the next one. This is
// probe A of the review of this change: before the fix a session of the
// next generation ran BeginPrepare with no Resume on record.
func TestSealedRunAdmitsNoDataUntilResume(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	seal := sealOverWire(t, h, s.header, 1, 1, 0)

	// A session of the next generation opens, because the seal retry and
	// the Resume come from it, but it admits no data. A later generation
	// admits none either.
	next := h.openSessionAt(t, 2, 2)
	refuseData(t, h, next.header, 9)
	later := h.openSessionAt(t, 3, 2)
	refuseData(t, h, later.header, 10)

	// The control RPCs of the drain keep working: a state read and the
	// retry of the seal.
	state, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: next.header(), Batch: batchMsg(9),
		PlanDigest: sha32([]byte("any plan"))})
	if err != nil || state.GetError() != nil || state.GetState() != BatchState_NOT_FOUND {
		t.Fatalf("state read under a seal: %s %+v %v", state.GetState(), state.GetError(), err)
	}
	if retry := sealOverWire(t, h, next.header, 1, 1, 0); retry.String() != seal.String() {
		t.Fatalf("seal retry: %+v, want %+v", retry, seal)
	}

	// Admission is durable state: a gateway restart changes nothing.
	h.stop()
	h2 := startHarness2(t, h)
	after := h2.openSessionAt(t, 2, 1)
	refuseData(t, h2, after.header, 9)

	// The Resume opens generation 2. From then on only generation 2 admits
	// data: a later generation that no Resume opened stays closed.
	resumed, e := resumeOverWire(t, h2, after.header, seal, resumeFive, testBinding, 2)
	if e != nil || resumed.GetAck().GetNewAdmissionGeneration() != 2 {
		t.Fatalf("resume: %+v %+v", resumed.GetAck(), e)
	}
	if res := preparedOverWire(t, h2, after.header, 9); res.BindingDigest != testBinding {
		t.Fatalf("a batch after the Resume is bound to %x, want the active binding %x", res.BindingDigest, testBinding)
	}
	stillLater := h2.openSessionAt(t, 3, 2)
	refuseData(t, h2, stillLater.header, 10)
}

// restartSwitchHarness starts a fresh gateway process over the surviving
// storage of a switch harness, which knows the identity of its binding.
func restartSwitchHarness(t *testing.T, h *harness) *harness {
	t.Helper()
	active := journal.Binding{DestinationID: activeSpec.dst, Generation: activeSpec.gen, ChainID: activeSpec.chain,
		GenesisHash: activeSpec.genesis, DescriptorDigest: activeSpec.digest()}
	j, err := journal.Open(h.st, journal.Options{BindingDigest: activeSpec.digest(), Binding: &active})
	if err != nil {
		t.Fatal(err)
	}
	return startHarness(t, h.st, j, session.NewGate())
}

// A prepared switch closes admission for the old binding. This is probe B
// of the review: before the fix a session drove a batch to CLOSED while the
// switch was only prepared, and after the commit that batch was still bound
// to the old digest. The publisher refuses such a batch, so it could never
// be acknowledged or released.
func TestPreparedSwitchClosesDataAdmission(t *testing.T) {
	h := newSwitchHarness(t)
	ctx := context.Background()
	active := activeSpec.digest()
	a := h.openSessionBound(t, 1, 1, active)
	seal := sealOverWire(t, h, a.header, activeSpec.gen, 1, 0)
	b := h.openSessionBound(t, 2, 2, active)
	h.server.SetTargetVerifier(&fakeVerifier{})
	desired := activeSpec
	desired.gen = 4
	desired.prefix = "evm2"
	newDigest := desired.digest()
	prepared, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: b.header(), OldSeal: seal,
		OldBindingDigest: active[:], DesiredDescriptor: desired.json(), SwitchId: switchID})
	if err != nil || prepared.GetError() != nil {
		t.Fatalf("prepare switch: %v %+v", err, prepared.GetError())
	}

	// The switch is only prepared: the old binding is still the one the
	// session is bound to, and no data runs under it.
	refuseData(t, h, b.header, 9)
	// The Resume waits for the commit.
	_, e := resumeOverWire(t, h, b.header, seal, resumeFive, active, 2)
	wantCode(t, "resume under a prepared switch", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)

	// The commit activates the new binding, so the old session no longer
	// passes the header check at all.
	committed, err := h.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: b.header(), SwitchId: switchID,
		Prepared: prepared.GetPrepared(), Assertion: &BindingCommitAssertion{SchemaVersion: 1, SwitchId: switchID,
			BindingDigest: newDigest[:], BindingGeneration: 4}})
	if err != nil || committed.GetError() != nil {
		t.Fatalf("commit switch: %v %+v", err, committed.GetError())
	}
	denied, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: b.header(), Batch: batchMsg(9),
		ManifestDigest: sha32([]byte("manifest")), EventCount: 1, DescriptorBytesTotal: 8})
	if err != nil || denied.GetError().GetCode() != ErrorCode_BINDING_MISMATCH {
		t.Fatalf("data RPC of the old binding after the commit: %v %+v", err, denied.GetError())
	}

	// Under the new binding data still waits for the Resume.
	c := h.openSessionBound(t, 2, 3, newDigest)
	refuseData(t, h, c.header, 9)
	resumed, e := resumeOverWire(t, h, c.header, seal, resumeFive, newDigest, 2)
	if e != nil || string(resumed.GetAck().GetBindingDigest()) != string(newDigest[:]) {
		t.Fatalf("resume after the switch: %+v %+v", resumed.GetAck(), e)
	}
	// Only now does data run, and a batch is bound to the new binding.
	if res := preparedOverWire(t, h, c.header, 9); res.BindingDigest != newDigest {
		t.Fatalf("a batch after the switch is bound to %x, want the new binding %x", res.BindingDigest, newDigest)
	}
}

// CUTOVER-01: a crash between PrepareSwitch and CommitSwitch. The prepared
// switch is durable. The restarted gateway answers its retry with the same
// receipt, refuses another switch, all data and the Resume until the
// commit, and commits it from a new session after one new target check.
func TestRestartBetweenPrepareAndCommitSwitch(t *testing.T) {
	h := newSwitchHarness(t)
	ctx := context.Background()
	active := activeSpec.digest()
	a := h.openSessionBound(t, 1, 1, active)
	seal := sealOverWire(t, h, a.header, activeSpec.gen, 1, 0)
	b := h.openSessionBound(t, 2, 2, active)
	h.server.SetTargetVerifier(&fakeVerifier{})
	desired := activeSpec
	desired.gen = 4
	desired.prefix = "evm2"
	newDigest := desired.digest()
	prepared, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: b.header(), OldSeal: seal,
		OldBindingDigest: active[:], DesiredDescriptor: desired.json(), SwitchId: switchID})
	if err != nil || prepared.GetError() != nil {
		t.Fatalf("prepare switch: %v %+v", err, prepared.GetError())
	}
	receipt := prepared.GetPrepared()

	// The gateway process crashes here. The store survives it.
	h.stop()
	h2 := restartSwitchHarness(t, h)
	stale, err := h2.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: b.header(), OldSeal: seal,
		OldBindingDigest: active[:], DesiredDescriptor: desired.json(), SwitchId: switchID})
	if err != nil || stale.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("the session of the crashed process: %v %+v", err, stale.GetError())
	}
	verifier := &fakeVerifier{}
	h2.server.SetTargetVerifier(verifier)
	c := h2.openSessionBound(t, 2, 1, active)

	// The prepared switch survived: its retry answers the same receipt
	// without a target check, and another switch over it is refused.
	again, err := h2.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: c.header(), OldSeal: seal,
		OldBindingDigest: active[:], DesiredDescriptor: desired.json(), SwitchId: switchID})
	if err != nil || again.GetError() != nil || again.GetPrepared().String() != receipt.String() {
		t.Fatalf("prepare retry after a restart: %v %+v", err, again.GetError())
	}
	other, err := h2.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: c.header(), OldSeal: seal,
		OldBindingDigest: active[:], DesiredDescriptor: desired.json(), SwitchId: otherSwitchID})
	wantCode(t, "another switch after a restart", wireErr(t, other.GetError(), err),
		ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)
	// No data and no Resume until the commit.
	refuseData(t, h2, c.header, 9)
	_, e := resumeOverWire(t, h2, c.header, seal, resumeFive, active, 2)
	wantCode(t, "resume under a prepared switch after a restart", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)

	// The commit from the new process checks the target once and activates
	// the binding.
	committed, err := h2.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: c.header(), SwitchId: switchID,
		Prepared: receipt, Assertion: &BindingCommitAssertion{SchemaVersion: 1, SwitchId: switchID,
			BindingDigest: newDigest[:], BindingGeneration: 4}})
	if err != nil || committed.GetError() != nil {
		t.Fatalf("commit switch after a restart: %v %+v", err, committed.GetError())
	}
	if n := verifier.count(); n != 1 {
		t.Fatalf("the commit after a restart ran %d target checks, want 1", n)
	}
	if digest := h2.j.BindingDigest(); digest != newDigest {
		t.Fatalf("active binding after the commit: %x, want %x", digest, newDigest)
	}

	// Then the Resume under the new binding opens data, bound to it.
	last := h2.openSessionBound(t, 2, 2, newDigest)
	resumed, e := resumeOverWire(t, h2, last.header, seal, resumeFive, newDigest, 2)
	if e != nil || resumed.GetAck().GetNewAdmissionGeneration() != 2 {
		t.Fatalf("resume after a restart: %+v %+v", resumed.GetAck(), e)
	}
	if res := preparedOverWire(t, h2, last.header, 9); res.BindingDigest != newDigest {
		t.Fatalf("a batch after the switch is bound to %x, want the new binding %x", res.BindingDigest, newDigest)
	}
}

// hookedStorage wraps the storage of a harness and runs a hook before every
// atomic batch is applied.
type hookedStorage struct {
	storage.Storage
	beforeApply func()
}

func (h *hookedStorage) Apply(b *storage.Batch) error {
	if h.beforeApply != nil {
		h.beforeApply()
	}
	return h.Storage.Apply(b)
}

// The publisher barrier goes up before the RELEASING marker is committed,
// as the release row of the contract orders it, and a refused release never
// fences a batch that still publishes.
func TestReleaseBarrierPrecedesReleasing(t *testing.T) {
	st := &hookedStorage{Storage: storage.NewMemory()}
	h := startHarness(t, st, openJournal(t, st), session.NewGate())
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	p := closedOverWire(t, h, s, 1) // CLOSED, not acknowledged yet
	gate := h.server.PublisherGate()
	fenced := func() bool {
		t.Helper()
		err := gate.Apply(session.Callback{Batch: jid(1).Bytes(), Generation: gate.Generation()}, func() error { return nil })
		if errors.Is(err, session.ErrBatchReleased) {
			return true
		}
		if err != nil {
			t.Fatalf("unexpected callback error: %v", err)
		}
		return false
	}
	release := func() (*ReleaseResponse, error) {
		t.Helper()
		return h.client.Release(ctx, &ReleaseRequest{Header: s.header(), Batch: batchMsg(1),
			ManifestDigest: p.manifest[:], PlanDigest: p.planDigest})
	}

	// A release that is refused fences nothing.
	refused, err := release()
	wantCode(t, "release of a CLOSED batch", wireErr(t, refused.GetError(), err), ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	if fenced() {
		t.Fatal("a refused release fenced the batch")
	}

	// An accepted release fences the batch before its first commit, the one
	// of the RELEASING marker, and keeps it fenced through every later one.
	p.ackInJournal(t, h)
	var seen []bool
	st.beforeApply = func() { seen = append(seen, fenced()) }
	accepted, err := release()
	if err != nil || accepted.GetError() != nil || accepted.GetAck() == nil {
		t.Fatalf("release: %v %+v", err, accepted.GetError())
	}
	if len(seen) == 0 {
		t.Fatal("the release committed nothing")
	}
	for i, f := range seen {
		if !f {
			t.Fatalf("the batch was not fenced at commit %d of %d", i+1, len(seen))
		}
	}
}

// The control RPCs of a drain are not data: under a seal a state read, the
// retry of a release, a recovery release and the check of a switch all keep
// answering. Only the Prepare and Publish steps wait for the Resume.
func TestControlRPCsRunUnderASeal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	s := h.openSessionAt(t, 1, 1)
	p := closedOverWire(t, h, s, 1)
	p.ackInJournal(t, h)
	released, err := h.client.Release(ctx, &ReleaseRequest{Header: s.header(), Batch: batchMsg(1),
		ManifestDigest: p.manifest[:], PlanDigest: p.planDigest})
	if err != nil || released.GetError() != nil {
		t.Fatalf("release: %v %+v", err, released.GetError())
	}
	seal := sealOverWire(t, h, s.header, 1, 1, 1)
	next := h.openSessionAt(t, 2, 2)
	refuseData(t, h, next.header, 5)

	// A state read answers.
	state, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: next.header(), Batch: batchMsg(1),
		PlanDigest: p.planDigest})
	if err != nil || state.GetError() != nil || state.GetState() != BatchState_RETIRED {
		t.Fatalf("state read under a seal: %s %+v %v", state.GetState(), state.GetError(), err)
	}
	// The retry of a finished release answers the retired prefix again.
	retry, err := h.client.Release(ctx, &ReleaseRequest{Header: next.header(), Batch: batchMsg(1),
		ManifestDigest: p.manifest[:], PlanDigest: p.planDigest})
	if err != nil || retry.GetError() != nil || retry.GetAck().GetRetiredPrefix() != 1 {
		t.Fatalf("release retry under a seal: %+v %+v", retry.GetAck(), retry.GetError())
	}
	// A recovery release of a lost batch advances the prefix.
	plan := sha32([]byte("a lost plan"))
	recovered, err := h.client.RecoveryRelease(ctx, &RecoveryReleaseRequest{Header: next.header(), Batch: batchMsg(2),
		IngestAck: ackProto(2, p.manifest, as32(plan), p.layout, testBinding[:]),
		Assertion: outboxProto(2, as32(plan), testBinding[:])})
	if err != nil || recovered.GetError() != nil || recovered.GetAck().GetRetiredPrefix() != 2 {
		t.Fatalf("recovery release under a seal: %+v %+v", recovered.GetAck(), recovered.GetError())
	}
	if prefix, _ := h.j.RetiredPrefix(jid(2)); prefix != 2 {
		t.Fatalf("retired prefix after the recovery release = %d, want 2", prefix)
	}
	// A switch is checked under a seal. This store was never told the
	// identity of its binding, so the check ends at that, not at admission.
	h.server.SetTargetVerifier(&fakeVerifier{})
	switched, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: next.header(), OldSeal: seal,
		OldBindingDigest: testBinding[:], DesiredDescriptor: activeSpec.json(), SwitchId: switchID})
	wantCode(t, "prepare switch under a seal", wireErr(t, switched.GetError(), err),
		ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)
}

// failingReads is a storage whose read of one key can be made to fail.
type failingReads struct {
	storage.Storage
	key   []byte
	fails bool
}

func (f *failingReads) Get(key []byte) ([]byte, error) {
	if f.fails && string(key) == string(f.key) {
		return nil, errors.New("test store: read failed")
	}
	return f.Storage.Get(key)
}

// Data admission reads the seal and the resume of the run before a data RPC
// runs. When the store cannot be read, the RPC is refused and creates no
// state: a store that cannot be read never answers "open".
func TestDataRPCRefusedWhenAdmissionReadFails(t *testing.T) {
	ids := append(append([]byte(nil), testProducer[:]...), testRun[:]...)
	cases := map[string][]byte{
		"seal read":   append([]byte("sealed_generation/"), ids...),
		"resume read": append([]byte("resume/"), ids...),
	}
	for name, key := range cases {
		st := &failingReads{Storage: storage.NewMemory(), key: key}
		h := startHarness(t, st, openJournal(t, st), session.NewGate())
		s := h.openSessionAt(t, 1, 1)
		sealOverWire(t, h, s.header, 1, 1, 0)
		next := h.openSessionAt(t, 2, 2)

		st.fails = true
		for _, c := range dataCalls(t, h, next.header, 9) {
			wantCode(t, name+": "+c.name, c.call(), ErrorCode_STORE_FATAL, RetryAction_STOP_LOCAL_OWNER)
		}
		assertNoBatch(t, st, 9)
		// With the store readable again the same RPC answers for its real
		// reason: the run is sealed and not resumed.
		st.fails = false
		refuseData(t, h, next.header, 9)
	}
}
