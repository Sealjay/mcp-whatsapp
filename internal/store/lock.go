package store

import (
	"errors"
	"os"
)

// ErrLocked is returned by TryLock when another process already holds the
// advisory lock on the store directory.
var ErrLocked = errors.New("another whatsapp-mcp instance is already running against this store directory")

// FileLock is a filesystem-level advisory lock that prevents two `serve`
// processes from racing on the same SQLite files. WhatsApp itself only
// allows one device connection per session so without this guard the
// second process would silently lose writes or kill the first connection.
//
// TryLock and Release are implemented per-OS in lock_unix.go (flock(2)) and
// lock_windows.go (LockFileEx), since neither primitive is portable.
type FileLock struct {
	path string
	f    *os.File
}
