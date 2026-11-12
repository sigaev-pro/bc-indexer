package sourceinputs

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
)

// Independent expected values. `python3 test/harness/source_inputs.py
// vectors` computes them with Python's hashlib from the README formulas;
// test/harness/test_source_inputs.py checks that they match.
const (
	wantCapsuleDigest       = "518f4f0eeac209903a9405c383f4e62d4a41d18435ceacc75ba7dd548d47304d"
	wantChainStart          = "68f15565da5604682f38070844093f8e317d171238a9a869dbe5024c13ec1e40"
	wantEntry0Digest        = "1d1e28aa83c78f6f6a8b153c34d509d33a3bf926c46d5cb705f80bca0b7b4372"
	wantChain1              = "253c07905f63942a5fd2992cb0d12d0f6b84568b95171ce1852f8618145e49cb"
	wantEntry1Digest        = "0820f284d7155d8dd538b2f686c07b4351f9ccc59ef3cfabf6af1d82c1dd5933"
	wantChain2              = "f9ba0ce8d5439674973797aef25fbcb656a47711e5db6a92cfe88127b7993fb7"
	wantConsumedOneDigest   = "5c3c075d8069ddcd298e17f54e47d311db11bc4cb292e304fbaa50904a0cdd7a"
	wantConsumedDigest      = "1a16a7e647843974f84e62f55b8ea6acd8d8e764af1edd004a4e5ff2f66a795b"
	wantConsumedEmptyDigest = "3c5082478e1d9f3f61970712fca3415f3a312ce1246fa2b27537a29de1571b56"
)

const observation = "obs-1"

var (
	vectorItems = []Item{{Name: "header", Data: []byte("header-bytes")}, {Name: "body", Data: []byte{}}}
	request0    = []byte("GET /clerk/event-record/list?from-id=1&to-time=1700000000&limit=50")
	response0   = []byte(`{"result":[]}`)
	request1    = []byte("GET /bor/span/1")
	response1   = bytes.Repeat(func() []byte {
		out := make([]byte, 256)
		for i := range out {
			out[i] = byte(i)
		}
		return out
	}(), 300)
	errCrash = errors.New("injected power loss")
	errDisk  = errors.New("injected disk error")
)

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models a native key-value store. Put stays pending until
// SyncKeyValue makes it durable. powerLoss keeps a chosen subset of pending
// writes, in order, and drops the rest. crashAt makes the mutating operation
// with that index (Put or SyncKeyValue, counted from 1) fail without effect.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
	putErr  error
	getErr  error
	hasErr  error
	syncErr error
	// deleteErr fails every Delete.
	deleteErr error
	// hasErrFor and getErrFor fail Has or Get with errDisk for a key that
	// ends with the given suffix.
	hasErrFor string
	getErrFor string
	writes    []string
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, !s.pending[i].deleted
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) has(key string) bool {
	_, ok := s.view(key)
	return ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	if s.hasErr != nil {
		return false, s.hasErr
	}
	if s.hasErrFor != "" && strings.HasSuffix(string(key), s.hasErrFor) {
		return false, errDisk
	}
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.getErrFor != "" && strings.HasSuffix(string(key), s.getErrFor) {
		return nil, errDisk
	}
	value, ok := s.view(string(key))
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}

func (s *memStore) step() error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	return nil
}

func (s *memStore) Put(key, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	s.writes = append(s.writes, string(key))
	return nil
}

func (s *memStore) Delete(key []byte) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), deleted: true})
	return nil
}

func apply(durable map[string][]byte, w write) {
	if w.deleted {
		delete(durable, w.key)
	} else {
		durable[w.key] = w.value
	}
}

func (s *memStore) SyncKeyValue() error {
	if s.syncErr != nil {
		return s.syncErr
	}
	if err := s.step(); err != nil {
		return err
	}
	for _, w := range s.pending {
		apply(s.durable, w)
	}
	s.pending = nil
	return nil
}

// powerLoss keeps pending write i when bit i of keep is set, then reopens.
func (s *memStore) powerLoss(keep uint) *memStore {
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if keep&(1<<i) != 0 {
			apply(out.durable, w)
		}
	}
	return out
}

func (s *memStore) snapshot() map[string]string {
	out := map[string]string{}
	for key, value := range s.durable {
		out[key] = string(value)
	}
	return out
}

func mustSeal(t *testing.T, db Store) Capsule {
	t.Helper()
	capsule, err := SealCapsule(db, observation, vectorItems)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return capsule
}

func mustOpen(t *testing.T, db Store) *Log {
	t.Helper()
	log, err := OpenLog(db, observation)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return log
}

func serve(response []byte) func() ([]byte, error) {
	return func() ([]byte, error) { return response, nil }
}

func noFetch(t *testing.T) func() ([]byte, error) {
	return func() ([]byte, error) {
		t.Fatalf("a saved response must be replayed, but fetch was called")
		return nil, nil
	}
}

func mustNext(t *testing.T, log *Log, request []byte, fetch func() ([]byte, error), want []byte) {
	t.Helper()
	got, err := log.Next(request, fetch)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("next returned %d bytes, want %d", len(got), len(want))
	}
}

