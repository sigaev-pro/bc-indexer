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
//   - The write-ahead log is always on, and every write is synced before it
//     returns. Callers cannot select a weaker mode.
//   - Applied records are never rolled back. The log recovery mode is set
//     to the one that refuses to open a store whose log is damaged anywhere
//     but at its end, instead of quietly dropping the records after the
//     damage. A record torn by a crash at the end of the log is dropped and
//     every record before it stays.
//   - Apply is one RocksDB WriteBatch: after a restart, either every
//     operation of a batch is durable or none is. No implementation may
//     split one batch.
//   - Writes are serialized inside the store, and the open is exclusive
//     through a lock this package takes before the engine starts. A second
//     writer, in this process or another and under any spelling of the
//     path, gets ErrLocked and leaves the directory as it was.
//   - A directory that is not a compatible store is refused before this
//     process writes anything to it, and it is never reinitialized. That
//     covers a non-empty directory without our marker (ErrForeign) and a
//     marker of another store format, store or engine (ErrIncompatible).
//   - Every write, sync and iterator error is returned. The engine's repair
//     that drops records is never used: a store the engine cannot open
//     stays an explicit error (ErrOpen).
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

// syncMode selects when a write reaches the disk before it returns.
type syncMode int

const (
	// syncAlways syncs the write-ahead log before Put, Delete and Apply
	// return. A write that returned survives a process crash and a power
	// loss.
	syncAlways syncMode = iota
	// syncNever skips that sync. A returned write survives a process crash,
	// because the operating system holds it, but not a power loss or an
	// operating system crash. Tests use it where the sync is not what is
	// under test. It is not reachable from outside this package: the seam
	// contract promises a durable write, and a caller must not be able to
	// build a store that breaks it.
	syncNever
)

// Options configures Open. The zero value is the only value a caller can
// write, and it is the durable one: every write is synced before it returns.
// Options cannot weaken the startup rules; the exclusive open, the log
// recovery mode, the compression and the refusal of a foreign or
// incompatible directory are not configurable.
type Options struct {
	// sync has no exported field on purpose. See syncMode.
	sync syncMode
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
	// ErrLocked: another process or store object has the store open.
	ErrLocked = errors.New("rocksdb storage: directory is locked by another writer")
	// ErrOpen: the directory is a store of this format, but the engine could
	// not open it: a damaged log that is not a torn tail, a damaged
	// database, a missing permission or a disk error. Nothing was
	// reinitialized and no record was dropped. The engine may have appended
	// to its own informational log while it failed.
	ErrOpen = errors.New("rocksdb storage: the engine could not open the store")
)

// ErrClosed is returned by every operation after Close.
var ErrClosed = errors.New("rocksdb storage: store is closed")

const (
	// formatFile marks a directory as an extractor journal store and records
	// the store format. Open writes it before the engine creates anything,
	// so a directory without it is never a completed store.
	formatFile = "STORE_FORMAT"
	// formatTempFile is the marker being written. Open renames it over
	// formatFile, so the marker itself is always whole or absent.
	formatTempFile = "STORE_FORMAT.tmp"
	// lockFile carries the exclusive lock of the writer. It is not the
	// engine's LOCK file: the engine's lock is released for the whole
	// process when any descriptor of that file closes, so a refused open
	// would release the lock of the writer that holds it.
	lockFile = "STORE_LOCK"
	// formatMagic is the first line of the marker.
	formatMagic = "bc-indexer journal store"
	// formatVersion is the layout version of the store directory. A build
	// refuses a store of another version instead of opening it; there is no
	// automatic migration.
	formatVersion = 1
	// engineName is recorded in the marker. The marker does not repeat the
	// engine's version: the binding has no call that reports the linked one,
	// and the engine writes it itself into every store (the first line of
	// LOG and rocksdb_version in the OPTIONS file).
	engineName = "rocksdb"

	// currentFile is the engine's own marker of a created database. It is
	// written as the last step of creating a database, and no write is
	// accepted before it exists.
	currentFile = "CURRENT"
)

// betweenOps is a hook for tests. Apply calls it after each buffered
// operation is added to the engine batch, with the number of operations
// added so far. Production code leaves it nil. The crash tests use it to
// make a batch last long enough for a kill to land inside it.
var betweenOps func(done int)

// testWriteBufferBytes is a hook for tests. When it is not zero, Open gives
// the engine a write buffer of that size instead of the engine's default.
// The crash tests use a small one, so that a flush of the buffer, and after
// four flushes a merge of table files, start within a second. Production
// code leaves it at zero; TestEngineSettingsMatchTheBaseline checks that
// the default is what a store gets.
var testWriteBufferBytes uint64

