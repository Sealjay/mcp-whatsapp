package transcribe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeAudio(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "voice.ogg")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscribe_Success(t *testing.T) {
	var gotAuth, gotPath, gotLang, gotModel, gotFormat, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotLang, gotModel, gotFormat = r.FormValue("language"), r.FormValue("model"), r.FormValue("response_format")
		f, _, _ := r.FormFile("file")
		b, _ := io.ReadAll(f)
		gotFile = string(b)
		_, _ = w.Write([]byte(`{"text":"  hola mundo ","language":"es","duration":3.5}`))
	}))
	defer srv.Close()

	cfg := Config{URL: srv.URL, Model: "small", Language: "es", Token: "tok"}
	res, err := Transcribe(context.Background(), cfg, writeAudio(t, "AUDIO"), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hola mundo" || res.Language != "es" || res.Duration != 3.5 {
		t.Fatalf("result: %+v", res)
	}
	if gotPath != "/v1/audio/transcriptions" || gotAuth != "Bearer tok" || gotLang != "es" ||
		gotModel != "small" || gotFormat != "verbose_json" || gotFile != "AUDIO" {
		t.Fatalf("request: path=%q auth=%q lang=%q model=%q fmt=%q file=%q", gotPath, gotAuth, gotLang, gotModel, gotFormat, gotFile)
	}

	// A per-call language overrides the configured default.
	if _, err := Transcribe(context.Background(), cfg, writeAudio(t, "AUDIO"), "en"); err != nil {
		t.Fatal(err)
	}
	if gotLang != "en" {
		t.Fatalf("language override: %q", gotLang)
	}
}

func TestTranscribe_ServiceErrorAndBadJSON(t *testing.T) {
	status := http.StatusInternalServerError
	body := "boom"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	cfg := Config{URL: srv.URL}

	if _, err := Transcribe(context.Background(), cfg, writeAudio(t, "A"), ""); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want a 500 error, got %v", err)
	}
	status, body = http.StatusOK, "not json"
	if _, err := Transcribe(context.Background(), cfg, writeAudio(t, "A"), ""); err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("want an unexpected-response error, got %v", err)
	}
}

func TestTranscribe_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	cfg := Config{URL: srv.URL, Timeout: 50 * time.Millisecond}
	if _, err := Transcribe(context.Background(), cfg, writeAudio(t, "A"), ""); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestTranscribe_RejectsRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`{"text":"redirected"}`))
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, status)
			}))
			defer source.Close()

			_, err := Transcribe(context.Background(), Config{URL: source.URL, Token: "test-token"}, writeAudio(t, "AUDIO"), "")
			if requests.Load() != 0 {
				t.Fatal("transcription followed the redirect")
			}
			if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) || !strings.Contains(err.Error(), "redirect") {
				t.Fatalf("want an explicit redirect error, got %v", err)
			}
		})
	}
}

func TestTranscribe_RejectsHTTPSDowngrade(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"text":"redirected"}`))
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	originalTransport := http.DefaultTransport
	http.DefaultTransport = source.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	_, err := Transcribe(context.Background(), Config{URL: source.URL, AllowRemote: true, Token: "test-token"}, writeAudio(t, "AUDIO"), "")
	if requests.Load() != 0 {
		t.Fatal("transcription followed an HTTPS-to-HTTP redirect")
	}
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("want an explicit redirect error, got %v", err)
	}
}

func TestTranscribe_NotConfiguredAndEmptyFile(t *testing.T) {
	if _, err := Transcribe(context.Background(), Config{}, writeAudio(t, "A"), ""); err != ErrNotConfigured {
		t.Fatalf("got %v", err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Transcribe(context.Background(), Config{URL: srv.URL}, writeAudio(t, ""), ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
}

func TestEndpoint_LoopbackRule(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:9000", "http://localhost:9000", "http://[::1]:9000", "http://127.0.0.5/"} {
		if _, err := (Config{URL: u}).endpoint(); err != nil {
			t.Errorf("%s must be accepted: %v", u, err)
		}
	}
	for _, u := range []string{"http://10.0.0.5:9000", "https://api.example.com", "http://whisper.internal"} {
		if _, err := (Config{URL: u}).endpoint(); err == nil || !strings.Contains(err.Error(), "ALLOW_REMOTE") {
			t.Errorf("%s must be refused without ALLOW_REMOTE, got %v", u, err)
		}
		if _, err := (Config{URL: u, AllowRemote: true}).endpoint(); err != nil {
			t.Errorf("%s must be accepted with AllowRemote: %v", u, err)
		}
	}
	if _, err := (Config{URL: "ftp://127.0.0.1"}).endpoint(); err == nil {
		t.Error("non-http scheme must be rejected")
	}
}

func TestEndpoint_Normalisation(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:9000":                         "http://127.0.0.1:9000/v1/audio/transcriptions",
		"http://127.0.0.1:9000/":                        "http://127.0.0.1:9000/v1/audio/transcriptions",
		"http://127.0.0.1:9000/v1":                      "http://127.0.0.1:9000/v1/audio/transcriptions",
		"http://127.0.0.1:9000/v1/audio/transcriptions": "http://127.0.0.1:9000/v1/audio/transcriptions",
	}
	for in, want := range cases {
		got, err := (Config{URL: in}).endpoint()
		if err != nil || got != want {
			t.Errorf("%q -> %q (%v), want %q", in, got, err, want)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_URL", " http://127.0.0.1:9000 ")
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_MODEL", "small")
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_LANGUAGE", "es")
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_TOKEN", "secret")
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_MAX_SECONDS", "120")
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_ALLOW_REMOTE", "1")
	c := ConfigFromEnv()
	if !c.Enabled() || c.URL != "http://127.0.0.1:9000" || c.Model != "small" || c.Language != "es" ||
		c.Token != "secret" || c.MaxSeconds != 120 || !c.AllowRemote {
		t.Fatalf("%+v", c)
	}
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_URL", "")
	t.Setenv("WHATSAPP_MCP_TRANSCRIBE_MAX_SECONDS", "abc")
	c = ConfigFromEnv()
	if c.Enabled() || c.MaxSecondsOrDefault() != defaultMaxSeconds {
		t.Fatalf("defaults: %+v", c)
	}
}