func TestDigestVectors(t *testing.T) {
	db := newStore()
	capsule := mustSeal(t, db)
	if capsule.Digest != wantCapsuleDigest {
		t.Fatalf("capsule digest %s, want %s", capsule.Digest, wantCapsuleDigest)
	}
	if got, _ := ChainStart(capsule.Digest); got != wantChainStart {
		t.Fatalf("chain start %s, want %s", got, wantChainStart)
	}
	entry0 := Entry{Sequence: 0, RequestSHA256: sha(request0), RequestLength: uint64(len(request0)),
		ResponseSHA256: sha(response0), ResponseLength: uint64(len(response0))}
	entry1 := Entry{Sequence: 1, RequestSHA256: sha(request1), RequestLength: uint64(len(request1)),
		ResponseSHA256: sha(response1), ResponseLength: uint64(len(response1))}
	if got, _ := EntryDigest(entry0); got != wantEntry0Digest {
		t.Fatalf("entry 0 digest %s, want %s", got, wantEntry0Digest)
	}
	if got, _ := EntryDigest(entry1); got != wantEntry1Digest {
		t.Fatalf("entry 1 digest %s, want %s", got, wantEntry1Digest)
	}
	if got, _ := ChainNext(wantChainStart, wantEntry0Digest); got != wantChain1 {
		t.Fatalf("chain 1 %s, want %s", got, wantChain1)
	}
	if got, _ := ChainNext(wantChain1, wantEntry1Digest); got != wantChain2 {
		t.Fatalf("chain 2 %s, want %s", got, wantChain2)
	}
	if got, _ := ConsumedDigest(wantCapsuleDigest, 0, wantChainStart); got != wantConsumedEmptyDigest {
		t.Fatalf("empty consumed digest %s, want %s", got, wantConsumedEmptyDigest)
	}
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	mustNext(t, log, request1, serve(response1), response1)
	manifest, err := log.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	want := Consumed{Observation: observation, CapsuleDigest: wantCapsuleDigest, Count: 2, Chain: wantChain2,
		Digest: wantConsumedDigest}
	if manifest != want {
		t.Fatalf("manifest %+v, want %+v", manifest, want)
	}
}

func TestEmptyLogFinish(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	manifest, err := mustOpen(t, db).Finish()
	if err != nil || manifest.Digest != wantConsumedEmptyDigest || manifest.Count != 0 {
		t.Fatalf("empty finish %+v %v, want digest %s", manifest, err, wantConsumedEmptyDigest)
	}
}

func TestDigestRejectsMalformedHex(t *testing.T) {
	good := wantCapsuleDigest
	for _, bad := range []string{"", good[:62], good + "00", strings.ToUpper(good), "zz" + good[2:]} {
		if _, err := CapsuleDigest([]ItemRef{{Name: "a", SHA256: bad}}); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("capsule digest of %q: %v", bad, err)
		}
		if _, err := EntryDigest(Entry{RequestSHA256: bad, ResponseSHA256: good}); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("entry request %q: %v", bad, err)
		}
		if _, err := EntryDigest(Entry{RequestSHA256: good, ResponseSHA256: bad}); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("entry response %q: %v", bad, err)
		}
		if _, err := ChainStart(bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("chain start %q: %v", bad, err)
		}
		if _, err := ChainNext(bad, good); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("chain previous %q: %v", bad, err)
		}
		if _, err := ChainNext(good, bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("chain entry %q: %v", bad, err)
		}
		if _, err := ConsumedDigest(bad, 0, good); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("consumed capsule %q: %v", bad, err)
		}
		if _, err := ConsumedDigest(good, 0, bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("consumed chain %q: %v", bad, err)
		}
	}
}

func sha(data []byte) string {
	digest := sum(data)
	return fmt.Sprintf("%x", digest[:])
}

func TestCapsuleSealAndRead(t *testing.T) {
	db := newStore()
	if _, err := ReadCapsule(db, observation); !errors.Is(err, ErrNoCapsule) {
		t.Fatalf("read before seal: %v", err)
	}
	if _, err := OpenLog(db, observation); !errors.Is(err, ErrNoCapsule) {
		t.Fatalf("open before seal: %v", err)
	}
	if _, err := ReadItem(db, observation, 0); !errors.Is(err, ErrNoCapsule) {
		t.Fatalf("item before seal: %v", err)
	}
	big := bytes.Repeat([]byte{7}, 2*ChunkBytes+1)
	exact := bytes.Repeat([]byte{9}, ChunkBytes)
	items := []Item{{Name: "big", Data: big}, {Name: "empty", Data: nil}, {Name: "exact", Data: exact}}
	capsule, err := SealCapsule(db, observation, items)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for index, item := range items {
		got, err := ReadItem(db, observation, index)
		if err != nil || !bytes.Equal(got, item.Data) {
			t.Fatalf("item %d: %d bytes, %v", index, len(got), err)
		}
	}
	if n := len(db.durable); n != 1+3+0+1 {
		t.Fatalf("store holds %d keys, want manifest plus 4 chunks", n)
	}
	again, err := SealCapsule(db, observation, items)
	if err != nil || again.Digest != capsule.Digest {
		t.Fatalf("reseal: %v", err)
	}
	read, err := ReadCapsule(db, observation)
	if err != nil || read.Digest != capsule.Digest || len(read.Items) != 3 || read.Items[0].Length != uint64(len(big)) {
		t.Fatalf("read capsule %+v %v", read, err)
	}
	changed := []Item{{Name: "big", Data: big}, {Name: "empty", Data: []byte{0}}, {Name: "exact", Data: exact}}
	if _, err := SealCapsule(db, observation, changed); !errors.Is(err, ErrCapsuleConflict) {
		t.Fatalf("different capsule: %v", err)
	}
	reordered := []Item{items[1], items[0], items[2]}
	if _, err := SealCapsule(db, observation, reordered); !errors.Is(err, ErrCapsuleConflict) {
		t.Fatalf("reordered capsule: %v", err)
	}
	for _, index := range []int{-1, 3} {
		if _, err := ReadItem(db, observation, index); !errors.Is(err, ErrInvalid) {
			t.Fatalf("item index %d: %v", index, err)
		}
	}
	db.durable[string(itemChunkKey(observation, 0, 1))] = bytes.Repeat([]byte{8}, ChunkBytes)
	if _, err := SealCapsule(db, observation, items); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("reseal over a damaged chunk: %v", err)
	}
}