// Open opens the journal store in dir, or creates it when dir is missing or
// empty. The same directory holds one store: opening it again after Close
// returns the durable state, and opening it while another store object or
// process holds it returns an error that wraps ErrLocked. Every spelling of
// the path is the same directory: relative or absolute, with a trailing or a
// doubled separator, or through a symbolic link.
//
// The directory must be the one the configuration names for the role
// (storage.live_dir for the producer, storage.gateway_dir for the Gateway).
// Open uses the path as given and does not compare it with other
// directories; resolving a relative path against the configuration file and
// keeping the store out of the native node's data directory are the
// caller's checks (LIVE_CONFIG_SPEC, "State and startup" and D78).
//
// A directory that is not a usable store is refused and never reinitialized:
//   - not empty and without our marker: ErrForeign;
//   - a marker of another store format, store or engine: ErrIncompatible;
//   - held by another writer: ErrLocked;
//   - a store of this format that the engine cannot open: ErrOpen.
//
// Open also returns a plain error, with no sentinel of this package, for an
// empty path, a path that cannot be resolved or read, and a directory, a
// lock file or a marker that cannot be created.
//
// The returned value is a storage.Storage; Close it through the Close
// function of this package.
func Open(dir string, opts Options) (storage.Storage, error) {
	if dir == "" {
		return nil, errors.New("rocksdb storage: directory path is empty")
	}
	// One spelling per directory. Symbolic links are resolved below, once
	// the directory exists.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("rocksdb storage: directory path %s: %w", dir, err)
	}

	// Nothing is written before this returns: no lock file, no marker.
	fresh, err := classify(abs)
	if err != nil {
		return nil, err
	}
	if fresh {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("rocksdb storage: create %s: %w", abs, err)
		}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("rocksdb storage: resolve %s: %w", abs, err)
	}

	// The lock comes before the engine. An open that is refused here has
	// touched nothing: the engine renames the store's LOG file before it
	// takes its own lock, so a refusal from the engine would not be free of
	// side effects.
	lock, err := lockStore(real)
	if err != nil {
		return nil, err
	}
	db, engineOpts, err := openLocked(real)
	if err != nil {
		lock.Close()
		return nil, err
	}

	// Created here, destroyed in Close: the engine keeps no copy.
	readOpts := grocksdb.NewDefaultReadOptions()
	readOpts.SetVerifyChecksums(true)
	writeOpts := grocksdb.NewDefaultWriteOptions()
	writeOpts.DisableWAL(false)
	writeOpts.SetSync(opts.sync == syncAlways)
	s := &store{dir: real, db: db, opts: engineOpts, ro: readOpts, wo: writeOpts, lock: lock}
	s.idle.L = &s.mu
	return s, nil
}

// openLocked creates or reopens the store. The caller holds the lock.
func openLocked(dir string) (*grocksdb.DB, *grocksdb.Options, error) {
	// Look again now that the lock is held. Another writer may have created
	// the store, or died in the middle of creating it, between the first
	// look and the lock.
	create, err := classify(dir)
	if err != nil {
		return nil, nil, err
	}
	if create {
		if err := writeFormat(dir); err != nil {
			return nil, nil, err
		}
	}

	engineOpts := grocksdb.NewDefaultOptions()
	// Only a directory without a database is created. An existing store is
	// reopened, never replaced: with create-if-missing set, the engine would
	// build a fresh empty database over a store whose CURRENT file went
	// missing, and delete its first manifest on the way.
	engineOpts.SetCreateIfMissing(create)
	engineOpts.SetErrorIfExists(false)
	// A checksum mismatch is an error, never a warning.
	engineOpts.SetParanoidChecks(true)
	// A record that was applied is never rolled back. The engine's default,
	// point-in-time recovery, stops at the first damage in the log and opens
	// the store without the records after it. This mode keeps the one
	// tolerance a crash needs, an incomplete last record, and refuses
	// everything else (include/rocksdb/options.h, WALRecoveryMode).
	engineOpts.SetWALRecoveryMode(grocksdb.TolerateCorruptedTailRecordsRecovery)
	// LZ4 is the accepted baseline for every level, the bottommost one
	// included (research/ROCKSDB_STACK). The bottommost options apply only
	// when they are enabled, so that is a third setting.
	engineOpts.SetCompression(grocksdb.LZ4Compression)
	engineOpts.SetBottommostCompression(grocksdb.LZ4Compression)
	engineOpts.SetBottommostCompressionOptions(grocksdb.NewDefaultCompressionOptions(), true)
	if testWriteBufferBytes > 0 {
		engineOpts.SetWriteBufferSize(testWriteBufferBytes)
	}
	// The write-ahead log stays in dir and is never disabled. The binding
	// has no call that turns it off at the database level.
	db, err := grocksdb.OpenDb(engineOpts, dir)
	if err != nil {
		engineOpts.Destroy()
		return nil, nil, openError(dir, err)
	}
	return db, engineOpts, nil
}

