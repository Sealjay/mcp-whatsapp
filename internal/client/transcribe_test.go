package client

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/sealjay/mcp-whatsapp/internal/store"
	"github.com/sealjay/mcp-whatsapp/internal/transcribe"
)

const trJID, trMsg = "447700000123@s.whatsapp.net", "AUDIO-1"

// transcribeFixture builds a client whose store holds one downloadable audio
// message and whose speech-to-text call is stubbed.
func transcribeFixture(t *testing.T, mediaType string, audio ...[]byte) (*Client, *int) {
	t.Helper()
	t.Chdir(t.TempDir())
	s, err := store.Open("./store")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	payload := []byte("fake-audio-bytes")
	if len(audio) > 0 {
		payload = audio[0]
	}
	digest := sha256.Sum256(payload)
	if err := s.StoreChat(trJID, "Test", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessage(context.Background(), store.Message{
		ID: trMsg, ChatJID: trJID, Sender: trJID, Timestamp: time.Now().UTC(),
		MediaType: mediaType, Filename: "voice.ogg", URL: "https://example.invalid/m", DirectPath: "/m",
	}, []byte{1}, digest[:], []byte{2}, uint64(len(payload))); err != nil {
		t.Fatal(err)
	}
	c := newClientWithStore(t, s)
	c.downloadMedia = func(context.Context, whatsmeow.DownloadableMessage) ([]byte, error) { return payload, nil }
	c.transcribeCfg = transcribe.Config{URL: "http://127.0.0.1:1", Model: "small", MaxSeconds: 600}
	calls := 0
	c.transcribeFn = func(_ context.Context, _ transcribe.Config, path, lang string) (transcribe.Result, error) {
		calls++
		return transcribe.Result{Text: "hola, soy " + lang, Language: "es", Duration: 2}, nil
	}
	return c, &calls
}

func TestTranscribeAudio_NotConfigured(t *testing.T) {
	c := newClientWithStore(t, nil)
	if _, _, err := c.TranscribeAudio(context.Background(), "m", "c", ""); !errors.Is(err, transcribe.ErrNotConfigured) {
		t.Fatalf("got %v", err)
	}
	if c.TranscriptionEnabled() {
		t.Fatal("must be disabled without a URL")
	}
}

func TestTranscribeAudio_TranscribesOnceAndCaches(t *testing.T) {
	c, calls := transcribeFixture(t, "audio")
	tr, cached, err := c.TranscribeAudio(context.Background(), trMsg, trJID, "es")
	if err != nil || cached || tr.Text != "hola, soy es" || tr.Language != "es" || tr.Model != "small" {
		t.Fatalf("first call: %+v cached=%v err=%v", tr, cached, err)
	}
	tr, cached, err = c.TranscribeAudio(context.Background(), trMsg, trJID, "")
	if err != nil || !cached || tr.Text != "hola, soy es" {
		t.Fatalf("second call: %+v cached=%v err=%v", tr, cached, err)
	}
	if *calls != 1 {
		t.Fatalf("the service was called %d times, want 1", *calls)
	}
}

func TestTranscribeAudio_RejectsNonAudioAndBadArgs(t *testing.T) {
	c, calls := transcribeFixture(t, "image")
	if _, _, err := c.TranscribeAudio(context.Background(), trMsg, trJID, ""); err == nil || !strings.Contains(err.Error(), "not audio") {
		t.Fatalf("non-audio: %v", err)
	}
	if *calls != 0 {
		t.Fatal("the service must not be called for non-audio media")
	}
	if _, _, err := c.TranscribeAudio(context.Background(), "", trJID, ""); err == nil {
		t.Fatal("empty message_id must fail")
	}
	if _, _, err := c.TranscribeAudio(context.Background(), "MISSING", trJID, ""); err == nil {
		t.Fatal("unknown message must fail")
	}
}

func TestTranscribeAudio_ServiceErrorIsNotCached(t *testing.T) {
	c, _ := transcribeFixture(t, "audio")
	c.transcribeFn = func(context.Context, transcribe.Config, string, string) (transcribe.Result, error) {
		return transcribe.Result{}, errors.New("backend down")
	}
	if _, _, err := c.TranscribeAudio(context.Background(), trMsg, trJID, ""); err == nil {
		t.Fatal("expected the service error")
	}
	if got, _ := c.store.GetTranscript(context.Background(), trMsg, trJID); got != nil {
		t.Fatalf("a failed transcription must not be cached: %+v", got)
	}
}

func transcriptionOpus(samples uint64) []byte {
	data := make([]byte, 47+29)
	copy(data, "OggS")
	data[5], data[26], data[27] = 2, 1, 19
	copy(data[28:], "OpusHead")
	data[36], data[37] = 1, 1
	binary.LittleEndian.PutUint16(data[38:40], 312)
	binary.LittleEndian.PutUint32(data[40:44], 48000)
	page := data[47:]
	copy(page, "OggS")
	page[5], page[26], page[27] = 4, 1, 1
	binary.LittleEndian.PutUint64(page[6:14], samples+312)
	binary.LittleEndian.PutUint32(page[18:22], 1)
	return data
}

func TestTranscribeAudio_DurationLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples uint64
		limit   int
		wantErr bool
	}{
		{"below default", 599 * 48000, 0, false},
		{"at default", 600 * 48000, 0, false},
		{"above default", 601 * 48000, 0, true},
		{"fraction above default", 600*48000 + 1, 0, true},
		{"custom limit", 3 * 48000, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := transcribeFixture(t, "audio", transcriptionOpus(tc.samples))
			c.transcribeCfg.MaxSeconds = tc.limit
			_, _, err := c.TranscribeAudio(context.Background(), trMsg, trJID, "")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "transcription limit") || *calls != 0 {
					t.Fatalf("err=%v backend calls=%d; expected limit rejection before upload", err, *calls)
				}
			} else if err != nil || *calls != 1 {
				t.Fatalf("err=%v backend calls=%d; expected one transcription", err, *calls)
			}
		})
	}
}

