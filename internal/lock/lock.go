// Package lock provides a single-instance file lock so only one candor
// dashboard runs at a time. Acquiring is advisory and auto-released when the
// process exits, avoiding stale locks after a crash.
package lock

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked is returned when another process already holds the lock.
var ErrLocked = errors.New("already held by another process")

// Lock is a held file lock. Call Release when done.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive, non-blocking lock on path, creating it (and its
// parent directory) if needed. Returns ErrLocked if another process holds it.
func Acquire(path string) (*Lock, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// 0700 to match the store's DB dir — this dir also holds daemon.log and
		// the pricing cache, which shouldn't be world-readable on a shared host.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	f, err := flock(path)
	if err != nil {
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock and removes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	name := l.f.Name()
	err := l.f.Close() // closing releases the advisory lock
	_ = os.Remove(name)
	l.f = nil
	return err
}
