package journal

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// failingReads is a storage whose reads of one key fail, to show what a
// journal step answers when the store cannot be read.
type failingReads struct {
	storage.Storage
	key   []byte
	fails bool
}

var errReadFailed = errors.New("test store: read failed")

func (f *failingReads) Get(key []byte) ([]byte, error) {
	if f.fails && string(key) == string(f.key) {
		return nil, errReadFailed
	}
	return f.Storage.Get(key)
}

// newFailingJournal opens a journal that knows its active binding over a
// store whose read of one key can be made to fail.
func newFailingJournal(t *testing.T, key []byte) (*Journal, *failingReads) {
	t.Helper()
	st := &failingReads{Storage: storage.NewMemory(), key: key}
	active := Binding{DestinationID: testDestination, Generation: 3, ChainID: testChain,
		GenesisHash: testGenesis, DescriptorDigest: testBinding}
	j, err := Open(st, Options{BindingDigest: testBinding, Binding: &active})
	if err != nil {
		t.Fatal(err)
	}
	return j, st
}

// Data admission reads the seal and, for a sealed run, the resume. When
// either read fails the step is refused with that error: a store that
// cannot be read never answers "open".
func TestDataAdmissionRefusesWhenAReadFails(t *testing.T) {
	run := testBatch(0)
	cases := map[string][]byte{"seal read": sealedKey(run), "resume read": resumeKey(run)}
	for name, key := range cases {
		j, st := newFailingJournal(t, key)
		if err := j.CheckDataAdmission(run, 1); err != nil {
			t.Fatalf("%s: unsealed run with a readable store: %v", name, err)
		}
		sealRun(t, j, 0)
		refuseAdmission(t, j, run, 1, 2)
		if _, err := j.Resume(run, ResumeIntent{ResumeID: [16]byte{5}, OldSeal: sealOf(t, j, run),
			BindingDigest: testBinding, NextAdmissionGeneration: 2}); err != nil {
			t.Fatalf("%s: resume with a readable store: %v", name, err)
		}

		// With the store readable the resumed generation admits data; with
		// this read failing, no generation gets an answer of any kind other
		// than the read error, the open generation included.
		if err := j.CheckDataAdmission(run, 2); err != nil {
			t.Fatalf("%s: resumed generation with a readable store: %v", name, err)
		}
		st.fails = true
		for _, admission := range []uint64{1, 2, 3} {
			if err := j.CheckDataAdmission(run, admission); !errors.Is(err, errReadFailed) {
				t.Fatalf("%s failed, generation %d: got %v, want the read error", name, admission, err)
			}
		}
		st.fails = false
		if err := j.CheckDataAdmission(run, 2); err != nil {
			t.Fatalf("%s: resumed generation after the store is readable again: %v", name, err)
		}
	}
}

// refuseAdmission checks that the run admits no data in the given
// generations.
func refuseAdmission(t *testing.T, j *Journal, run BatchID, admissions ...uint64) {
	t.Helper()
	for _, admission := range admissions {
		if err := j.CheckDataAdmission(run, admission); !errors.Is(err, ErrAdmissionClosed) {
			t.Fatalf("generation %d: got %v, want ErrAdmissionClosed", admission, err)
		}
	}
}

// sealOf is the durable seal of the run.
func sealOf(t *testing.T, j *Journal, run BatchID) Seal {
	t.Helper()
	seal, ok, err := j.SealedGeneration(run)
	if err != nil || !ok {
		t.Fatalf("seal of the run: ok=%v %v", ok, err)
	}
	return seal
}
