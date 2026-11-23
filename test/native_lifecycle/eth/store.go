package eth

import (
	"bytes"
	"errors"
	"fmt"
	"maps"

	"github.com/ethereum/go-ethereum/extractor/admission"
)

// errCrash is the injected power loss of the model store.
var errCrash = errors.New("injected power loss")

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models the native key-value store. Put and Delete stay pending
// until SyncKeyValue makes them durable; powerLoss keeps a chosen subset of
// the pending writes. crashAt makes the mutating operation with that index
// (counted from 1) fail without effect.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
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

func (s *memStore) Has(key []byte) (bool, error) {
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
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
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	return nil
}

func (s *memStore) Delete(key []byte) error {
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), deleted: true})
	return nil
}

func (s *memStore) SyncKeyValue() error {
	if err := s.step(); err != nil {
		return err
	}
	for _, w := range s.pending {
		if w.deleted {
			delete(s.durable, w.key)
		} else {
			s.durable[w.key] = w.value
		}
	}
	s.pending = nil
	return nil
}

// putString is a model helper for the manifest ledger records.
func (s *memStore) putString(key, value string) {
	if err := s.Put([]byte(key), []byte(value)); err != nil {
		panic(err)
	}
}

// durableString returns the durable value of a manifest ledger key.
func (s *memStore) durableString(key string) (string, bool) {
	value, ok := s.durable[key]
	return string(value), ok
}

// powerLoss keeps pending write i when bit i of keep is set and returns the
// reopened store.
func (s *memStore) powerLoss(keep uint) *memStore {
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if keep&(1<<i) != 0 {
			if w.deleted {
				delete(out.durable, w.key)
			} else {
				out.durable[w.key] = w.value
			}
		}
	}
	return out
}

// fakePins is a durable pin ledger.
type fakePins struct {
	roots   map[string]string
	digests map[string]string
}

func (p *fakePins) Verify(id, root string) (string, error) {
	got, ok := p.roots[id]
	if !ok {
		return "", errors.New("no such pin")
	}
	if got != root {
		return "", fmt.Errorf("pin %s holds root %s", id, got)
	}
	return p.digests[id], nil
}

func (p *fakePins) Release(id string) error {
	delete(p.roots, id)
	delete(p.digests, id)
	return nil
}

// fakeInputs holds the sealed capsules and closed consumed manifests.
type fakeInputs struct {
	capsules map[string]string
	consumed map[string]admission.Consumed
}

func (f *fakeInputs) CapsuleDigest(id string) (string, error) {
	digest, ok := f.capsules[id]
	if !ok {
		return "", errors.New("capsule is not sealed")
	}
	return digest, nil
}

func (f *fakeInputs) Consumed(id string) (admission.Consumed, error) {
	manifest, ok := f.consumed[id]
	if !ok {
		return admission.Consumed{}, errors.New("log is not closed")
	}
	return manifest, nil
}

// fakeRoots holds the durable native state roots.
type fakeRoots struct {
	durable map[string]bool
}

func (r *fakeRoots) Durable(root string) (bool, error) { return r.durable[root], nil }

// noMirror is the applied-decision frontier before E05-01.registry: no
// decision is applied.
type noMirror struct{}

func (noMirror) Applied(uint64, string) (bool, error) { return false, nil }
