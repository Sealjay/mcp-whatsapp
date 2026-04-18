package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked is returned by TryLock when another process already holds the
// advisory lock on the store directory.
var ErrLocked = errors.New("another whatsapp-mcp instance is already running against this store directory")

// FileLock is a filesystem-level advisory lock that prevents two `serve`
// processes from racing on the same SQLite files. WhatsApp itself only
// allows one device connection per session so without this guard the
// second process would silently lose writes or kill the first connection.
type FileLock struct {
	path string
	f    *os.File
}

// TryLock acquires a non-blocking exclusive flock(2) on <storeDir>/.lock.
// If the lock is held by another process, ErrLocked is returned.
func TryLock(storeDir string) (*FileLock, error) {
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	p := filepath.Join(storeDir, ".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &FileLock{path: p, f: f}, nil
}

// Release drops the lock. Safe to call multiple times.
func (l *FileLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
