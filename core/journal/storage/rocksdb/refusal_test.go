package rocksdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/linxGnu/grocksdb"
)

// LIVE_CONFIG_SPEC ("State and startup" step 3, "Validation and shutdown"):
// an incompatible WAL is neither deleted nor silently reinitialized, and
// DELIVERY_STATE_MACHINE ("Errors, ENOSPC, compatibility"): a store of
// another persistent format is refused before any write.

// goodMarker is the marker Open writes.
func goodMarker() string {
	return fmt.Sprintf("%s\nformat_version=%d\nengine=%s\n", formatMagic, formatVersion, engineName)
}

// place creates dir and puts files in it. A name that ends with a separator
// is a directory. The marker gets its real content and the marker being
// written a cut-off one; every other file gets placeholder bytes.
func place(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		data := []byte("left behind")
		switch name {
		case formatFile:
			data = []byte(goodMarker())
		case formatTempFile:
			data = []byte("bc-indexer journ")
		}
		writeFile(t, path, data)
	}
}

// withStore fills a store, closes it and returns its directory.
func withStore(t *testing.T, dir string) {
	t.Helper()
	st := openFast(t, dir)
	for i := 0; i < 20; i++ {
		if err := st.Put([]byte(fmt.Sprintf("batch/%d", i)), []byte("pending payload")); err != nil {
			t.Fatal(err)
		}
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
}

// Every refusal has no side effect: Open answers before the engine starts,
// and the directory holds exactly the same bytes after it.
func TestRefusedOpenLeavesDirectoryAsItWas(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(t *testing.T, dir string)
		want    error
	}{
		{"another program's file", func(t *testing.T, dir string) {
			place(t, dir, "nodekey")
		}, ErrForeign},
		{"a native data directory", func(t *testing.T, dir string) {
			place(t, dir, "chaindata/000001.sst", "geth/nodekey", "LOCK")
		}, ErrForeign},
		{"engine files without our marker", func(t *testing.T, dir string) {
			place(t, dir, "LOCK", "LOG")
		}, ErrForeign},
		{"an engine database that is not ours", func(t *testing.T, dir string) {
			// Another user of the engine: it has no marker, so it is not
			// ours, whatever else it looks like.
			opts := grocksdb.NewDefaultOptions()
			defer opts.Destroy()
			opts.SetCreateIfMissing(true)
			raw, err := grocksdb.OpenDb(opts, dir)
			if err != nil {
				t.Fatal(err)
			}
			wo := grocksdb.NewDefaultWriteOptions()
			defer wo.Destroy()
			if err := raw.Put(wo, []byte("foreign"), []byte("payload")); err != nil {
				t.Fatal(err)
			}
			raw.Close()
		}, ErrForeign},
		{"held by another store object", func(t *testing.T, dir string) {
			openTest(t, dir)
		}, ErrLocked},
		{"database files without CURRENT: a log", func(t *testing.T, dir string) {
			writeFormatForTest(t, dir)
			place(t, dir, "000003.log")
		}, ErrIncompatible},
		{"database files without CURRENT: a manifest", func(t *testing.T, dir string) {
			writeFormatForTest(t, dir)
			place(t, dir, "MANIFEST-000001")
		}, ErrIncompatible},
		{"database files without CURRENT: a table file", func(t *testing.T, dir string) {
			writeFormatForTest(t, dir)
			place(t, dir, "000006.sst")
		}, ErrIncompatible},
		{"an archive that holds more than logs", func(t *testing.T, dir string) {
			writeFormatForTest(t, dir)
			place(t, dir, "archive/000004.log")
		}, ErrIncompatible},
	}
	for _, marker := range []struct{ name, text string }{
		{"a newer store format", strings.Replace(goodMarker(), "format_version=1", "format_version=2", 1)},
		{"no format version", formatMagic + "\nengine=rocksdb\n"},
		{"another engine", strings.Replace(goodMarker(), "engine=rocksdb", "engine=other", 1)},
		{"another store", "some other program\nformat_version=1\nengine=rocksdb\n"},
		{"garbage", "\x00\x01\x02not a marker at all\n"},
		{"a truncated first line", "bc-indexer journal stor"},
	} {
		marker := marker
		cases = append(cases, struct {
			name    string
			arrange func(t *testing.T, dir string)
			want    error
		}{"a marker that is " + marker.name, func(t *testing.T, dir string) {
			// A real store with a payload, so that a refusal that replaced
			// it would be visible.
			withStore(t, dir)
			writeFile(t, formatPath(dir), []byte(marker.text))
		}, ErrIncompatible})
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			c.arrange(t, dir)
			before := snapshotDir(t, dir, nil)

			st, err := Open(dir, Options{})
			if st != nil {
				Close(st)
				t.Fatalf("Open returned a store for %s", c.name)
			}
			wantErr(t, err, c.want, c.name)

			after := snapshotDir(t, dir, nil)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("the refused open changed the directory:\n before %v\n after  %v",
					sortedKeys(before), sortedKeys(after))
			}
		})
	}
}

func writeFormatForTest(t *testing.T, dir string) {
	t.Helper()
	if err := writeFormat(dir); err != nil {
		t.Fatal(err)
	}
}