func TestCapsuleRejectsInvalidInput(t *testing.T) {
	db := newStore()
	for _, id := range []string{"", "A", "-x", "a/b", strings.Repeat("a", 129)} {
		if _, err := SealCapsule(db, id, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("seal id %q: %v", id, err)
		}
		if _, err := ReadCapsule(db, id); !errors.Is(err, ErrInvalid) {
			t.Fatalf("read id %q: %v", id, err)
		}
	}
	if _, err := SealCapsule(db, strings.Repeat("a", 128), nil); err != nil {
		t.Fatalf("longest id: %v", err)
	}
	for _, items := range [][]Item{{{Name: "", Data: nil}}, {{Name: "a/b"}}, {{Name: "x"}, {Name: "x"}}} {
		if _, err := SealCapsule(db, observation, items); !errors.Is(err, ErrInvalid) {
			t.Fatalf("items %+v: %v", items, err)
		}
	}
	if len(db.writes) != 1 {
		t.Fatalf("rejected seals wrote %v", db.writes)
	}
}

// Every power-loss point of a seal leaves either no capsule, which a new seal
// completes, or the whole sealed capsule.
func TestCapsulePowerLoss(t *testing.T) {
	items := []Item{{Name: "big", Data: bytes.Repeat([]byte{5}, ChunkBytes+3)}, {Name: "small", Data: []byte("x")}}
	reference := newStore()
	want, err := SealCapsule(reference, observation, items)
	if err != nil {
		t.Fatal(err)
	}
	total := reference.ops
	for crash := 1; crash <= total+1; crash++ {
		probe := newStore()
		probe.crashAt = crash
		_, sealErr := SealCapsule(probe, observation, items)
		if (sealErr == nil) != (crash > total) {
			t.Fatalf("crash %d: seal error %v", crash, sealErr)
		}
		for keep := uint(0); keep < 1<<len(probe.pending); keep++ {
			db := probe.powerLoss(keep)
			capsule, err := ReadCapsule(db, observation)
			if errors.Is(err, ErrNoCapsule) {
				if crash > total {
					t.Fatalf("crash %d keep %b: capsule lost after its final sync", crash, keep)
				}
				if capsule, err = SealCapsule(db, observation, items); err != nil {
					t.Fatalf("crash %d keep %b: reseal %v", crash, keep, err)
				}
			} else if err != nil {
				t.Fatalf("crash %d keep %b: %v", crash, keep, err)
			}
			if capsule.Digest != want.Digest {
				t.Fatalf("crash %d keep %b: digest %s", crash, keep, capsule.Digest)
			}
			for index, item := range items {
				if got, err := ReadItem(db, observation, index); err != nil || !bytes.Equal(got, item.Data) {
					t.Fatalf("crash %d keep %b: item %d %v", crash, keep, index, err)
				}
			}
		}
	}
}

// AC-CAP-04-01: the response is synced but not yet handed to native code when
// power is lost, and the external response then changes. The retry returns
// the saved bytes and request sequence; the new response is never used.
func TestReplayReturnsSavedResponseAfterPowerLoss(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	if _, err := log.Next(request0, serve(response0)); err != nil {
		t.Fatal(err)
	}
	// Power loss before the hand-off: the returned bytes are dropped.
	db = db.powerLoss(0)
	// The external response has changed; noFetch fails the test if the retry
	// asks for it.
	retry := mustOpen(t, db)
	if retry.Frontier() != 1 || retry.Cursor() != 0 {
		t.Fatalf("frontier %d cursor %d, want 1 and 0", retry.Frontier(), retry.Cursor())
	}
	mustNext(t, retry, request0, noFetch(t), response0)
	if retry.Cursor() != 1 {
		t.Fatalf("cursor %d after replay", retry.Cursor())
	}
	manifest, err := retry.Finish()
	if err != nil || manifest.Digest != wantConsumedOneDigest {
		t.Fatalf("manifest %+v %v, want %s", manifest, err, wantConsumedOneDigest)
	}
}

