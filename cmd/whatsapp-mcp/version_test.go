package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestVersionFlag builds the binary in a temp dir and invokes both `-version`
// and `--version`. This is the cheapest way to exercise the early-return path
// in main() without restructuring around os.Exit.
func TestVersionFlag(t *testing.T) {
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "whatsapp-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build",
		"-ldflags", "-X main.Version=1.2.3-test",
		"-o", bin, ".")
	build.Stderr = nil
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	for _, flag := range []string{"-version", "--version"} {
		out, err := exec.Command(bin, flag).CombinedOutput()
		if err != nil {
			t.Fatalf("%s exited non-zero: %v\n%s", flag, err, out)
		}
		got := strings.TrimSpace(string(out))
		want := "whatsapp-mcp 1.2.3-test"
		if got != want {
			t.Errorf("%s output = %q, want %q", flag, got, want)
		}
	}

	// -version should win even when a subcommand is also present.
	out, err := exec.Command(bin, "-version", "serve").CombinedOutput()
	if err != nil {
		t.Fatalf("-version serve exited non-zero: %v\n%s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "whatsapp-mcp ") {
		t.Errorf("-version serve output = %q, want version line", out)
	}
}
