package storage

import (
	"sort"
	"sync"
)

// MemoryStorage is an in-memory Storage used by tests and by tools that run
// without RocksDB. It is a full implementation of the seam contract: Apply is
// atomic under one lock, values are copied in and out, and iteration walks a
// snapshot of the range so a visit is never disturbed by writes.
//
// MemoryStorage models durability (an Apply that returned is visible to every
// later reader, including a Store reopened on the same MemoryStorage), but it
// obviously cannot model power loss of a disk. Crash-recovery tests use it by
// dropping the Store object and reopening over the same MemoryStorage, which
// is the same view a recovered RocksDB process would see after sync commits.
type MemoryStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemory returns an empty memory-backed storage.
func NewMemory() *MemoryStorage {
	return &MemoryStorage{data: make(map[string][]byte)}
}

// Get returns a copy of the value for key.
func (m *MemoryStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

// Put stores a copy of value under key.
func (m *MemoryStorage) Put(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = append([]byte(nil), value...)
	return nil
}

// Delete removes key; a missing key is not an error.
func (m *MemoryStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

// Apply applies the whole batch under one lock, or nothing. It rejects an
// oversized batch before mutating any state.
func (m *MemoryStorage) Apply(b *Batch) error {
	if b.Size() > MaxBatchBytes {
		return ErrBatchTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range b.ops {
		if op.del {
			delete(m.data, string(op.key))
			continue
		}
		m.data[string(op.key)] = append([]byte(nil), op.value...)
	}
	return nil
}

// Iterate walks a snapshot of [lo, hi) in ascending byte order. The snapshot
// is taken under the read lock; visit then runs outside it, so a visit may
// itself mutate the storage.
func (m *MemoryStorage) Iterate(lo, hi []byte, visit func(key, value []byte) error) error {
	for _, kv := range m.snapshot(lo, hi) {
		if err := visit(kv[0], kv[1]); err != nil {
			if err == ErrStopIterate {
				return nil
			}
			return err
		}
	}
	return nil
}

func (m *MemoryStorage) snapshot(lo, hi []byte) [][2][]byte {
	m.mu.RLock()
	pairs := make([][2][]byte, 0, len(m.data))
	for k, v := range m.data {
		if !inRange(lo, hi, k) {
			continue
		}
		pairs = append(pairs, [2][]byte{[]byte(k), append([]byte(nil), v...)})
	}
	m.mu.RUnlock()
	sort.Slice(pairs, func(i, j int) bool { return string(pairs[i][0]) < string(pairs[j][0]) })
	return pairs
}

func inRange(lo, hi []byte, key string) bool {
	if len(lo) > 0 && string(key) < string(lo) {
		return false
	}
	if len(hi) > 0 && string(key) >= string(hi) {
		return false
	}
	return true
}