// Close closes a store that Open returned. It is for callers that hold the
// store as a storage.Storage, which has no Close of its own. Close waits for
// the operations in flight and then releases the lock. It always returns
// nil: the binding does not report the engine's close status. A store of
// another kind is left alone.
func Close(st storage.Storage) error {
	closer, ok := st.(io.Closer)
	if !ok {
		return nil
	}
	return closer.Close()
}

// classify looks at dir without changing it and reports whether Open has to
// create the store there. It says create only for a directory that is safe
// to create in: missing, empty, or holding nothing but the first steps of a
// creation that was interrupted.
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
		// No marker was ever completed here. The directory can only be ours
		// if the lock was just taken and the marker was being written.
		for _, entry := range entries {
			if !isOwnStep(entry) {
				return false, fmt.Errorf("%w: %s is not empty and holds no %s file", ErrForeign, dir, formatFile)
			}
		}
		return true, nil
	}
	if version != formatVersion {
		return false, fmt.Errorf("%w: %s has store format %d and this build reads format %d; "+
			"there is no automatic migration", ErrIncompatible, dir, version, formatVersion)
	}

	// The marker is ours, so everything in the directory came from this
	// adapter and its engine. Without the engine's CURRENT file no database
	// was ever completed here, which is normal only while a creation is
	// under way or was interrupted.
	if hasEntry(entries, currentFile) {
		return false, nil
	}
	for _, entry := range entries {
		if !isCreationLeftover(dir, entry) {
			return false, fmt.Errorf("%w: %s holds database files without %s; "+
				"the first creation was interrupted or the store is damaged", ErrIncompatible, dir, currentFile)
		}
	}
	return true, nil
}

func hasEntry(entries []os.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

// isOwnStep reports a file that Open itself writes before the marker is
// complete: the lock file and the marker being written.
func isOwnStep(entry os.DirEntry) bool {
	name := entry.Name()
	return name == lockFile || strings.HasPrefix(name, formatTempFile)
}

// isCreationLeftover reports whether an entry may exist in a directory that
// has our marker and no database yet. Only files that cannot hold a record
// qualify.
func isCreationLeftover(dir string, entry os.DirEntry) bool {
	name := entry.Name()
	switch {
	case isOwnStep(entry), name == formatFile,
		name == "LOCK", name == "IDENTITY", name == "LOG",
		strings.HasPrefix(name, "LOG.old."), strings.HasPrefix(name, "OPTIONS-"):
		return true
	case name == "archive":
		// The engine keeps its old informational logs here. It moves one
		// there before it takes its lock, so an open that was refused or
		// interrupted can leave one in a directory that has no database.
		// They hold no data; anything else in there does.
		children, err := os.ReadDir(filepath.Join(dir, name))
		if err != nil {
			return false
		}
		for _, child := range children {
			if !child.Type().IsRegular() || !strings.HasPrefix(child.Name(), "LOG.old.") {
				return false
			}
		}
		return true
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
// it, rename it over the target, then sync the directory. A failure removes
// the temporary file, so it does not stay behind as a leftover of this
// attempt. (A crash cannot remove it; classify treats it as a step of an
// interrupted creation.)
func writeFormat(dir string) error {
	tmp := filepath.Join(dir, formatTempFile)
	fail := func(err error) error {
		os.Remove(tmp)
		return err
	}
	text := fmt.Sprintf("%s\nformat_version=%d\nengine=%s\n", formatMagic, formatVersion, engineName)
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fail(fmt.Errorf("rocksdb storage: write %s: %w", tmp, err))
	}
	if err := syncFile(tmp); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, formatPath(dir)); err != nil {
		return fail(fmt.Errorf("rocksdb storage: publish %s: %w", formatPath(dir), err))
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
// The lock of this package normally answers first. This is the fallback for
// a writer that does not take it. grocksdb v1.10.2 turns every engine status
// into a plain error that carries the status text, so this reads the two
// texts the engine's POSIX file system produces (env/fs_posix.cc): one for a
// lock held elsewhere, and one for a lock held by this process.
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
	lock *os.File

	// writeMu serializes the writers and lets Close wait for the one in
	// flight.
	writeMu sync.Mutex

	// mu guards active, closed, db and lock.
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
//
// Close returns nil. The binding does not report the engine's close status,
// so a failure inside the engine cannot be told here.
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
	// The lock goes last, after the engine closed its files.
	s.lock.Close()
	s.lock = nil
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

// Put stores value under key and returns after the write is synced.
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

// Delete removes key and returns after the write is synced. Deleting a
// missing key is not an error.
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
	done := 0
	b.Visit(func(op storage.Op) bool {
		if op.Kind == storage.OpDelete {
			batch.Delete(op.Key)
		} else {
			batch.Put(op.Key, op.Value)
		}
		done++
		if betweenOps != nil {
			betweenOps(done)
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
