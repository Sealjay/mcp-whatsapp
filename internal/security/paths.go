// Package security holds policy-layer helpers used across the MCP bridge:
// path allowlisting for outbound media, filename sanitisation for inbound
// documents, and a log redactor for JIDs and message bodies.
package security

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ValidateMediaPath resolves userPath to an absolute, symlink-resolved path
// and returns it if it is equal to, or lives under, allowedRoot. Empty input
// returns "" with no error (the caller treats it as "no media"). Missing
// files and out-of-root paths both return wrapped errors naming the attempted
// path and root so an MCP client can surface them to the user.
func ValidateMediaPath(userPath, allowedRoot string) (string, error) {
	if userPath == "" {
		return "", nil
	}
	abs, err := filepath.Abs(userPath)
	if err != nil {
		return "", fmt.Errorf("invalid media_path %q: %w", userPath, err)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("media_path %q not accessible: %w", abs, err)
	}
	rootAbs, err := filepath.Abs(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("invalid allowed root %q: %w", allowedRoot, err)
	}
	rootAbs = filepath.Clean(rootAbs)
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// Root may not exist yet; fall back to the cleaned absolute form.
		rootResolved = rootAbs
	}
	if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
		return "", fmt.Errorf("media_path %q is outside allowed root %q; set WHATSAPP_MCP_MEDIA_ROOT or place the file under that root", resolved, rootResolved)
	}
	return resolved, nil
}

// SafeFilename returns filepath.Base(raw) unless the base is one of the
// degenerate values that could escape a join or hit special-case behaviour
// ("", ".", "..", "/", or contains a null byte), in which case it returns
// a timestamped fallback of the form "document_YYYYMMDD_HHMMSS". Matches
// the old whatsapp-bridge behaviour.
func SafeFilename(raw string) string {
	if strings.ContainsRune(raw, '\x00') {
		return fallbackFilename()
	}
	base := filepath.Base(raw)
	switch base {
	case "", ".", "..", "/":
		return fallbackFilename()
	}
	return base
}

func fallbackFilename() string {
	return "document_" + time.Now().UTC().Format("20060102_150405")
}
