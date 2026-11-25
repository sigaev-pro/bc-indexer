package rocksdb

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// LIVE_CONFIG_SPEC, "State and startup" step 2: the owner state is opened
// exclusively and a second writer gets an error.
//
// The engine's own lock does not give that inside one process. It keys its
// in-process check on the path string, and it takes an fcntl lock, which
// cannot conflict inside a process (env/fs_posix.cc). So this package takes
// its own lock, on a file of its own, before the engine starts.

// spellings returns the other ways to name dir: by a trailing or a doubled
// separator, a dot segment, a relative path, a symbolic link, and the path
// with links resolved. On a case-insensitive file system it adds the name in
// another case.
func spellings(t *testing.T, dir string) []struct{ name, path string } {
	t.Helper()
	sep := string(os.PathSeparator)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	link := dir + "-link"
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []struct{ name, path string }{
		{"the same path", dir},
		{"a trailing separator", dir + sep},
		{"a doubled separator", filepath.Dir(dir) + sep + sep + filepath.Base(dir)},
		{"a dot segment", filepath.Dir(dir) + sep + "." + sep + filepath.Base(dir)},
		{"a relative path", relative},
		{"a symbolic link", link},
	}
	if resolved != dir {
		out = append(out, struct{ name, path string }{"the path with links resolved", resolved})
	}
	other := filepath.Join(filepath.Dir(dir), strings.ToUpper(filepath.Base(dir)))
	here, errHere := os.Stat(formatPath(dir))
	there, errThere := os.Stat(formatPath(other))
	if errHere == nil && errThere == nil && os.SameFile(here, there) {
		out = append(out, struct{ name, path string }{"the name in another case", other})
	}
	return out
}

func TestExclusiveOpenInOneProcessUnderEverySpelling(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "volume", "wal")
	first := openTest(t, dir)
	if err := first.Put([]byte("owner"), []byte("first")); err != nil {
		t.Fatal(err)
	}

	for _, alias := range spellings(t, dir) {
		second, err := Open(alias.path, Options{})
		if second != nil {
			Close(second)
		}
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("open through %s (%s): store opened=%v, err=%v; want ErrLocked",
				alias.name, alias.path, second != nil, err)
		}
	}

	// The refused opens left the owner working and the state intact.
	if err := first.Put([]byte("after"), []byte("refusal")); err != nil {
		t.Fatalf("owner write after refused opens: %v", err)
	}
	if got := mustGet(t, first, "owner"); got != "first" {
		t.Fatalf("owner state changed: %q", got)
	}

	// Close releases the lock: the store opens again under any spelling.
	if err := Close(first); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, openTest(t, dir+"-link"), "after"); got != "refusal" {
		t.Fatalf("state after the owner closed: %q", got)
	}
}

// A second process is refused while the first holds the store. The refusal
// comes from the lock of this package, before the engine starts, so not one
// byte of the directory changes. The engine's own lock would come too late
// for that: the engine renames the store's LOG file before it takes it.
func TestExclusiveOpenAcrossProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	h := startHelper(t, helperIdle, dir, true, 0)
	if line := h.next(60 * time.Second); line != "OPEN" {
		t.Fatalf("the helper reported %q, want OPEN", line)
	}

	before := snapshotDir(t, dir, nil)
	refused, err := Open(dir, Options{})
	if refused != nil {
		Close(refused)
		t.Fatal("Open returned a store while another process holds the directory")
	}
	wantErr(t, err, ErrLocked, "open while another process holds the store")
	if after := snapshotDir(t, dir, nil); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused open changed the directory:\n before %v\n after  %v",
			sortedKeys(before), sortedKeys(after))
	}

	// The death of the holder releases the lock.
	h.kill()
	if got := mustGet(t, openTest(t, dir), "owner"); got != "helper" {
		t.Fatalf("state after the holder died: %q", got)
	}
}

// Many opens at once on one directory: exactly one wins, also when the
// directory is new and the winner creates the store in it.
func TestConcurrentOpenAllowsExactlyOne(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		name := "existing store"
		if fresh {
			name = "new store"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			if !fresh {
				// A store that exists, and is closed: nobody holds it when
				// the concurrent opens start.
				st := openTest(t, dir)
				if err := st.Put([]byte("k"), []byte("v")); err != nil {
					t.Fatal(err)
				}
				if err := Close(st); err != nil {
					t.Fatal(err)
				}
			}
			const attempts = 16
			var wg sync.WaitGroup
			winners := make(chan storage.Storage, attempts)
			refusals := make(chan error, attempts)
			for i := 0; i < attempts; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					st, err := Open(dir, Options{})
					if err == nil {
						winners <- st
						return
					}
					refusals <- err
				}()
			}
			wg.Wait()
			close(winners)
			close(refusals)

			var winner storage.Storage
			won := 0
			for st := range winners {
				won++
				winner = st
			}
			if won != 1 {
				t.Fatalf("%d of %d concurrent opens won, want exactly 1", won, attempts)
			}
			for err := range refusals {
				if !errors.Is(err, ErrLocked) {
					t.Fatalf("a losing open returned %v, want ErrLocked", err)
				}
			}
			t.Cleanup(func() { Close(winner) })
			if err := winner.Put([]byte("k"), []byte("v")); err != nil {
				t.Fatalf("the winner cannot write: %v", err)
			}
		})
	}
}

// Whatever spelling Open gets, it opens the engine on one path: absolute,
// cleaned, with symbolic links resolved. The lock alone already refuses
// every alias, so this is the second line of defense: the engine's own
// in-process check compares path strings, and it agrees only when every
// spelling reaches it as the same string.
func TestOpensTheEngineOnOneSpellingOfThePath(t *testing.T) {
	linked := filepath.Join(t.TempDir(), "volume", "wal")
	// Created, and closed: nobody holds it while the spellings are opened.
	first := openTest(t, linked)
	if err := first.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := Close(first); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, linked)
	if err != nil {
		t.Fatal(err)
	}
	link := linked + "-link"
	if err := os.Symlink(linked, link); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathSeparator)
	for _, spelling := range []struct{ name, path string }{
		{"a trailing separator", linked + sep},
		{"a doubled separator", filepath.Dir(linked) + sep + sep + filepath.Base(linked)},
		{"a dot segment", filepath.Dir(linked) + sep + "." + sep + filepath.Base(linked)},
		{"a relative path", relative},
		{"a symbolic link", link},
	} {
		st, err := Open(spelling.path, Options{})
		if err != nil {
			t.Fatalf("open through %s: %v", spelling.name, err)
		}
		if got := st.(*store).dir; got != resolved {
			t.Errorf("opened through %s, the engine got %q, want %q", spelling.name, got, resolved)
		}
		if err := Close(st); err != nil {
			t.Fatal(err)
		}
	}
}
