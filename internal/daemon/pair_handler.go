package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sync"
)

const (
	qrPNGSize = 256
)

// resetter is the dependency `handlePairReset` needs. Production wiring
// satisfies it via *client.Client; tests substitute a fake.
type resetter interface {
	Logout(ctx context.Context) error
}

// pairHandlers bundles the three pair endpoints against a shared cache and
// a resetter. Wire to an *http.ServeMux via mount.
type pairHandlers struct {
	cache   *PairCache
	reset   resetter
	onReset func() // optional hook invoked after a successful Logout

	// csrfToken is generated once when the handlers are constructed. It is
	// embedded in the /pair page as a hidden form input and required to
	// match on /pair/reset POST. Process-lifetime scope is sufficient: a
	// restart invalidates outstanding forms, which is acceptable.
	tokenMu   sync.RWMutex
	csrfToken string
}

// newPairHandlers constructs pairHandlers with a freshly generated CSRF token.
func newPairHandlers(cache *PairCache, reset resetter) *pairHandlers {
	return &pairHandlers{
		cache:     cache,
		reset:     reset,
		csrfToken: generateCSRFToken(),
	}
}

// generateCSRFToken returns a 32-byte, base64url-encoded random token.
func generateCSRFToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on a healthy OS does not fail; panic is
		// acceptable because failure here means the process can't
		// safely serve the pairing UI at all.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (h *pairHandlers) token() string {
	h.tokenMu.RLock()
	defer h.tokenMu.RUnlock()
	return h.csrfToken
}

func (h *pairHandlers) mount(mux *http.ServeMux) {
	mux.HandleFunc("/pair", h.handlePairPage)
	mux.HandleFunc("/pair/qr.png", h.handlePairQR)
	mux.HandleFunc("/pair/reset", h.handlePairReset)
}

func (h *pairHandlers) handlePairPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.cache.Paired() {
		if err := pairedPageTmpl.Execute(w, pairedPageData{CSRFToken: h.token()}); err != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
		return
	}
	fmt.Fprint(w, pairingPage)
}

func (h *pairHandlers) handlePairQR(w http.ResponseWriter, r *http.Request) {
	qr := h.cache.QR()
	if qr == "" {
		http.NotFound(w, r)
		return
	}
	png, err := renderQRPNG(qr, qrPNGSize)
	if err != nil {
		http.Error(w, "qr render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (h *pairHandlers) handlePairReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginRequest(r) {
		http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	submitted := r.Form.Get("pair_token")
	expected := h.token()
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(expected)) != 1 {
		http.Error(w, "forbidden: invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := h.reset.Logout(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf("logout failed: %v", err), http.StatusInternalServerError)
		return
	}
	h.cache.Reset()
	if h.onReset != nil {
		h.onReset()
	}
	http.Redirect(w, r, "/pair", http.StatusSeeOther)
}

// sameOriginRequest checks that the request's Origin header (or Referer as a
// fallback) names the same host as the target r.Host. Missing both headers is
// treated as a rejection: we refuse to accept POSTs where the origin cannot
// be validated. Loopback deployments still get an Origin header from real
// browsers.
func sameOriginRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == r.Host
}

const pairingPage = `<!doctype html>
<html><head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="5">
<title>Pair WhatsApp</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Pair WhatsApp</h1>
<p>Open WhatsApp on your phone -> <em>Settings</em> -> <em>Linked Devices</em> -> <em>Link a Device</em>, then scan:</p>
<p><img src="/pair/qr.png" alt="WhatsApp pairing QR"></p>
<p><small>This page auto-refreshes every 5 seconds while the QR rotates.</small></p>
</body></html>`

type pairedPageData struct {
	CSRFToken string
}

// pairedPageTmpl renders the "already paired" page with an embedded CSRF
// token that /pair/reset requires on POST. html/template escapes the token
// value so a compromised RNG cannot inject markup.
var pairedPageTmpl = template.Must(template.New("paired").Parse(`<!doctype html>
<html><head>
<meta charset="utf-8">
<title>Paired</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Already paired</h1>
<p>This daemon is connected to WhatsApp. You can close this tab.</p>
<form method="post" action="/pair/reset">
  <input type="hidden" name="pair_token" value="{{.CSRFToken}}">
  <p><button type="submit">Force re-pair</button></p>
</form>
<p><small>"Force re-pair" disconnects this device from WhatsApp and starts a fresh pairing flow. Use it if you want to switch accounts.</small></p>
</body></html>`))
