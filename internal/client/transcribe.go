package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sealjay/mcp-whatsapp/internal/media"
	"github.com/sealjay/mcp-whatsapp/internal/store"
	"github.com/sealjay/mcp-whatsapp/internal/transcribe"
)

// TranscriptionEnabled reports whether a speech-to-text service is configured.
func (c *Client) TranscriptionEnabled() bool { return c.transcribeCfg.Enabled() }

// TranscribeAudio returns the transcript of a cached audio message, transcribing
// it on first use and caching the result. The second return value is true when
// the transcript came from the cache.
//
// The audio is downloaded through the normal media path (so the usual size and
// hash checks apply), then sent to the configured speech-to-text service. The
// resulting text is third-party content and must be treated as untrusted.
func (c *Client) TranscribeAudio(ctx context.Context, messageID, chatJID, language string) (*store.Transcript, bool, error) {
	if !c.transcribeCfg.Enabled() {
		return nil, false, transcribe.ErrNotConfigured
	}
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(chatJID) == "" {
		return nil, false, errors.New("message_id and chat_jid are required")
	}
	if t, err := c.store.GetTranscript(ctx, messageID, chatJID); err != nil {
		return nil, false, err
	} else if t != nil {
		return t, true, nil
	}

	// One transcription at a time: the service is typically CPU-bound.
	c.transcribeMu.Lock()
	defer c.transcribeMu.Unlock()
	// Another call may have finished the same message while we waited.
	if t, err := c.store.GetTranscript(ctx, messageID, chatJID); err != nil {
		return nil, false, err
	} else if t != nil {
		return t, true, nil
	}

	dl := c.Download(ctx, messageID, chatJID, "")
	if !dl.Success {
		return nil, false, fmt.Errorf("download audio: %s", dl.Message)
	}
	if dl.MediaType != "audio" {
		return nil, false, fmt.Errorf("message is %q media, not audio", dl.MediaType)
	}
	if err := c.checkAudioDuration(dl.Path); err != nil {
		return nil, false, err
	}

	fn := c.transcribeFn
	if fn == nil {
		fn = transcribe.Transcribe
	}
	res, err := fn(ctx, c.transcribeCfg, dl.Path, language)
	if err != nil {
		return nil, false, err
	}
	t := store.Transcript{
		MessageID: messageID,
		ChatJID:   chatJID,
		Text:      res.Text,
		Language:  res.Language,
		Model:     c.transcribeCfg.Model,
		Duration:  res.Duration,
	}
	if err := c.store.PutTranscript(ctx, t); err != nil {
		return nil, false, err
	}
	stored, err := c.store.GetTranscript(ctx, messageID, chatJID)
	if err != nil || stored == nil {
		return &t, false, nil
	}
	return stored, false, nil
}

// checkAudioDuration rejects voice notes longer than the configured limit
// before they are uploaded. Only Ogg/Opus (what WhatsApp voice notes use) can be
// measured here; other formats are left to the service's own limit.
func (c *Client) checkAudioDuration(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read audio: %w", err)
	}
	if !bytes.HasPrefix(data, []byte("OggS")) {
		return nil // Other formats use the service's duration limit.
	}
	secs, _, err := media.AnalyzeOggOpus(data)
	if err != nil {
		return fmt.Errorf("analyze Ogg audio: %w", err)
	}
	if limit := c.transcribeCfg.MaxSecondsOrDefault(); uint64(secs) > uint64(limit) {
		return fmt.Errorf("audio is %ds long; the transcription limit is %ds (WHATSAPP_MCP_TRANSCRIBE_MAX_SECONDS)", secs, limit)
	}
	return nil
}