func TestTranscribeAudio_MalformedOggNotUploaded(t *testing.T) {
	c, calls := transcribeFixture(t, "audio", []byte("OggS"))
	if _, _, err := c.TranscribeAudio(context.Background(), trMsg, trJID, ""); err == nil || *calls != 0 {
		t.Fatalf("err=%v backend calls=%d; malformed Ogg must not reach the service", err, *calls)
	}
}

func TestTranscribeAudio_UnfinishedOpusNotUploaded(t *testing.T) {
	audio := transcriptionOpus(48000)[:75]
	audio[74] = 255
	audio = append(audio, make([]byte, 255)...)
	c, calls := transcribeFixture(t, "audio", audio)
	if _, _, err := c.TranscribeAudio(context.Background(), trMsg, trJID, ""); err == nil || *calls != 0 {
		t.Fatalf("err=%v backend calls=%d; unfinished Opus must not reach the service", err, *calls)
	}
}

func TestTranscribeAudio_MissingTextIsNotCached(t *testing.T) {
	for _, body := range []string{"null", "{}", `{"text":null}`} {
		t.Run(body, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			c, _ := transcribeFixture(t, "audio")
			c.transcribeCfg.URL, c.transcribeFn = srv.URL, nil
			for range 2 {
				if _, _, err := c.TranscribeAudio(context.Background(), trMsg, trJID, ""); err == nil {
					t.Fatal("missing transcript text must be an error")
				}
			}
			if got, err := c.store.GetTranscript(context.Background(), trMsg, trJID); err != nil || got != nil {
				t.Fatalf("invalid response was cached: %+v err=%v", got, err)
			}
			if requests.Load() != 2 {
				t.Fatalf("failed response must be retried, backend calls=%d", requests.Load())
			}
		})
	}
}
