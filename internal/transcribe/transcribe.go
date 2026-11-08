// Package transcribe talks to a speech-to-text service that exposes the
// OpenAI-compatible POST /v1/audio/transcriptions endpoint (a local
// whisper.cpp or faster-whisper server, LocalAI, or a hosted API).
//
// The feature is opt-in: it is disabled unless WHATSAPP_MCP_TRANSCRIBE_URL is
// set. To keep audio from leaving the machine by accident, a URL whose host is
// not loopback is refused unless WHATSAPP_MCP_TRANSCRIBE_ALLOW_REMOTE=1.
package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxSeconds = 600
	defaultTimeout    = 5 * time.Minute
	maxAudioBytes     = 40 << 20 // refuse to upload more than 40 MiB
	maxResponseBytes  = 1 << 20  // a transcript larger than 1 MiB is not plausible
)

// Config is the transcription configuration, read from the environment.
type Config struct {
	URL         string // base URL of the service, e.g. http://127.0.0.1:9000
	Model       string // optional model name sent to the service
	Language    string // optional default language (ISO 639-1); empty = autodetect
	Token       string // optional bearer token for the service
	MaxSeconds  int    // refuse audio longer than this (default 600)
	AllowRemote bool   // permit a non-loopback URL
	Timeout     time.Duration
}

// ConfigFromEnv reads the WHATSAPP_MCP_TRANSCRIBE_* variables.
func ConfigFromEnv() Config {
	c := Config{
		URL:         strings.TrimSpace(os.Getenv("WHATSAPP_MCP_TRANSCRIBE_URL")),
		Model:       strings.TrimSpace(os.Getenv("WHATSAPP_MCP_TRANSCRIBE_MODEL")),
		Language:    strings.TrimSpace(os.Getenv("WHATSAPP_MCP_TRANSCRIBE_LANGUAGE")),
		Token:       os.Getenv("WHATSAPP_MCP_TRANSCRIBE_TOKEN"),
		MaxSeconds:  defaultMaxSeconds,
		AllowRemote: os.Getenv("WHATSAPP_MCP_TRANSCRIBE_ALLOW_REMOTE") == "1",
		Timeout:     defaultTimeout,
	}
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("WHATSAPP_MCP_TRANSCRIBE_MAX_SECONDS"))); err == nil && v > 0 {
		c.MaxSeconds = v
	}
	return c
}

// Enabled reports whether a service URL is configured.
func (c Config) Enabled() bool { return c.URL != "" }

// endpoint validates the configuration and returns the full request URL.
func (c Config) endpoint() (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("transcription: WHATSAPP_MCP_TRANSCRIBE_URL must be an http(s) URL, got %q", c.URL)
	}
	if !c.AllowRemote && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("transcription: refusing %q: audio would leave this machine; set WHATSAPP_MCP_TRANSCRIBE_ALLOW_REMOTE=1 to allow a non-loopback service", u.Hostname())
	}
	base := strings.TrimRight(u.String(), "/")
	if !strings.HasSuffix(base, "/audio/transcriptions") {
		base = strings.TrimSuffix(base, "/v1") + "/v1/audio/transcriptions"
	}
	return base, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ErrNotConfigured is returned when no service URL is set.
var ErrNotConfigured = errors.New("transcription is not configured: set WHATSAPP_MCP_TRANSCRIBE_URL to an OpenAI-compatible speech-to-text service")

// Result is a transcription.
type Result struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration_seconds,omitempty"`
}

// Transcribe uploads the audio file at path to the service. language overrides
// the configured default when non-empty.
func Transcribe(ctx context.Context, cfg Config, path, language string) (Result, error) {
	endpoint, err := cfg.endpoint()
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("transcription: read audio: %w", err)
	}
	if len(data) == 0 {
		return Result{}, errors.New("transcription: audio file is empty")
	}
	if len(data) > maxAudioBytes {
		return Result{}, fmt.Errorf("transcription: audio is %d MiB; limit is %d MiB", len(data)>>20, maxAudioBytes>>20)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return Result{}, err
	}
	if _, err := fw.Write(data); err != nil {
		return Result{}, err
	}
	fields := map[string]string{"response_format": "verbose_json"}
	if cfg.Model != "" {
		fields["model"] = cfg.Model
	}
	if lang := firstNonEmpty(language, cfg.Language); lang != "" {
		fields["language"] = lang
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return Result{}, err
		}
	}
	if err := mw.Close(); err != nil {
		return Result{}, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	httpClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("transcription: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return Result{}, fmt.Errorf("transcription: service returned %s; redirects are not allowed; configure the endpoint directly", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("transcription: read response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return Result{}, errors.New("transcription: response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("transcription: service returned %s: %s", resp.Status, snippet(raw))
	}
	var out struct {
		Text     *string `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("transcription: unexpected response: %w", err)
	}
	if out.Text == nil {
		return Result{}, errors.New("transcription: unexpected response: missing text")
	}
	return Result{Text: strings.TrimSpace(*out.Text), Language: out.Language, Duration: out.Duration}, nil
}

// MaxSecondsOrDefault returns the configured limit.
func (c Config) MaxSecondsOrDefault() int {
	if c.MaxSeconds > 0 {
		return c.MaxSeconds
	}
	return defaultMaxSeconds
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
