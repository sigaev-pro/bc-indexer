// Package rocksdb is the durable implementation of the journal storage seam
// (core/journal/storage) on the engine that decision D33 accepted: RocksDB
// 10.4.2 through the pinned Go binding github.com/linxGnu/grocksdb v1.10.2
// (COMPONENT_STACK, research/ROCKSDB_STACK).
//
// It is its own Go module because it needs CGO and a native librocksdb. The
// seam, core/journal/producer and gateway/journal build without it.
// build-librocksdb.sh builds the pinned library; README.md lists the steps.
//
// What this implementation holds:
//
//   - The write-ahead log is always on. With SyncAlways (the default) Put,
//     Delete and Apply return only after RocksDB reports the write synced.
//   - Apply is one RocksDB WriteBatch: after a restart, either every
//     operation of a batch is durable or none is. No implementation may
//     split one batch.
//   - Writes are serialized inside the store, and the engine's directory
//     lock makes the open exclusive: a second writer gets ErrLocked.
//   - A directory that is not a compatible store is refused before this
//     process writes anything to it, and it is never reinitialized. That
//     covers a non-empty directory without our marker (ErrForeign) and a
//     marker of another store format, store or engine (ErrIncompatible).
//   - Every write, sync and iterator error is returned. The engine's repair
//     that drops records is never used: a store the engine cannot open stays
//     an explicit error (ErrOpen).
//   - Keys are opaque bytes in unsigned byte order. Iterate walks a snapshot
//     of [lo, hi) in ascending order, exactly like the memory store, and
//     hands out copies.
package rocksdb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/linxGnu/grocksdb"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// SyncMode selects when a write reaches the disk before it returns.
type SyncMode int

const (
	// SyncAlways syncs the write-ahead log before Put, Delete and Apply
	// return. It is the zero value and the only mode with a durability
	// guarantee: a write that returned survives a process crash and a power
	// loss.
	SyncAlways SyncMode = iota
	// SyncNever skips that sync. A returned write still survives a process
	// crash, because the operating system holds it, but not a power loss or
	// an operating system crash. Use it for measurements and throwaway data
	// only.
	SyncNever
)

// String returns the mode name for logs and errors.
func (m SyncMode) String() string {
	switch m {
	case SyncAlways:
		return "SyncAlways"
	case SyncNever:
		return "SyncNever"
	default:
		return "SyncMode(" + strconv.Itoa(int(m)) + ")"
	}
}

// Options configures Open. The zero value is the durable default: every
// write is synced before it returns. Options cannot weaken the startup rules;
// the exclusive open and the refusal of a foreign or incompatible directory
// are not configurable.
type Options struct {
	// Sync selects when a write is synced. The zero value is SyncAlways.
	Sync SyncMode
}

// Errors that Open returns. Each one that comes from the engine keeps the
// engine's status in its chain.
var (
	// ErrForeign: the directory is not empty and is not an extractor journal
	// store. Nothing was written to it.
	ErrForeign = errors.New("rocksdb storage: directory is not an extractor journal store")
	// ErrIncompatible: the directory holds a store this build cannot read:
	// another store format, another store or engine, or database files
	// without the engine's marker of a completed database. Nothing was
	// written to it.
	ErrIncompatible = errors.New("rocksdb storage: directory holds an incompatible store")
	// ErrLocked: another process has the store open. The engine's directory
	// lock allows one writer at a time.
	ErrLocked = errors.New("rocksdb storage: directory is locked by another writer")
	// ErrOpen: the directory is a store of this format, but the engine could
	// not open it: a damaged database, a missing permission or a disk error.
	// Nothing was reinitialized. For a damaged database the engine may have
	// appended to its own informational log.
	ErrOpen = errors.New("rocksdb storage: the engine could not open the store")
)

// ErrClosed is returned by every operation after Close.
var ErrClosed = errors.New("rocksdb storage: store is closed")

const (
	// formatFile marks a directory as an extractor journal store and records
	// the store format. Open writes it before the engine creates anything,
	// so a directory without it is never ours.
	formatFile     = "STORE_FORMAT"
	formatTempFile = "STORE_FORMAT.tmp"
	formatMagic    = "bc-indexer journal store"
	// formatVersion is the layout version of the store directory. A build
	// refuses a store of another version instead of opening it; there is no
	// automatic migration.
	formatVersion = 1
	// engineName and engineVersion are recorded in the marker. The engine
	// version is information for operators: engine file formats are
	// compatible across engine versions, so a different number is not a
	// refusal.
	engineName    = "rocksdb"
	engineVersion = "10.4.2"

	// currentFile is the engine's own marker of a created database. It is
	// written as the last step of creating a database, and no write is
	// accepted before it exists.
	currentFile = "CURRENT"
)

