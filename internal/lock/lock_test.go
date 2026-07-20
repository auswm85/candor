package lock

import (
	"errors"
	"testing"
)

func TestLock_ExclusiveAndReleasable(t *testing.T) {
	path := t.TempDir() + "/daemon.lock"

	l1, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// A second acquire on Unix must fail with ErrLocked. On non-unix the flock
	// is a no-op, so only assert the exclusivity contract where it's enforced.
	if l2, err := Acquire(path); err == nil {
		// Not enforced here — release both handles before skipping so the
		// temp-dir cleanup can delete the lock file (Windows can't remove a
		// file that's still open).
		_ = l2.Release()
		_ = l1.Release()
		t.Skip("advisory locking not enforced on this platform")
	} else if !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire error = %v, want ErrLocked", err)
	}

	// After release, it can be re-acquired.
	if err := l1.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = l2.Release()
}
