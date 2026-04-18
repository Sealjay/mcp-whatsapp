package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	return newPairHandlers(cache, reset), reset
}

// newResetRequest builds a POST request to /pair/reset with the given form
// values and (optionally) the given Origin header. host is used as r.Host so
// tests can control same-origin semantics.
func newResetRequest(host, origin string, form url.Values) *http.Request {
	body := ""
	if form != nil {
		body = form.Encode()
	}
	r := httptest.NewRequest(http.MethodPost, "/pair/reset", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if host != "" {
		r.Host = host
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
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
	// CSRF token must be embedded as a hidden form field.
	if !strings.Contains(body, `name="pair_token"`) {
		t.Fatal("paired body should include the pair_token hidden input")
	}
	if !strings.Contains(body, h.token()) {
		t.Fatal("paired body should contain the generated CSRF token value")
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

func TestHandlePairReset_SameOriginWithTokenAllowed(t *testing.T) {
	h, rst := newTestHandlers(true, "", nil)
	form := url.Values{"pair_token": {h.token()}}
	r := newResetRequest("127.0.0.1:8765", "http://127.0.0.1:8765", form)
	w := httptest.NewRecorder()
	h.handlePairReset(w, r)
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

func TestHandlePairReset_RejectsMissingOrigin(t *testing.T) {
	h, rst := newTestHandlers(true, "", nil)
	form := url.Values{"pair_token": {h.token()}}
	r := newResetRequest("127.0.0.1:8765", "", form)
	w := httptest.NewRecorder()
	h.handlePairReset(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403 when Origin is missing, got %d", w.Code)
	}
	if rst.called {
		t.Fatal("Logout must not run when Origin check fails")
	}
}

func TestHandlePairReset_RejectsCrossOrigin(t *testing.T) {
	h, rst := newTestHandlers(true, "", nil)
	form := url.Values{"pair_token": {h.token()}}
	r := newResetRequest("127.0.0.1:8765", "http://evil.example.com", form)
	w := httptest.NewRecorder()
	h.handlePairReset(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403 on cross-origin POST, got %d", w.Code)
	}
	if rst.called {
		t.Fatal("Logout must not run on cross-origin POST")
	}
}

func TestHandlePairReset_RejectsWrongToken(t *testing.T) {
	h, rst := newTestHandlers(true, "", nil)
	form := url.Values{"pair_token": {"not-the-real-token"}}
	r := newResetRequest("127.0.0.1:8765", "http://127.0.0.1:8765", form)
	w := httptest.NewRecorder()
	h.handlePairReset(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403 on bad token, got %d", w.Code)
	}
	if rst.called {
		t.Fatal("Logout must not run when token is wrong")
	}
}

func TestHandlePairReset_RefererFallback(t *testing.T) {
	// Browsers omit Origin on same-origin GETs but send it on POST; some
	// older clients still rely on Referer. Verify Referer works when
	// Origin is absent.
	h, rst := newTestHandlers(true, "", nil)
	form := url.Values{"pair_token": {h.token()}}
	body := form.Encode()
	r := httptest.NewRequest(http.MethodPost, "/pair/reset", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Host = "127.0.0.1:8765"
	r.Header.Set("Referer", "http://127.0.0.1:8765/pair")
	w := httptest.NewRecorder()
	h.handlePairReset(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: want 303 with valid Referer+token, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !rst.called {
		t.Fatal("Logout should run on valid Referer-based request")
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
	form := url.Values{"pair_token": {h.token()}}
	r := newResetRequest("127.0.0.1:8765", "http://127.0.0.1:8765", form)
	w := httptest.NewRecorder()
	h.handlePairReset(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}