// Every power-loss point of one Next, with every subset of unsynced writes
// kept. Until the frontier sync, the response was never handed out and the
// retry fetches the changed response; after it, the retry replays the saved
// one. Either way the log stays readable and the final digest matches a clean
// run of the bytes actually consumed.
func TestNextPowerLossAtEveryPoint(t *testing.T) {
	for _, size := range []int{0, 1, ChunkBytes, 2*ChunkBytes + 5} {
		old := bytes.Repeat([]byte{1}, size)
		changed := bytes.Repeat([]byte{2}, size/2+1)
		base := newStore()
		mustSeal(t, base)
		first := mustOpen(t, base)
		mustNext(t, first, request0, serve(response0), response0)
		start := base.ops
		reference := maps.Clone(base.durable)
		mustNext(t, first, request1, serve(old), old)
		total := base.ops - start
		for crash := 1; crash <= total+1; crash++ {
			probe := &memStore{durable: maps.Clone(reference)}
			log := mustOpen(t, probe)
			probe.crashAt = probe.ops + crash
			mustNext(t, log, request0, noFetch(t), response0)
			got, err := log.Next(request1, serve(old))
			if crash <= total {
				if !errors.Is(err, errCrash) {
					t.Fatalf("size %d crash %d: %v", size, crash, err)
				}
				if _, again := log.Next(request1, serve(old)); !errors.Is(again, ErrFailed) {
					t.Fatalf("size %d crash %d: log continued after an error: %v", size, crash, again)
				}
				if _, again := log.Finish(); !errors.Is(again, ErrFailed) {
					t.Fatalf("size %d crash %d: finish after an error: %v", size, crash, again)
				}
			} else if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("size %d: clean next %v", size, err)
			}
			for keep := uint(0); keep < 1<<len(probe.pending); keep++ {
				db := probe.powerLoss(keep)
				retry := mustOpen(t, db)
				// The frontier put is the next-to-last operation; when the final
				// sync fails, a kept frontier write is durable but was not handed
				// out, and replaying it is allowed.
				durable := retry.Frontier() == 2
				if (crash > total && !durable) || (crash < total && durable) {
					t.Fatalf("size %d crash %d keep %b: frontier %d", size, crash, keep, retry.Frontier())
				}
				mustNext(t, retry, request0, noFetch(t), response0)
				want := changed
				fetch := serve(changed)
				if durable {
					want, fetch = old, noFetch(t)
				}
				mustNext(t, retry, request1, fetch, want)
				manifest, err := retry.Finish()
				if err != nil {
					t.Fatalf("size %d crash %d keep %b: finish %v", size, crash, keep, err)
				}
				clean := newStore()
				mustSeal(t, clean)
				cleanLog := mustOpen(t, clean)
				mustNext(t, cleanLog, request0, serve(response0), response0)
				mustNext(t, cleanLog, request1, serve(want), want)
				expected, _ := cleanLog.Finish()
				if manifest != expected {
					t.Fatalf("size %d crash %d keep %b: manifest %+v, want %+v", size, crash, keep, manifest, expected)
				}
				reopened := mustOpen(t, db)
				mustNext(t, reopened, request0, noFetch(t), response0)
				mustNext(t, reopened, request1, noFetch(t), want)
			}
		}
	}
}

// The second sync of Next is the commit point: a frontier write that
// survives without it would hand out nothing, and an entry that survives
// without the frontier is overwritten by a shorter tail.
func TestOrphanEntryIsOverwritten(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	db.crashAt = db.ops + 6 // 3 chunks, entry, sync, then the frontier put fails
	long := bytes.Repeat([]byte{3}, 2*ChunkBytes+1)
	if _, err := log.Next(request1, serve(long)); !errors.Is(err, errCrash) {
		t.Fatalf("crash: %v", err)
	}
	db = db.powerLoss(0)
	if _, ok := db.durable[string(entryKey(observation, 1))]; !ok {
		t.Fatal("the orphan entry should be durable")
	}
	retry := mustOpen(t, db)
	mustNext(t, retry, request0, noFetch(t), response0)
	mustNext(t, retry, []byte("another request"), serve([]byte("short")), []byte("short"))
	reopened := mustOpen(t, db)
	mustNext(t, reopened, request0, noFetch(t), response0)
	mustNext(t, reopened, []byte("another request"), noFetch(t), []byte("short"))
}

// AC-CAP-04-02: a retry sends a different request at a durable offset. The
// conflict is an error, fetch is not called, and the saved prefix is unchanged.
func TestPrefixConflict(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	mustNext(t, log, request1, serve(response1), response1)
	before := db.snapshot()
	retry := mustOpen(t, db)
	mustNext(t, retry, request0, noFetch(t), response0)
	_, err := retry.Next([]byte("GET /bor/span/2"), noFetch(t))
	if !errors.Is(err, ErrPrefixConflict) {
		t.Fatalf("different request at offset 1: %v", err)
	}
	if _, err := retry.Next(request1, noFetch(t)); !errors.Is(err, ErrFailed) {
		t.Fatalf("log continued after a conflict: %v", err)
	}
	if after := db.snapshot(); !maps.Equal(before, after) || len(db.pending) != 0 {
		t.Fatal("a prefix conflict changed the store")
	}
	first := mustOpen(t, db)
	if _, err := first.Next(request1, noFetch(t)); !errors.Is(err, ErrPrefixConflict) {
		t.Fatalf("different request at offset 0: %v", err)
	}
	again := mustOpen(t, db)
	mustNext(t, again, request0, noFetch(t), response0)
	mustNext(t, again, request1, noFetch(t), response1)
}

