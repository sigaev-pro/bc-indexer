package rocksdb_test

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage/rocksdb"
)

// The producer journal on the durable store: allocate, write, close, reopen,
// and the state is identical.
//
// core/journal/producer tests a restart by reopening the journal over the
// same memory store. Here the store is closed and a new one is opened on the
// directory, so everything comes back from the disk through the real engine.
// It covers, at the journal level, the crash points of
// ROCKSDB_DELIVERY_PROTOCOL that leave a batch in INTENT, STAGING or SEALED
// (REC-T02): the bytes, identities and states come back unchanged and the
// protocol continues from them.

var journalBinding = [32]byte{0x07}

type journalState struct {
	identity producer.Identity
	mode     producer.ModeState
	binding  [32]byte
	retired  uint64
	batches  []producer.BatchInfo
	outbox   []producer.OutboxEntry
}

func readJournalState(t *testing.T, j *producer.Store, seqs []uint64) journalState {
	t.Helper()
	s := journalState{
		identity: j.Identity(),
		mode:     j.ModeInfo(),
		binding:  j.BindingDigest(),
		retired:  j.RetiredPrefix(),
	}
	for _, seq := range seqs {
		info, err := j.Batch(seq)
		if err != nil {
			t.Fatalf("batch %d: %v", seq, err)
		}
		s.batches = append(s.batches, info)
	}
	s.outbox, _ = j.Outbox()
	if s.outbox == nil {
		s.outbox = []producer.OutboxEntry{}
	}
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestProducerJournalRestartKeepsIdenticalState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st, err := rocksdb.Open(dir, rocksdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := producer.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	must(t, j.SetDestinationBinding(journalBinding))

	// Batch 1 goes as far as SEALED: ready to publish, nothing published.
	first, _, err := j.AllocateBatch(1, 2)
	must(t, err)
	att, err := j.StartAttempt(first.BatchSeq, 2)
	must(t, err)
	manifest := []byte("extractor.manifest.v2\x00payload")
	must(t, j.WriteAttemptManifest(first.BatchSeq, att.Generation, 0, manifest[:7]))
	must(t, j.WriteAttemptManifest(first.BatchSeq, att.Generation, 7, manifest[7:]))
	must(t, j.WriteAttemptData(first.BatchSeq, att.Generation, 0, []byte("record-fragment-bytes")))
	ready, err := j.CommitReady(first.BatchSeq, att.Generation, uint64(len(manifest)), []byte("capture-checkpoint"))
	must(t, err)
	generation, _, err := j.BeginPlan(first.BatchSeq, []byte("reservation-seed"), [32]byte{5}, []byte("header"))
	must(t, err)
	must(t, j.WritePlanChunk(first.BatchSeq, generation, 0, []byte("frame-one")))
	must(t, j.WritePlanChunk(first.BatchSeq, generation, 9, []byte("frame-two")))
	sealed, err := j.Seal(first.BatchSeq, generation, 18)
	must(t, err)

	// Batch 2 stops in STAGING and batch 3 right after its intent was
	// allocated: the process went down between transitions. (A decision
	// without public events saves no batch at all, so every batch here has
	// events.)
	second, _, err := j.AllocateBatch(2, 1)
	must(t, err)
	secondAttempt, err := j.StartAttempt(second.BatchSeq, 1)
	must(t, err)
	must(t, j.WriteAttemptData(second.BatchSeq, secondAttempt.Generation, 0, []byte("half-written")))
	third, _, err := j.AllocateBatch(3, 1)
	must(t, err)

	seqs := []uint64{first.BatchSeq, second.BatchSeq, third.BatchSeq}
	before := readJournalState(t, j, seqs)
	rawBefore := dumpPairs(t, st)
	if before.batches[0].State != producer.StateSealed || before.batches[1].State != producer.StateStaging ||
		before.batches[2].State != producer.StateIntent {
		t.Fatalf("states before the restart: %+v", before.batches)
	}
	must(t, rocksdb.Close(st))

	// A real restart: nothing of the old store object or journal is left.
	st, err = rocksdb.Open(dir, rocksdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rocksdb.Close(st) })
	j, err = producer.Open(st)
	if err != nil {
		t.Fatal(err)
	}

	after := readJournalState(t, j, seqs)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("journal state changed across the restart:\n before %+v\n after  %+v", before, after)
	}
	if rawAfter := dumpPairs(t, st); !reflect.DeepEqual(rawAfter, rawBefore) {
		t.Fatalf("stored bytes changed across the restart:\n before %d pairs\n after  %d pairs", len(rawBefore), len(rawAfter))
	}
	if len(rawBefore) < 12 {
		t.Fatalf("the store holds only %d pairs; the scenario wrote more", len(rawBefore))
	}

	// The run continues from the durable state. A replayed native decision
	// finds its batch instead of allocating a second one.
	replayed, isReplay, err := j.AllocateBatch(1, 2)
	must(t, err)
	if !isReplay || replayed.BatchSeq != first.BatchSeq {
		t.Fatalf("decision replay after the restart: %+v replayed=%v", replayed, isReplay)
	}
	// A STAGING retry keeps the batch and opens the next attempt generation.
	retry, err := j.StartAttempt(second.BatchSeq, 1)
	must(t, err)
	if retry.Generation != secondAttempt.Generation+1 {
		t.Fatalf("retry generation after the restart = %d, want %d", retry.Generation, secondAttempt.Generation+1)
	}
	// The sealed batch is acknowledged, cleaned up and released.
	ack := producer.IngestAck{
		ManifestDigest: ready.ManifestDigest,
		PlanDigest:     sealed.PlanDigest,
		LayoutDigest:   sealed.LayoutDigest,
		BindingDigest:  journalBinding,
		IssuerEpoch:    [16]byte{3},
	}
	must(t, j.OnIngestAck(first.BatchSeq, ack))
	for {
		done, err := j.Cleanup(first.BatchSeq, 2)
		must(t, err)
		if done {
			break
		}
	}
	must(t, j.OnReleaseAck(first.BatchSeq))
	if got := j.RetiredPrefix(); got != first.BatchSeq {
		t.Fatalf("retired prefix = %d, want %d", got, first.BatchSeq)
	}
	if info, err := j.Batch(first.BatchSeq); err != nil || info.State != producer.StateRetired {
		t.Fatalf("batch state after the release: %+v %v", info, err)
	}
	// The restart did not disturb the other two batches.
	final := readJournalState(t, j, seqs)
	if final.batches[1].State != producer.StateStaging || final.batches[2].State != producer.StateIntent {
		t.Fatalf("states of the interrupted batches changed: %+v", final.batches)
	}
}

type pair struct{ key, value []byte }

func dumpPairs(t *testing.T, st storage.Storage) []pair {
	t.Helper()
	out := []pair{}
	err := st.Iterate(nil, nil, func(k, v []byte) error {
		out = append(out, pair{bytes.Clone(k), bytes.Clone(v)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
