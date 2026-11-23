package registry

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
)

// errCrash reports the simulated power loss of a store that stops syncing.
var errCrash = errors.New("simulated power loss")

// memStore is the memory-backed durable store of the tests. Writes become
// visible at once but survive a power loss only after SyncKeyValue, and a
// power loss may keep any subset of the unsynced writes, as the durability
// contract of Store allows. failAt makes the SyncKeyValue call fail once
// that many syncs succeeded, which crashes the caller mid-commit.
type memStore struct {
	mu      sync.Mutex
	durable map[string][]byte
	pending []write
	syncs   int
	failAt  int
}

type write struct {
	key   string
	value []byte
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

// view returns the visible value of key and whether it exists.
func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, true
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.view(string(key))
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}

func (s *memStore) Put(key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	return nil
}

func (s *memStore) SyncKeyValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncs++
	if s.failAt != 0 && s.syncs > s.failAt {
		return errCrash
	}
	for _, w := range s.pending {
		s.durable[w.key] = w.value
	}
	s.pending = nil
	return nil
}

func (s *memStore) Range(prefix []byte, visit func(key, value []byte) error) error {
	s.mu.Lock()
	keys := make([]string, 0, len(s.durable)+len(s.pending))
	merged := map[string][]byte{}
	for key, value := range s.durable {
		if bytes.HasPrefix([]byte(key), prefix) {
			keys = append(keys, key)
			merged[key] = value
		}
	}
	for _, w := range s.pending {
		if bytes.HasPrefix([]byte(w.key), prefix) {
			if _, seen := merged[w.key]; !seen {
				keys = append(keys, w.key)
			}
			merged[w.key] = w.value
		}
	}
	s.mu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		if err := visit([]byte(key), bytes.Clone(merged[key])); err != nil {
			return err
		}
	}
	return nil
}

// powerLoss returns the store a restart would see: every synced write plus
// the unsynced writes whose bit in keep is set.
func (s *memStore) powerLoss(keep uint) *memStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if keep&(1<<i) != 0 {
			out.durable[w.key] = w.value
		}
	}
	return out
}

// snapshot returns the visible records as printable strings, sorted by key.
func (s *memStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := slices.Collect(maps.Keys(s.durable))
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, fmt.Sprintf("%s=%s", key, s.durable[key]))
	}
	return out
}
