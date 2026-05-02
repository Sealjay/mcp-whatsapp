package daemon

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"sync"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var (
	pairTmpl   = template.Must(template.ParseFS(templateFS, "templates/pair.html.tmpl"))
	pairedTmpl = template.Must(template.ParseFS(templateFS, "templates/pair_success.html.tmpl"))
)

const (
	qrPNGSize = 256
)

// pairPageData is the template context for pair pages.
type pairPageData struct {
	CSRFToken string
}

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

	// Rate limiters keyed by endpoint path.
	pairGetLimiter   *Limiter
	pairQRLimiter    *Limiter
	pairResetLimiter *Limiter

	// CSRF protection for the reset endpoint.
	csrfMu    sync.Mutex
	csrfToken string
}

// newPairHandlers constructs handlers with default rate limiters.
func newPairHandlers(cache *PairCache, reset resetter) *pairHandlers {
	return &pairHandlers{
		cache:            cache,
		reset:            reset,
		pairGetLimiter:   NewLimiter(5.0/60.0, 5),   // 5/min, burst 5
		pairQRLimiter:    NewLimiter(10.0/60.0, 10), // 10/min, burst 10
		pairResetLimiter: NewLimiter(1.0/60.0, 1),   // 1/min, burst 1
	}
}

// generateCSRFToken creates a new random CSRF token, stores it in the
// handler, and returns it. Thread-safe.
func (h *pairHandlers) generateCSRFToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback should never be reached in practice.
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	token := hex.EncodeToString(b)
	h.csrfMu.Lock()
	h.csrfToken = token
	h.csrfMu.Unlock()
	return token
}

// validCSRFToken checks whether the supplied token matches the stored one.
// A match consumes the token (single-use). Thread-safe.
func (h *pairHandlers) validCSRFToken(token string) bool {
	h.csrfMu.Lock()
	defer h.csrfMu.Unlock()
	if h.csrfToken == "" || token == "" {
		return false
	}
	ok := h.csrfToken == token
	if ok {
		h.csrfToken = "" // single-use: consume after validation
	}
	return ok
}

func (h *pairHandlers) mount(mux *http.ServeMux) {
	mux.HandleFunc("/pair", h.handlePairPage)
	mux.HandleFunc("/pair/qr.png", h.handlePairQR)
	mux.HandleFunc("/pair/reset", h.handlePairReset)
}

func (h *pairHandlers) handlePairPage(w http.ResponseWriter, r *http.Request) {
	if !h.pairGetLimiter.Allow("/pair") {
		w.Header().Set("Retry-After", "12")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := pairPageData{
		CSRFToken: h.generateCSRFToken(),
	}
	if h.cache.Paired() {
		if err := pairedTmpl.Execute(w, data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
		return
	}
	if err := pairTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (h *pairHandlers) handlePairQR(w http.ResponseWriter, r *http.Request) {
	if !h.pairQRLimiter.Allow("/pair/qr.png") {
		w.Header().Set("Retry-After", "6")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
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
	if !h.pairResetLimiter.Allow("/pair/reset") {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	// Validate the CSRF token from the form submission.
	if !h.validCSRFToken(r.FormValue("csrf_token")) {
		http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
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