// AC-CAP-04-03: the saved prefix replays without conflict, then a request
// beyond the durable frontier appends a new tail. The old prefix keeps its
// bytes, and the final consumed digest is bound to a test-only stand-in for
// the EXEC_READY marker (D98); the real binding belongs to E01-02.admission.
func TestTailAppendAndStandInMarker(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	first := mustOpen(t, db)
	mustNext(t, first, request0, serve(response0), response0)
	prefix := db.snapshot()
	retry := mustOpen(t, db)
	mustNext(t, retry, request0, noFetch(t), response0)
	fetched := 0
	mustNext(t, retry, request1, func() ([]byte, error) { fetched++; return response1, nil }, response1)
	if fetched != 1 || retry.Frontier() != 2 {
		t.Fatalf("tail append fetched %d times, frontier %d", fetched, retry.Frontier())
	}
	after := db.snapshot()
	for key, value := range prefix {
		if key == string(frontierKey(observation)) {
			continue
		}
		if after[key] != value {
			t.Fatalf("tail append changed prefix key %s", key)
		}
	}
	if _, ok := retry.Closed(); ok {
		t.Fatalf("an open log reports a closed manifest")
	}
	manifest, err := retry.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := retry.Closed(); !ok || got != manifest {
		t.Fatalf("closed manifest after finish %+v %v", got, ok)
	}
	if got, ok := mustOpen(t, db).Closed(); !ok || got != manifest {
		t.Fatalf("closed manifest of a reopened log %+v %v", got, ok)
	}
	marker := standInMarker{Observation: observation, ConsumedDigest: manifest.Digest}
	if marker.ConsumedDigest != wantConsumedDigest {
		t.Fatalf("stand-in marker binds %s, want %s", marker.ConsumedDigest, wantConsumedDigest)
	}
	if _, err := retry.Next([]byte("GET /bor/span/3"), noFetch(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after finish: %v", err)
	}
	writes := len(db.writes)
	again, err := retry.Finish()
	if err != nil || again != manifest || len(db.writes) != writes {
		t.Fatalf("second finish %+v %v wrote %d records", again, err, len(db.writes)-writes)
	}
	closed := mustOpen(t, db)
	if _, err := closed.Finish(); !errors.Is(err, ErrUnconsumed) {
		t.Fatalf("finish before replay: %v", err)
	}
	mustNext(t, closed, request0, noFetch(t), response0)
	if _, err := closed.Finish(); !errors.Is(err, ErrUnconsumed) {
		t.Fatalf("finish after a partial replay: %v", err)
	}
	mustNext(t, closed, request1, noFetch(t), response1)
	if _, err := closed.Next([]byte("GET /bor/span/3"), noFetch(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("append to a reopened closed log: %v", err)
	}
	replayed, err := closed.Finish()
	if err != nil || replayed != manifest || len(db.writes) != writes {
		t.Fatalf("finish of the reopened log %+v %v", replayed, err)
	}
}

// standInMarker stands in for the EXEC_READY marker in this test only. The
// real binding is the EXEC_READY record of E01-02.admission.
type standInMarker struct {
	Observation    string
	ConsumedDigest string
}

func TestFinishPowerLoss(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	db.crashAt = db.ops + 2
	if _, err := log.Finish(); !errors.Is(err, errCrash) {
		t.Fatalf("finish crash: %v", err)
	}
	for keep := uint(0); keep < 1<<len(db.pending); keep++ {
		reopened := mustOpen(t, db.powerLoss(keep))
		mustNext(t, reopened, request0, noFetch(t), response0)
		_, err := reopened.Next(request1, serve(response1))
		if (keep == 1) != errors.Is(err, ErrClosed) || (keep == 0 && err != nil) {
			t.Fatalf("keep %b: next after an interrupted finish: %v", keep, err)
		}
	}
}

func TestFetchErrorWritesNothing(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	before := len(db.writes)
	if _, err := log.Next(request0, func() ([]byte, error) { return nil, errDisk }); !errors.Is(err, errDisk) {
		t.Fatalf("fetch error: %v", err)
	}
	if len(db.writes) != before || log.Frontier() != 0 || log.Cursor() != 0 {
		t.Fatalf("a fetch error wrote %v", db.writes[before:])
	}
	mustNext(t, log, request0, serve(response0), response0)
}

func TestNextKeepsItsOwnCopy(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	source := []byte("abc")
	got, err := log.Next(request0, serve(source))
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'x'
	if string(got) != "abc" {
		t.Fatalf("returned bytes alias the fetch buffer: %q", got)
	}
}

func TestNextRejectsEmptyRequest(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	if _, err := log.Next(nil, noFetch(t)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty request: %v", err)
	}
	mustNext(t, log, request0, serve(response0), response0)
}

func TestStoreErrorsStopTheLog(t *testing.T) {
	cases := map[string]func(*memStore){
		"put":  func(s *memStore) { s.putErr = errDisk },
		"sync": func(s *memStore) { s.syncErr = errDisk },
	}
	for name, inject := range cases {
		db := newStore()
		mustSeal(t, db)
		log := mustOpen(t, db)
		inject(db)
		if _, err := log.Next(request0, serve(response0)); !errors.Is(err, errDisk) {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := log.Next(request0, serve(response0)); !errors.Is(err, ErrFailed) {
			t.Fatalf("%s: log continued: %v", name, err)
		}
		db = newStore()
		mustSeal(t, db)
		log = mustOpen(t, db)
		inject(db)
		if _, err := log.Finish(); !errors.Is(err, errDisk) {
			t.Fatalf("%s: finish %v", name, err)
		}
		if _, err := log.Finish(); !errors.Is(err, ErrFailed) {
			t.Fatalf("%s: finish continued: %v", name, err)
		}
		db = newStore()
		inject(db)
		if _, err := SealCapsule(db, observation, vectorItems); !errors.Is(err, errDisk) {
			t.Fatalf("%s: seal %v", name, err)
		}
	}
	readCases := map[string]func(*memStore){
		"has": func(s *memStore) { s.hasErr = errDisk },
		"get": func(s *memStore) { s.getErr = errDisk },
	}
	for name, inject := range readCases {
		db := newStore()
		mustSeal(t, db)
		inject(db)
		if _, err := OpenLog(db, observation); !errors.Is(err, errDisk) {
			t.Fatalf("%s: open %v", name, err)
		}
		if _, err := SealCapsule(db, observation, vectorItems); !errors.Is(err, errDisk) {
			t.Fatalf("%s: seal %v", name, err)
		}
	}
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	replay := mustOpen(t, db)
	db.getErr = errDisk
	if _, err := replay.Next(request0, noFetch(t)); !errors.Is(err, errDisk) {
		t.Fatalf("replay read error: %v", err)
	}
	if _, err := replay.Next(request0, noFetch(t)); !errors.Is(err, ErrFailed) {
		t.Fatalf("replay continued: %v", err)
	}
}

// Damaged records fail closed when opened or replayed.
func TestCorruptRecords(t *testing.T) {
	build := func() *memStore {
		db := newStore()
		mustSeal(t, db)
		log := mustOpen(t, db)
		mustNext(t, log, request0, serve(response0), response0)
		mustNext(t, log, request1, serve(response1), response1)
		return db
	}
	entry1 := string(entryKey(observation, 1))
	openCases := map[string]func(*memStore){
		"capsule unknown field": func(s *memStore) {
			key := string(capsuleKey(observation))
			s.durable[key] = append([]byte(`{"x":1,`), s.durable[key][1:]...)
		},
		"capsule trailing data": func(s *memStore) {
			key := string(capsuleKey(observation))
			s.durable[key] = append(bytes.Clone(s.durable[key]), []byte(" {}")...)
		},
		"capsule digest": func(s *memStore) {
			key := string(capsuleKey(observation))
			s.durable[key] = bytes.Replace(s.durable[key], []byte(wantCapsuleDigest), []byte(wantChainStart), 1)
		},
		"capsule observation": func(s *memStore) {
			key := string(capsuleKey(observation))
			s.durable[key] = bytes.Replace(s.durable[key], []byte(`"obs-1"`), []byte(`"obs-2"`), 1)
		},
		"capsule item digest": func(s *memStore) {
			key := string(capsuleKey(observation))
			s.durable[key] = bytes.Replace(s.durable[key], []byte(`"sha256":"`), []byte(`"sha256":"X`), 1)
		},
		"missing entry":   func(s *memStore) { delete(s.durable, entry1) },
		"malformed entry": func(s *memStore) { s.durable[entry1] = []byte("{") },
		"entry sequence": func(s *memStore) {
			s.durable[entry1] = bytes.Replace(s.durable[entry1], []byte(`"sequence":1`), []byte(`"sequence":0`), 1)
		},
		"entry digest": func(s *memStore) {
			s.durable[entry1] = bytes.Replace(s.durable[entry1], []byte(`"response_length":76800`), []byte(`"response_length":76801`), 1)
		},
		"entry bad sha": func(s *memStore) {
			s.durable[entry1] = bytes.Replace(s.durable[entry1], []byte(`"request_sha256":"`), []byte(`"request_sha256":"X`), 1)
		},
		"frontier chain": func(s *memStore) {
			s.durable[string(frontierKey(observation))] = []byte(`{"count":2,"chain":"` + wantChain1 + `"}`)
		},
		"frontier count": func(s *memStore) {
			s.durable[string(frontierKey(observation))] = []byte(`{"count":1,"chain":"` + wantChain2 + `"}`)
		},
		"frontier format": func(s *memStore) { s.durable[string(frontierKey(observation))] = []byte(`[]`) },
		"closed digest": func(s *memStore) {
			s.durable[string(closedKey(observation))] = closedRecord(wantConsumedOneDigest, 2, wantChain2)
		},
		"closed count": func(s *memStore) {
			s.durable[string(closedKey(observation))] = closedRecord(wantConsumedDigest, 1, wantChain2)
		},
		"closed format": func(s *memStore) { s.durable[string(closedKey(observation))] = []byte(`{"digest":1}`) },
		"closed chain": func(s *memStore) {
			s.durable[string(closedKey(observation))] = closedRecord(wantConsumedDigest, 2, wantChain1)
		},
		"closed capsule": func(s *memStore) {
			s.durable[string(closedKey(observation))] = bytes.Replace(closedRecord(wantConsumedDigest, 2, wantChain2), []byte(wantCapsuleDigest), []byte(wantChainStart), 1)
		},
		"closed observation": func(s *memStore) {
			s.durable[string(closedKey(observation))] = bytes.Replace(closedRecord(wantConsumedDigest, 2, wantChain2), []byte(`"obs-1"`), []byte(`"obs-2"`), 1)
		},
	}
	for name, damage := range openCases {
		db := build()
		if _, err := OpenLog(db, observation); err != nil {
			t.Fatalf("%s: undamaged open %v", name, err)
		}
		damage(db)
		if _, err := OpenLog(db, observation); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s: open %v", name, err)
		}
	}
	replayCases := map[string]func(*memStore){
		"changed chunk": func(s *memStore) { s.durable[string(responseChunkKey(observation, 1, 0))][0] ^= 1 },
		"short chunk": func(s *memStore) {
			key := string(responseChunkKey(observation, 1, 1))
			s.durable[key] = s.durable[key][1:]
		},
		"shifted byte": func(s *memStore) {
			first, second := string(responseChunkKey(observation, 1, 0)), string(responseChunkKey(observation, 1, 1))
			last := s.durable[first][len(s.durable[first])-1]
			s.durable[first] = s.durable[first][:len(s.durable[first])-1]
			s.durable[second] = append([]byte{last}, s.durable[second]...)
		},
		"missing chunk": func(s *memStore) { delete(s.durable, string(responseChunkKey(observation, 1, 1))) },
		"chunk too long": func(s *memStore) {
			key := string(responseChunkKey(observation, 1, 0))
			s.durable[key] = append(s.durable[key], 0)
		},
	}
	for name, damage := range replayCases {
		db := build()
		damage(db)
		log := mustOpen(t, db)
		mustNext(t, log, request0, noFetch(t), response0)
		if _, err := log.Next(request1, noFetch(t)); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s: replay %v", name, err)
		}
		if _, err := log.Next(request1, noFetch(t)); !errors.Is(err, ErrFailed) {
			t.Fatalf("%s: log continued: %v", name, err)
		}
	}
	db := build()
	log := mustOpen(t, db)
	delete(db.durable, entry1)
	mustNext(t, log, request0, noFetch(t), response0)
	if _, err := log.Next(request1, noFetch(t)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("entry removed after open: %v", err)
	}
	db = build()
	key := string(capsuleKey(observation))
	db.durable[key] = bytes.Replace(db.durable[key], []byte(wantCapsuleDigest), []byte(wantChainStart), 1)
	if _, err := ReadCapsule(db, observation); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("capsule manifest with a wrong digest: %v", err)
	}
	db = build()
	db.durable[string(itemChunkKey(observation, 0, 0))][0] ^= 1
	if _, err := ReadItem(db, observation, 0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged capsule item: %v", err)
	}
}

