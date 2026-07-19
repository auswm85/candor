//go:build unix

package lock

import (
	"os"
	"syscall"
)

// flock opens the file and takes a non-blocking exclusive advisory lock. The
// OS releases it automatically when the file descriptor closes (including on
// process exit), so a crash never leaves a stale lock.
func flock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600) // fix perms on a pre-existing file
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errIsWouldBlock(err) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}

func errIsWouldBlock(err error) bool {
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}
