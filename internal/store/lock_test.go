package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLockExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := TryLock(dir)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	defer first.Release()

	if _, err := TryLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock: got %v, want ErrLocked", err)
	}

	first.Release()

	second, err := TryLock(dir)
	if err != nil {
		t.Fatalf("TryLock after release: %v", err)
	}
	second.Release()
}

func TestTryLockCreatesStoreDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")

	lock, err := TryLock(dir)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	lock.Release()
}

func TestFileLockReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	lock, err := TryLock(dir)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	lock.Release()
	lock.Release() // must not panic

	var nilLock *FileLock
	nilLock.Release() // must not panic
}
