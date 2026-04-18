package daemon

import (
	"context"
	"fmt"
	"net/http"
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
}

func (h *pairHandlers) mount(mux *http.ServeMux) {
	mux.HandleFunc("/pair", h.handlePairPage)
	mux.HandleFunc("/pair/qr.png", h.handlePairQR)
	mux.HandleFunc("/pair/reset", h.handlePairReset)
}

func (h *pairHandlers) handlePairPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.cache.Paired() {
		fmt.Fprint(w, pairedPage)
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

const pairingPage = `<!doctype html>
<html><head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="5">
<title>Pair WhatsApp</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Pair WhatsApp</h1>
<p>Open WhatsApp on your phone → <em>Settings</em> → <em>Linked Devices</em> → <em>Link a Device</em>, then scan:</p>
<p><img src="/pair/qr.png" alt="WhatsApp pairing QR"></p>
<p><small>This page auto-refreshes every 5 seconds while the QR rotates.</small></p>
</body></html>`

const pairedPage = `<!doctype html>
<html><head>
<meta charset="utf-8">
<title>Paired</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Already paired</h1>
<p>This daemon is connected to WhatsApp. You can close this tab.</p>
<form method="post" action="/pair/reset">
  <p><button type="submit">Force re-pair</button></p>
</form>
<p><small>"Force re-pair" disconnects this device from WhatsApp and starts a fresh pairing flow. Use it if you want to switch accounts.</small></p>
</body></html>`