// A store of this format that the engine cannot open is an error, not a
// fresh empty store. ErrOpen tells it apart from an incompatible format:
// the store is ours, so a caller maps it to a disk error.
func TestDamagedDatabaseIsRefusedNotReinitialized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	withStore(t, dir)

	// Point the database at a manifest that does not exist.
	writeFile(t, filepath.Join(dir, currentFile), []byte("MANIFEST-999999\n"))
	before := snapshotDir(t, dir, notInfoLog)

	st, err := Open(dir, Options{})
	if st != nil {
		Close(st)
		t.Fatal("Open returned a store for a damaged database")
	}
	wantErr(t, err, ErrOpen, "open of a damaged database")
	if isAnyOf(err, ErrIncompatible, ErrForeign, ErrLocked) {
		t.Fatalf("a damaged database of this format must not report a foreign, incompatible or locked store: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("the error does not name the directory: %v", err)
	}

	// The engine was started on our own store, so it may have written to
	// its informational log. Everything else holds the same bytes: nothing
	// was repaired or replaced.
	after := snapshotDir(t, dir, notInfoLog)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused open changed the database files:\n before %v\n after  %v",
			sortedKeys(before), sortedKeys(after))
	}
}

func isAnyOf(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// Open creates a store in steps: the lock file, the marker, then the engine's
// database. A crash can stop it between any two. These are the states that
// leaves, and what the next Open does with them.
func TestInterruptedFirstCreation(t *testing.T) {
	resumes := []struct {
		name  string
		files []string
	}{
		{"after the lock file", []string{lockFile}},
		{"while the marker is written", []string{lockFile, formatTempFile}},
		{"after the marker", []string{lockFile, formatFile}},
		{"after the engine's lock and log", []string{lockFile, formatFile, "LOCK", "LOG"}},
		{"after the engine rolled its log once", []string{lockFile, formatFile, "LOCK", "LOG", "archive/LOG.old.1730000000000000"}},
		{"after the engine wrote its identity and options", []string{lockFile, formatFile, "LOCK", "LOG", "LOG.old.1720000000000000",
			"archive/LOG.old.1730000000000000", "archive/LOG.old.1730000000000001", "IDENTITY", "OPTIONS-000003"}},
	}
	for _, c := range resumes {
		t.Run("resumes "+c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			place(t, dir, c.files...)

			st := openTest(t, dir)
			if _, err := os.Stat(filepath.Join(dir, currentFile)); err != nil {
				t.Fatalf("the database was not created: %v", err)
			}
			if err := st.Put([]byte("k"), []byte("v")); err != nil {
				t.Fatal(err)
			}
			if got := mustGet(t, st, "k"); got != "v" {
				t.Fatalf("got %q", got)
			}
		})
	}

	// Database files without CURRENT are ambiguous: the engine was
	// interrupted between creating its first manifest and writing CURRENT,
	// or somebody removed CURRENT from a real store. Neither is written
	// over. No write was ever accepted in such a directory.
	refuses := []struct {
		name  string
		files []string
	}{
		{"a manifest", []string{lockFile, formatFile, "MANIFEST-000001"}},
		{"a log", []string{lockFile, formatFile, "LOCK", "LOG", "000004.log"}},
		{"a table file", []string{lockFile, formatFile, "000006.sst"}},
		{"an archived write-ahead log", []string{lockFile, formatFile, "archive/000004.log"}},
		{"a subdirectory in the archive", []string{lockFile, formatFile, "archive/LOG.old.1730000000000000", "archive/old/"}},
		{"a directory under the name of a log", []string{lockFile, formatFile, "archive/LOG.old.1730000000000000/"}},
		{"a directory that is not the archive", []string{lockFile, formatFile, "old/LOG.old.1730000000000000"}},
	}
	for _, c := range refuses {
		t.Run("refuses "+c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			place(t, dir, c.files...)
			before := snapshotDir(t, dir, nil)

			st, err := Open(dir, Options{})
			if st != nil {
				Close(st)
				t.Fatalf("Open returned a store with %s and no %s", c.name, currentFile)
			}
			wantErr(t, err, ErrIncompatible, "open over database files without "+currentFile)

			after := snapshotDir(t, dir, nil)
			if !reflect.DeepEqual(after, before) {
				t.Fatal("the refused open changed the directory")
			}
		})
	}
}

// Open passes the engine create-if-missing only for a directory without a
// database. With it set for an existing store too, the engine would build a
// fresh empty database over a store whose CURRENT file it cannot read, and
// delete the store's first manifest on the way (db_impl_open.cc, NewDB).
//
// The test stands in for CURRENT going missing between the look at the
// directory and the engine's open: the name is there for Open's look, and
// the engine cannot read it.
func TestNeverRecreatesOverAnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	withStore(t, dir)
	current := filepath.Join(dir, currentFile)
	original := readFile(t, current)
	// Everything but CURRENT and the engine's log. CURRENT is the file this
	// test replaces, so it is checked on its own below.
	dataFiles := snapshotDir(t, dir, func(rel string) bool { return notInfoLog(rel) || rel == currentFile })

	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("no-such-manifest", current); err != nil {
		t.Fatal(err)
	}

	st, err := Open(dir, Options{})
	if st != nil {
		Close(st)
		t.Fatal("Open returned a store over a database whose CURRENT file cannot be read")
	}
	wantErr(t, err, ErrOpen, "open of a database without a readable CURRENT file")
	if !reflect.DeepEqual(snapshotDir(t, dir, func(rel string) bool { return notInfoLog(rel) || rel == currentFile }), dataFiles) {
		t.Fatal("the refused open changed the database files")
	}
	// A recreated database would have a new CURRENT of its own.
	if target, err := os.Readlink(current); err != nil || target != "no-such-manifest" {
		t.Fatalf("CURRENT is not the link the test placed: target %q, err %v", target, err)
	}

	// With CURRENT back, the store opens with everything it held.
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	writeFile(t, current, original)
	reopened := openTest(t, dir)
	for i := 0; i < 20; i++ {
		if got := mustGet(t, reopened, fmt.Sprintf("batch/%d", i)); got != "pending payload" {
			t.Fatalf("batch/%d = %q after the refused open", i, got)
		}
	}
}