// Open opens the journal store in dir, or creates it when dir is missing or
// empty. The same directory holds one store: opening it again after Close
// returns the durable state, and opening it from a second process or store
// object at the same time returns an error that wraps ErrLocked.
//
// The directory must be the one the configuration names for the role
// (storage.live_dir for the producer, storage.gateway_dir for the Gateway).
// LIVE_CONFIG_SPEC, "State and startup": the directories of the native node,
// the producer and the Gateway must not be the same or nested. Open uses the
// path as given; resolving a relative path against the configuration file is
// the caller's job (LIVE_CONFIG_SPEC, D78).
//
// A directory that is not a usable store is refused and never reinitialized:
//   - not empty and without our marker: ErrForeign;
//   - a marker of another store format, store or engine: ErrIncompatible;
//   - open in another process or store object: ErrLocked;
//   - a store of this format that the engine cannot open: ErrOpen.
//
// The returned value is a storage.Storage; Close it through the Close
// function of this package.
func Open(dir string, opts Options) (storage.Storage, error) {
	if dir == "" {
		return nil, errors.New("rocksdb storage: directory path is empty")
	}
	if opts.Sync != SyncAlways && opts.Sync != SyncNever {
		return nil, fmt.Errorf("rocksdb storage: unknown sync mode %d", opts.Sync)
	}

	// Nothing is written before this returns.
	create, err := classify(dir)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("rocksdb storage: create %s: %w", dir, err)
		}
		if err := writeFormat(dir); err != nil {
			return nil, err
		}
	}

	engineOpts := grocksdb.NewDefaultOptions()
	// Only a directory without a database is created. An existing store is
	// reopened, never replaced.
	engineOpts.SetCreateIfMissing(create)
	engineOpts.SetErrorIfExists(false)
	// A checksum mismatch is an error, never a warning.
	engineOpts.SetParanoidChecks(true)
	// The write-ahead log stays in dir and is never disabled. The binding
	// has no call that turns it off at the database level.
	db, err := grocksdb.OpenDb(engineOpts, dir)
	if err != nil {
		engineOpts.Destroy()
		return nil, openError(dir, err)
	}

	// Created here, destroyed in Close: the engine keeps no copy.
	readOpts := grocksdb.NewDefaultReadOptions()
	readOpts.SetVerifyChecksums(true)
	writeOpts := grocksdb.NewDefaultWriteOptions()
	writeOpts.DisableWAL(false)
	writeOpts.SetSync(opts.Sync == SyncAlways)
	s := &store{dir: dir, db: db, opts: engineOpts, ro: readOpts, wo: writeOpts}
	s.idle.L = &s.mu
	return s, nil
}

// Close closes a store that Open returned. It is for callers that hold the
// store as a storage.Storage, which has no Close of its own. Close waits for
// the operations in flight and then releases the directory lock. A store of
// another kind is left alone and nil is returned.
func Close(st storage.Storage) error {
	closer, ok := st.(io.Closer)
	if !ok {
		return nil
	}
	return closer.Close()
}

// classify looks at dir without changing it and reports whether Open has to
// create the store there.
func classify(dir string) (bool, error) {
	// Stat follows a symbolic link: a link to the store directory is the
	// store directory (a mounted volume reached through a link, for example).
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("rocksdb storage: inspect %s: %w", dir, err)
	case !info.IsDir():
		return false, fmt.Errorf("%w: %s is not a directory", ErrForeign, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("rocksdb storage: read %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return true, nil
	}

	version, present, err := readFormat(dir)
	if err != nil {
		return false, err
	}
	if !present {
		return false, fmt.Errorf("%w: %s is not empty and holds no %s file", ErrForeign, dir, formatFile)
	}
	if version != formatVersion {
		return false, fmt.Errorf("%w: %s has store format %d and this build reads format %d; "+
			"there is no automatic migration", ErrIncompatible, dir, version, formatVersion)
	}

	// The marker is ours, so everything in the directory came from this
	// adapter. Without the engine's CURRENT file no database was ever
	// completed here, which is normal only when the first creation was
	// interrupted.
	if !hasEntry(entries, currentFile) {
		for _, entry := range entries {
			if !isCreationLeftover(dir, entry) {
				return false, fmt.Errorf("%w: %s holds database files without %s; "+
					"the first creation was interrupted or the store is damaged", ErrIncompatible, dir, currentFile)
			}
		}
		return true, nil
	}
	return false, nil
}

func hasEntry(entries []os.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

// isCreationLeftover reports whether a file may exist in a marker directory
// that has no database yet: files the engine writes before it creates the
// database, and the marker itself. Anything that can hold data does not
// qualify.
func isCreationLeftover(dir string, entry os.DirEntry) bool {
	name := entry.Name()
	switch {
	case name == formatFile, name == formatTempFile,
		name == "LOCK", name == "IDENTITY", name == "LOG",
		strings.HasPrefix(name, "LOG.old."), strings.HasPrefix(name, "OPTIONS-"):
		return true
	case name == "archive":
		// The engine creates an empty subdirectory for archived logs.
		children, err := os.ReadDir(filepath.Join(dir, name))
		return err == nil && len(children) == 0
	default:
		return false
	}
}

func formatPath(dir string) string { return filepath.Join(dir, formatFile) }

// readFormat reads the store marker. present is false when the directory has
// no marker. An error means the marker is not one this build can trust.
func readFormat(dir string) (version int, present bool, err error) {
	data, err := os.ReadFile(formatPath(dir))
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("rocksdb storage: read %s: %w", formatPath(dir), err)
	}
	unreadable := func(reason string) (int, bool, error) {
		return 0, true, fmt.Errorf("%w: %s is unreadable (%s)", ErrIncompatible, formatPath(dir), reason)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != formatMagic {
		return unreadable("unknown first line")
	}
	fields := make(map[string]string, len(lines))
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return unreadable("line " + strconv.Quote(line))
		}
		fields[key] = value
	}
	version, err = strconv.Atoi(fields["format_version"])
	if err != nil || version < 1 {
		return unreadable("format_version " + strconv.Quote(fields["format_version"]))
	}
	if engine := fields["engine"]; engine != engineName {
		return unreadable("engine " + strconv.Quote(engine))
	}
	return version, true, nil
}