func closedRecord(digest string, count int, chain string) []byte {
	return []byte(fmt.Sprintf(`{"observation":"obs-1","capsule_digest":"%s","count":%d,"chain":"%s","digest":"%s"}`,
		wantCapsuleDigest, count, chain, digest))
}

// No key can be deleted by the native offline pruner (32-byte trie node keys
// and code keys), and every key stays inside this package's namespace.
func TestKeysStayOutsidePrunedSpace(t *testing.T) {
	db := newStore()
	if _, err := SealCapsule(db, "a", []Item{{Name: "x", Data: []byte("1")}}); err != nil {
		t.Fatal(err)
	}
	log, err := OpenLog(db, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Next([]byte("r"), serve([]byte("1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(db.durable) != 6 {
		t.Fatalf("wrote %d keys, want 6", len(db.durable))
	}
	for key := range db.durable {
		if len(key) == 32 || len(key) == 33 || !strings.HasPrefix(key, keyPrefix+"a/") {
			t.Fatalf("key %q can collide with native or foreign keys", key)
		}
	}
}

// After a process crash without power loss, a frontier or manifest that was
// written but not synced can still be visible. Opening the log, or sealing
// the same capsule again, syncs it before relying on it, so a later power
// loss cannot drop a response that was replayed.
func TestProcessCrashVisibleWritesAreSyncedOnOpen(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	db.crashAt = db.ops + 5 // chunk, entry, sync, frontier put, then the final sync fails
	if _, err := log.Next(request0, serve(response0)); !errors.Is(err, errCrash) {
		t.Fatalf("crash: %v", err)
	}
	db.crashAt = 0 // the process restarts on the same store; pending writes stay visible
	reopened := mustOpen(t, db)
	if reopened.Frontier() != 1 {
		t.Fatalf("visible frontier %d, want 1", reopened.Frontier())
	}
	mustNext(t, reopened, request0, noFetch(t), response0)
	afterPowerLoss := mustOpen(t, db.powerLoss(0))
	if afterPowerLoss.Frontier() != 1 {
		t.Fatal("a replayed response was lost by a later power loss")
	}
	mustNext(t, afterPowerLoss, request0, noFetch(t), response0)

	db = newStore()
	db.crashAt = 4 // chunk, sync, manifest put, then the final sync fails
	if _, err := SealCapsule(db, observation, vectorItems); !errors.Is(err, errCrash) {
		t.Fatalf("seal crash: %v", err)
	}
	db.crashAt = 0
	if _, err := SealCapsule(db, observation, vectorItems); err != nil {
		t.Fatalf("reseal after a process crash: %v", err)
	}
	if _, err := ReadCapsule(db.powerLoss(0), observation); err != nil {
		t.Fatalf("a resealed capsule was lost by a later power loss: %v", err)
	}
}

func TestChunkReadErrorsPassThrough(t *testing.T) {
	for _, field := range []string{"has", "get"} {
		db := newStore()
		mustSeal(t, db)
		log := mustOpen(t, db)
		mustNext(t, log, request0, serve(response0), response0)
		replay := mustOpen(t, db)
		suffix := string(responseChunkKey(observation, 0, 0))
		if field == "has" {
			db.hasErrFor = suffix
		} else {
			db.getErrFor = suffix
		}
		if _, err := replay.Next(request0, noFetch(t)); !errors.Is(err, errDisk) || errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s error on a chunk: %v", field, err)
		}
		if _, err := replay.Next(request0, noFetch(t)); !errors.Is(err, ErrFailed) {
			t.Fatalf("%s: replay continued: %v", field, err)
		}
	}
	db := newStore()
	mustSeal(t, db)
	db.syncErr = errDisk
	if _, err := OpenLog(db, observation); !errors.Is(err, errDisk) {
		t.Fatalf("open sync error: %v", err)
	}
	if _, err := SealCapsule(db, observation, vectorItems); !errors.Is(err, errDisk) {
		t.Fatalf("reseal sync error: %v", err)
	}
}

// purgeFixture seals the vector capsule, logs both vector responses and
// closes the log, then leaves an interrupted third response at the frontier.
func purgeFixture(t *testing.T) *memStore {
	t.Helper()
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	mustNext(t, log, request1, serve(response1), response1)
	if _, err := log.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return db
}

func countPrefix(db *memStore, prefix string) int {
	count := 0
	for key := range db.durable {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

func TestPurgeDeletesEveryRecord(t *testing.T) {
	db := purgeFixture(t)
	other := "obs-2"
	if _, err := SealCapsule(db, other, vectorItems); err != nil {
		t.Fatalf("seal other: %v", err)
	}
	before := countPrefix(db, keyPrefix+other+"/")
	// Capsule, header chunk, frontier, closed, two entries, one chunk of
	// response0 and two chunks of response1.
	if got := countPrefix(db, keyPrefix+observation+"/"); got != 9 {
		t.Fatalf("fixture holds %d keys, want 9", got)
	}
	if err := Purge(db, observation); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if len(db.pending) != 0 {
		t.Fatalf("purge left %d unsynced writes", len(db.pending))
	}
	if got := countPrefix(db, keyPrefix+observation+"/"); got != 0 {
		t.Fatalf("purge left %d keys: %v", got, db.snapshot())
	}
	if got := countPrefix(db, keyPrefix+other+"/"); got != before {
		t.Fatalf("purge changed another observation: %d keys, want %d", got, before)
	}
	if err := Purge(db, observation); err != nil {
		t.Fatalf("repeated purge: %v", err)
	}
	if _, err := ReadCapsule(db, observation); !errors.Is(err, ErrNoCapsule) {
		t.Fatalf("read after purge: %v", err)
	}
	if err := Purge(db, "Bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid id: %v", err)
	}
}

func TestPurgeIncludesTheEntryAtTheFrontier(t *testing.T) {
	db := newStore()
	mustSeal(t, db)
	log := mustOpen(t, db)
	mustNext(t, log, request0, serve(response0), response0)
	// An interrupted Next: the entry and chunks of the second response are
	// durable, the advanced frontier is not.
	db.crashAt = db.ops + 5
	if _, err := log.Next(request1, serve(response1)); !errors.Is(err, errCrash) {
		t.Fatalf("interrupted next: %v", err)
	}
	db = db.powerLoss(0)
	if !db.has(string(entryKey(observation, 1))) {
		t.Fatalf("fixture lost the entry at the frontier")
	}
	if err := Purge(db, observation); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if got := countPrefix(db, keyPrefix+observation+"/"); got != 0 {
		t.Fatalf("purge left %d keys: %v", got, db.snapshot())
	}
}

func TestPurgeOfAnUnsealedObservation(t *testing.T) {
	db := newStore()
	if err := Purge(db, observation); err != nil {
		t.Fatalf("purge without capsule: %v", err)
	}
	if len(db.durable) != 0 || len(db.writes) != 0 {
		t.Fatalf("purge without capsule wrote %v", db.writes)
	}
}

func TestPurgePowerLossAtEveryPoint(t *testing.T) {
	probe := purgeFixture(t)
	if err := Purge(probe, observation); err != nil {
		t.Fatalf("probe purge: %v", err)
	}
	total := probe.ops - purgeFixture(t).ops
	for crash := 1; crash <= total; crash++ {
		for _, keep := range []uint{0, 1, 0b0101, ^uint(0)} {
			db := purgeFixture(t)
			db.crashAt = db.ops + crash
			if err := Purge(db, observation); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			db = db.powerLoss(keep)
			if left := countPrefix(db, keyPrefix+observation+"/"); left == 0 {
				continue
			}
			if !db.has(string(purgeKey(observation))) {
				// Before the plan sync nothing is deleted: the log still opens.
				if _, err := OpenLog(db, observation); err != nil {
					t.Fatalf("crash %d keep %b: log without a plan: %v", crash, keep, err)
				}
			}
			if err := Purge(db, observation); err != nil {
				t.Fatalf("crash %d keep %b: repeated purge: %v", crash, keep, err)
			}
			if left := countPrefix(db, keyPrefix+observation+"/"); left != 0 {
				t.Fatalf("crash %d keep %b: %d keys left: %v", crash, keep, left, db.snapshot())
			}
		}
	}
}

func TestPurgeErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(db *memStore)
	}{
		{"plan read", func(db *memStore) { db.hasErrFor = "/purge" }},
		{"capsule read", func(db *memStore) { db.getErrFor = "/capsule" }},
		{"frontier read", func(db *memStore) { db.getErrFor = "/frontier" }},
		{"entry read", func(db *memStore) { db.getErrFor = "/log/0000000000000001" }},
		{"plan write", func(db *memStore) { db.putErr = errDisk }},
		{"sync", func(db *memStore) { db.syncErr = errDisk }},
		{"delete", func(db *memStore) { db.deleteErr = errDisk }},
	} {
		db := purgeFixture(t)
		tc.set(db)
		if err := Purge(db, observation); !errors.Is(err, errDisk) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	// The plan is synced before the first deletion, so a failed deletion
	// is finished by a repeated purge.
	db := purgeFixture(t)
	db.crashAt = db.ops + 3
	if err := Purge(db, observation); !errors.Is(err, errCrash) {
		t.Fatalf("crash in the deletions: %v", err)
	}
	db = db.powerLoss(^uint(0))
	if err := Purge(db, observation); err != nil || countPrefix(db, keyPrefix+observation+"/") != 0 {
		t.Fatalf("finish after crash: %v, left %v", err, db.snapshot())
	}
}

func TestPurgeRefusesDamagedRecords(t *testing.T) {
	db := purgeFixture(t)
	db.durable[string(entryKey(observation, 0))] = []byte("{")
	if err := Purge(db, observation); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged entry: %v", err)
	}
	db = purgeFixture(t)
	delete(db.durable, string(entryKey(observation, 0)))
	if err := Purge(db, observation); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing entry below the frontier: %v", err)
	}
	db = purgeFixture(t)
	db.durable[string(purgeKey(observation))] = []byte(`{"items":[1],"responses":[],"extra":1}`)
	if err := Purge(db, observation); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged plan: %v", err)
	}
	db = purgeFixture(t)
	db.durable[string(capsuleKey(observation))] = []byte(`{"observation":"obs-1","items":[],"digest":"00"}`)
	if err := Purge(db, observation); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("damaged capsule: %v", err)
	}
}
