//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// TryLock acquires a non-blocking exclusive byte-range lock on
// <storeDir>/.lock via LockFileEx, the Windows analogue of flock(2). If the
// lock is held by another process, a wrapped ErrLocked is returned whose
// message includes the full path of the lock file and a hint for recovery
// when the lock is stale.
func TryLock(storeDir string) (*FileLock, error) {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	p := filepath.Join(storeDir, ".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY | windows.LOCKFILE_EXCLUSIVE_LOCK)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			// Wrap ErrLocked so existing errors.Is callers still match,
			// but enrich the message with the operator-recovery hint.
			return nil, fmt.Errorf("%w (if no other whatsapp-mcp is running, remove %s)", ErrLocked, p)
		}
		return nil, fmt.Errorf("lockfileex: %w", err)
	}
	return &FileLock{path: p, f: f}, nil
}

// Release drops the lock. Safe to call multiple times.
func (l *FileLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol)
	_ = l.f.Close()
	l.f = nil
}
