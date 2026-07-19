//go:build !unix

package lock

import "os"

// flock is a best-effort no-op on non-Unix platforms: it opens (and holds) the
// file but does not enforce mutual exclusion. Single-instance protection on
// those platforms is a follow-up.
func flock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}