// writeFormat writes the store marker durably: write a temporary file, sync
// it, rename it over the target, then sync the directory.
func writeFormat(dir string) error {
	text := fmt.Sprintf("%s\nformat_version=%d\nengine=%s\nengine_version=%s\n",
		formatMagic, formatVersion, engineName, engineVersion)
	tmp := filepath.Join(dir, formatTempFile)
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("rocksdb storage: write %s: %w", tmp, err)
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, formatPath(dir)); err != nil {
		return fmt.Errorf("rocksdb storage: publish %s: %w", formatPath(dir), err)
	}
	return syncDir(dir)
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("rocksdb storage: open %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("rocksdb storage: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("rocksdb storage: close %s: %w", path, err)
	}
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("rocksdb storage: open %s: %w", dir, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("rocksdb storage: sync %s: %w", dir, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("rocksdb storage: close %s: %w", dir, err)
	}
	return nil
}

// openError turns an engine error from Open into one of this package's
// errors, keeping the engine status in the chain.
func openError(dir string, err error) error {
	if isLocked(err) {
		return fmt.Errorf("%w: %s: %w", ErrLocked, dir, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrOpen, dir, err)
}

// isLocked reports the engine's status for a directory that is already held.
// grocksdb v1.10.2 turns every engine status into a plain error that carries
// the status text, so this reads the two texts the engine's POSIX file system
// produces (env/fs_posix.cc): one for a lock held elsewhere, and one for a
// lock held by this process.
func isLocked(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "While lock file") || strings.Contains(msg, "lock hold by current process")
}

// store is one open journal store.
type store struct {
	dir  string
	db   *grocksdb.DB
	opts *grocksdb.Options
	ro   *grocksdb.ReadOptions
	wo   *grocksdb.WriteOptions

	// writeMu serializes the writers and lets Close wait for the one in
	// flight.
	writeMu sync.Mutex

	// mu guards active, closed and db.
	mu     sync.Mutex
	active int
	closed bool
	// idle signals that the last operation left after Close was called.
	// Every waiter is woken: Close may have been called more than once.
	idle sync.Cond
}

// enter registers one operation and returns the open database. It fails once
// Close was called. The database stays open until the last operation leaves.
func (s *store) enter() (*grocksdb.DB, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	s.active++
	return s.db, true
}

func (s *store) leave() {
	s.mu.Lock()
	s.active--
	if s.active == 0 {
		s.idle.Broadcast()
	}
	s.mu.Unlock()
}

// Close releases the store. Operations in flight finish first; after Close
// every operation returns ErrClosed. Close waits for them, so it must not be
// called from inside an Iterate visit function.
func (s *store) Close() error {
	// Take the writer's place so that the writer in flight finishes and no
	// new one starts, then mark the store closed. writeMu is released at
	// once: an operation that is still running may try to write, and that
	// write must fail fast with ErrClosed instead of waiting for Close.
	s.writeMu.Lock()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.writeMu.Unlock()

	// A reader may still be inside a visit function. The database closes
	// after the last operation has left.
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.active > 0 {
		s.idle.Wait()
	}
	if s.db == nil {
		return nil // already closed
	}
	s.db.Close()
	s.db = nil
	s.ro.Destroy()
	s.wo.Destroy()
	s.opts.Destroy()
	return nil
}

