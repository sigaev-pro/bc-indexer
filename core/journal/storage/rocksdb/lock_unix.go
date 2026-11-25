//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package rocksdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockStore takes the exclusive lock of the writer in dir and creates the
// lock file when the directory does not have one yet. The caller keeps the
// returned file open for as long as the store is open: closing it releases
// the lock, and so does the death of the process.
//
// This is an flock on a file of this package. It has the two properties the
// engine's own lock lacks for this purpose. It conflicts between two opens
// in one process, whatever spelling of the path opened the file, because it
// belongs to the open file and not to the process. And closing one open
// releases only that open, so a refused open cannot release the lock of the
// writer that holds it.
//
// The lock file is not the engine's LOCK file. The engine takes an fcntl
// lock there, and an fcntl lock is released for the whole process when any
// descriptor of the file closes in that process (env/fs_posix.cc).
func lockStore(dir string) (*os.File, error) {
	path := filepath.Join(dir, lockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("rocksdb storage: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, fmt.Errorf("rocksdb storage: lock %s: %w", path, err)
	}
	return f, nil
}
