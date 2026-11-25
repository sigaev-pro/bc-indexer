//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package rocksdb

import (
	"errors"
	"os"
)

// lockStore refuses the open on a platform where the standard library has no
// flock call. Without the lock the open would not be exclusive between two
// opens in one process, and the rule is that a second writer gets an error.
func lockStore(dir string) (*os.File, error) {
	return nil, errors.New("rocksdb storage: the exclusive open is not supported on this platform")
}