// Get returns a copy of the value for key, or storage.ErrNotFound.
func (s *store) Get(key []byte) ([]byte, error) {
	db, ok := s.enter()
	if !ok {
		return nil, ErrClosed
	}
	defer s.leave()
	slice, err := db.Get(s.ro, key)
	if err != nil {
		return nil, fmt.Errorf("rocksdb storage: get in %s: %w", s.dir, err)
	}
	// The engine reports a missing key as a null value without an error
	// (db/c.cc, rocksdb_get). An empty value comes back as an existing,
	// zero-length slice.
	if slice == nil || !slice.Exists() {
		return nil, storage.ErrNotFound
	}
	defer slice.Free()
	return append([]byte(nil), slice.Data()...), nil
}

// Put stores value under key. With SyncAlways it returns after the write is
// synced.
func (s *store) Put(key, value []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	db, ok := s.enter()
	if !ok {
		return ErrClosed
	}
	defer s.leave()
	if err := db.Put(s.wo, key, value); err != nil {
		return fmt.Errorf("rocksdb storage: put in %s: %w", s.dir, err)
	}
	return nil
}

// Delete removes key. Deleting a missing key is not an error.
func (s *store) Delete(key []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	db, ok := s.enter()
	if !ok {
		return ErrClosed
	}
	defer s.leave()
	if err := db.Delete(s.wo, key); err != nil {
		return fmt.Errorf("rocksdb storage: delete in %s: %w", s.dir, err)
	}
	return nil
}

// Apply commits every buffered operation of b as one engine WriteBatch: after
// a restart either all of them are durable or none is. A batch over
// storage.MaxBatchBytes is rejected before the engine is touched.
func (s *store) Apply(b *storage.Batch) error {
	if b.Size() > storage.MaxBatchBytes {
		return storage.ErrBatchTooLarge
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	db, ok := s.enter()
	if !ok {
		return ErrClosed
	}
	defer s.leave()
	batch := grocksdb.NewWriteBatch()
	defer batch.Destroy()
	b.Visit(func(op storage.Op) bool {
		if op.Kind == storage.OpDelete {
			batch.Delete(op.Key)
		} else {
			batch.Put(op.Key, op.Value)
		}
		return true
	})
	if err := db.Write(s.wo, batch); err != nil {
		return fmt.Errorf("rocksdb storage: apply in %s: %w", s.dir, err)
	}
	return nil
}

// Iterate visits every key/value pair with lo <= key < hi in ascending byte
// order. An empty hi iterates to the end. The walk reads the snapshot the
// engine takes when the iterator is created, so writes during the walk do
// not disturb it. The key and value given to visit are copies. Returning
// storage.ErrStopIterate from visit stops the walk; any other error aborts
// it and is returned.
func (s *store) Iterate(lo, hi []byte, visit func(key, value []byte) error) error {
	db, ok := s.enter()
	if !ok {
		return ErrClosed
	}
	defer s.leave()
	ro := grocksdb.NewDefaultReadOptions()
	// The iterator keeps a reference to its read options, so the options
	// outlive it: Destroy calls run in reverse order.
	defer ro.Destroy()
	ro.SetVerifyChecksums(true)
	if len(lo) > 0 {
		ro.SetIterateLowerBound(lo)
	}
	if len(hi) > 0 {
		ro.SetIterateUpperBound(hi)
	}
	it := db.NewIterator(ro)
	defer it.Close()
	if len(lo) > 0 {
		it.Seek(lo)
	} else {
		it.SeekToFirst()
	}
	missingKey := false
	for ; it.Valid(); it.Next() {
		key, value := it.Key(), it.Value()
		if key == nil {
			// A valid position without a key cannot be reported. The check
			// below tells an engine failure from an impossible answer.
			missingKey = true
			break
		}
		// A position without a value buffer is an empty value.
		var data []byte
		if value != nil {
			data = value.Data()
		}
		err := visit(append([]byte(nil), key.Data()...), append([]byte(nil), data...))
		if err != nil {
			if err == storage.ErrStopIterate {
				return nil
			}
			return err
		}
	}
	// Valid is also false after a read error, so the iterator's error is the
	// only way to tell the end of the range from a failure. Ending the walk
	// quietly would look like a complete walk.
	if err := it.Err(); err != nil {
		return fmt.Errorf("rocksdb storage: iterate in %s: %w", s.dir, err)
	}
	if missingKey {
		return fmt.Errorf("rocksdb storage: iterate in %s: the engine returned no key for a valid position", s.dir)
	}
	return nil
}
