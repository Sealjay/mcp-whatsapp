package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeResetter struct {
	called bool
	err    error
}

func (f *fakeResetter) Logout(_ context.Context) error {
	f.called = true
	return f.err
}

func newTestHandlers(paired bool, qr string, err error) (*pairHandlers, *fakeResetter) {
	cache := NewPairCache()
	if paired {
		cache.SetPaired()
	} else if qr != "" {
		cache.SetQR(qr)
	}
	reset := &fakeResetter{err: err}
	h := newPairHandlers(cache, reset)
	return h, reset
}

func TestHandlePairPage_Unpaired(t *testing.T) {
	h, _ := newTestHandlers(false, "abc", nil)
	w := httptest.NewRecorder()
	h.handlePairPage(w, httptest.NewRequest(http.MethodGet, "/pair", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type: want text/html*, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Pair WhatsApp") {
		t.Fatalf("unpaired body should contain the pairing instructions, got %q", body)
	}
	if !strings.Contains(body, `<img src="/pair/qr.png"`) {
		t.Fatal("unpaired body should reference /pair/qr.png")
	}
}

func TestHandlePairPage_Paired(t *testing.T) {
	h, _ := newTestHandlers(true, "", nil)
	w := httptest.NewRecorder()
	h.handlePairPage(w, httptest.NewRequest(http.MethodGet, "/pair", nil))
	body := w.Body.String()
	if !strings.Contains(body, "Already paired") {
		t.Fatalf("paired body should say Already paired, got %q", body)
	}
	if !strings.Contains(body, `action="/pair/reset"`) {
		t.Fatal("paired body should include the re-pair form")
	}
}

func TestHandlePairQR_HasQR(t *testing.T) {
	h, _ := newTestHandlers(false, "test payload", nil)
	w := httptest.NewRecorder()
	h.handlePairQR(w, httptest.NewRequest(http.MethodGet, "/pair/qr.png", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type: want image/png, got %q", ct)
	}
	body := w.Body.Bytes()
	if len(body) < 100 {
		t.Fatalf("expected PNG body, got %d bytes", len(body))
	}
	if body[0] != 0x89 || body[1] != 0x50 {
		t.Fatal("body does not start with PNG magic bytes")
	}
}

func TestHandlePairQR_NoQR(t *testing.T) {
	h, _ := newTestHandlers(false, "", nil)
	w := httptest.NewRecorder()
	h.handlePairQR(w, httptest.NewRequest(http.MethodGet, "/pair/qr.png", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 with empty cache, got %d", w.Code)
	}
}

func TestHandlePairReset_POST_CallsLogoutAndRedirects(t *testing.T) {
	h, rst := newTestHandlers(true, "", nil)
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodPost, "/pair/reset", nil))
	if !rst.called {
		t.Fatal("Logout was not called")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: want 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pair" {
		t.Fatalf("location: want /pair, got %q", loc)
	}
	if h.cache.Paired() {
		t.Fatal("cache should be reset after Logout")
	}
}

func TestHandlePairReset_RejectsGET(t *testing.T) {
	h, _ := newTestHandlers(true, "", nil)
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodGet, "/pair/reset", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

func TestHandlePairReset_LogoutErrorSurfaces(t *testing.T) {
	h, _ := newTestHandlers(true, "", errors.New("boom"))
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodPost, "/pair/reset", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}

func TestPairGet_RateLimited(t *testing.T) {
	h, _ := newTestHandlers(false, "abc", nil)
	// Exhaust the burst of 5.
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.handlePairPage(w, httptest.NewRequest(http.MethodGet, "/pair", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i, w.Code)
		}
	}
	// 6th should be rate-limited.
	w := httptest.NewRecorder()
	h.handlePairPage(w, httptest.NewRequest(http.MethodGet, "/pair", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: want 429, got %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429")
	}
}

func TestPairReset_RateLimited(t *testing.T) {
	h, _ := newTestHandlers(true, "", nil)
	// First POST should succeed (burst=1).
	w := httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodPost, "/pair/reset", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("first POST: want 303, got %d", w.Code)
	}
	// Second should be rate-limited.
	w = httptest.NewRecorder()
	h.handlePairReset(w, httptest.NewRequest(http.MethodPost, "/pair/reset", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second POST: want 429, got %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429")
	}
}

func TestPairTemplate_EscapesToken(t *testing.T) {
	cache := NewPairCache()
	cache.SetPaired()
	h := newPairHandlers(cache, &fakeResetter{})

	// Inject a CSRFToken containing a script tag to verify html/template escapes it.
	// We need to call the template directly since the handler doesn't set CSRFToken yet.
	w := httptest.NewRecorder()
	data := pairPageData{CSRFToken: `<script>alert("xss")</script>`}
	if err := pairedTmpl.Execute(w, data); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	body := w.Body.String()
	// html/template should escape < and > in the value attribute.
	if strings.Contains(body, "<script>") {
		t.Fatalf("template did not escape <script> tag in CSRFToken: %s", body)
	}
	// The escaped form should be present.
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag in output, got: %s", body)
	}

	// Sanity: the handler still works.
	_ = h
}
