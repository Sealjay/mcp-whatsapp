package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMediaPath_InsideRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(file)
	got, err := ValidateMediaPath(file, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantResolved {
		t.Fatalf("want %q, got %q", wantResolved, got)
	}
}

func TestValidateMediaPath_OutsideRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	file := filepath.Join(other, "secret.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := ValidateMediaPath(file, root)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "allowed root") {
		t.Fatalf("error should mention allowed root, got %v", err)
	}
}

func TestValidateMediaPath_Empty(t *testing.T) {
	got, err := ValidateMediaPath("", "/any/root")
	if err != nil {
		t.Fatalf("empty input should not error, got %v", err)
	}
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestValidateMediaPath_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	_, err := ValidateMediaPath(link, root)
	if err == nil {
		t.Fatal("expected symlink-escape to be rejected")
	}
}

func TestValidateMediaPath_SymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("y"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	got, err := ValidateMediaPath(link, root)
	if err != nil {
		t.Fatalf("symlink inside root should pass, got %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(real)
	if got != wantResolved {
		t.Fatalf("want resolved path %q, got %q", wantResolved, got)
	}
}

func TestValidateMediaPath_TraversalAttempt(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "x")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	attempt := filepath.Join(root, "..", filepath.Base(outside), "x")
	_, err := ValidateMediaPath(attempt, root)
	if err == nil {
		t.Fatal("expected traversal attempt to be rejected")
	}
}

func TestValidateMediaPath_MissingFile(t *testing.T) {
	root := t.TempDir()
	_, err := ValidateMediaPath(filepath.Join(root, "nope.txt"), root)
	if err == nil {
		t.Fatal("missing file should be rejected")
	}
}

func TestSafeFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // "" means must match the fallback pattern
	}{
		{"plain", "photo.jpg", "photo.jpg"},
		{"path strips to base", "a/b/c.txt", "c.txt"},
		{"traversal strips to leaf", "../etc/passwd", "passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SafeFilename(tc.in)
			if got != tc.want {
				t.Fatalf("SafeFilename(%q): want %q, got %q", tc.in, tc.want, got)
			}
		})
	}

	fallbacks := []string{"", ".", "..", "/"}
	for _, in := range fallbacks {
		t.Run("fallback/"+in, func(t *testing.T) {
			got := SafeFilename(in)
			if !strings.HasPrefix(got, "document_") {
				t.Fatalf("SafeFilename(%q) should fall back to document_*, got %q", in, got)
			}
		})
	}

	t.Run("null byte falls back", func(t *testing.T) {
		got := SafeFilename("evil\x00name.txt")
		if !strings.HasPrefix(got, "document_") {
			t.Fatalf("null-byte name should fall back, got %q", got)
		}
	})
}
