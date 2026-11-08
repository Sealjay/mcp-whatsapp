package store

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CachedMediaDir returns the per-message media cache directory. It rejects
// chat paths that escape the store or traverse a symlink.
func (s *Store) CachedMediaDir(chatJID, messageID string) (string, error) {
	if chatJID == "" || messageID == "" {
		return "", fmt.Errorf("chat_jid and message_id are required")
	}
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return "", fmt.Errorf("resolve store directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve store directory: %w", err)
	}
	root = filepath.Clean(root)
	chatDir := filepath.Clean(filepath.Join(root, strings.ReplaceAll(chatJID, ":", "_")))
	if !pathWithin(root, chatDir) {
		return "", fmt.Errorf("invalid chat directory: path escapes store")
	}
	if info, err := os.Lstat(chatDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("invalid chat directory: expected a real directory")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect chat directory: %w", err)
	}
	messageDir := filepath.Clean(filepath.Join(chatDir, hex.EncodeToString([]byte(messageID))))
	if !pathWithin(root, messageDir) {
		return "", fmt.Errorf("invalid message directory: path escapes store")
	}
	return messageDir, nil
}

// RemoveCachedMedia deletes only the cache directory for a revoked message.
func (s *Store) RemoveCachedMedia(chatJID, messageID string) error {
	dir, err := s.CachedMediaDir(chatJID, messageID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove cached media directory: %w", err)
	}
	return nil
}

func pathWithin(root, path string) bool {
	return path != root && strings.HasPrefix(path, root+string(filepath.Separator))
}
