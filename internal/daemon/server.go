package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// pairDriver is the minimal surface the Server needs from the underlying
// WhatsApp client. Production wiring (cmd/whatsapp-mcp/production_driver.go)
// satisfies this via *client.Client. Tests substitute a fake.
type pairDriver interface {
	IsLoggedIn() bool
	// StartPairing begins an unpaired session. onQR is invoked every time
	// whatsmeow emits a new pairing code. onSuccess is invoked once
	// pairing succeeds.
	StartPairing(ctx context.Context, onQR func(code string), onSuccess func()) error
	// Connect attaches event handlers (including LoggedOut) on an already-
	// paired session. onLoggedOut fires when the remote device revokes the
	// session.
	Connect(ctx context.Context, onLoggedOut func()) error
	Logout(ctx context.Context) error
	Disconnect()
}

// Config configures a daemon Server. Addr may be "host:port" or "host:0"
// (the latter only for tests). MCPMount, if non-nil, is called once the
// mux is being built so the caller can attach additional handlers (in
// practice, the /mcp Streamable HTTP handler from internal/mcp).
type Config struct {
	Addr     string
	Driver   pairDriver
	MCPMount func(mux *http.ServeMux)
}

// Server is the long-lived daemon process. Safe for a single Run call.
type Server struct {
	cfg   Config
	cache *PairCache

	mu         sync.Mutex
	httpServer *http.Server
	boundAddr  string
	listenerOK chan struct{} // closed once the listener is bound (tests use this)
}

// New constructs a Server but does not start any goroutines yet.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		return nil, errors.New("daemon.New: Addr required")
	}
	if cfg.Driver == nil {
		return nil, errors.New("daemon.New: Driver required")
	}
	return &Server{
		cfg:        cfg,
		cache:      NewPairCache(),
		listenerOK: make(chan struct{}),
	}, nil
}

// Cache exposes the pair cache (tests only).
func (s *Server) Cache() *PairCache { return s.cache }

// BoundAddr returns the address the HTTP listener actually bound to, useful
// when Addr was "host:0". Only valid after <-s.listenerOK fires.
func (s *Server) BoundAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundAddr
}

// Run starts the HTTP listener, begins the pairing state machine, and
// blocks until ctx is cancelled. On cancel, performs the shutdown
// sequence: drain HTTP → Disconnect driver.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	handlers := &pairHandlers{cache: s.cache, reset: driverLogout{s.cfg.Driver}}
	handlers.mount(mux)
	if s.cfg.MCPMount != nil {
		s.cfg.MCPMount(mux)
	} else {
		// No MCP attached (tests). Provide a 503 so the shape is observable.
		mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "mcp not mounted", http.StatusServiceUnavailable)
		})
	}

	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.mu.Lock()
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	s.boundAddr = ln.Addr().String()
	httpSrv := s.httpServer
	s.mu.Unlock()

	listenErrCh := make(chan error, 1)
	go func() { listenErrCh <- httpSrv.Serve(ln) }()

	close(s.listenerOK)

	// Drive the pairing state machine.
	if err := s.bootDriver(ctx); err != nil {
		_ = httpSrv.Shutdown(context.Background())
		return fmt.Errorf("boot driver: %w", err)
	}

	// Wait for cancellation or a listener failure.
	select {
	case <-ctx.Done():
	case err := <-listenErrCh:
		if err != nil && err != http.ErrServerClosed {
			s.cfg.Driver.Disconnect()
			return fmt.Errorf("http listen: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	s.cfg.Driver.Disconnect()
	return nil
}

// bootDriver inspects the driver's IsLoggedIn and either starts pairing
// or connects with the existing session. Safe to call only once per Run.
func (s *Server) bootDriver(ctx context.Context) error {
	onQR := func(code string) { s.cache.SetQR(code) }
	onPairSuccess := func() {
		s.cache.SetPaired()
		// Transition into Connect to install the LoggedOut handler.
		_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
	}

	if s.cfg.Driver.IsLoggedIn() {
		s.cache.SetPaired()
		return s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
	}
	return s.cfg.Driver.StartPairing(ctx, onQR, onPairSuccess)
}

// onLoggedOut returns a callback the driver invokes when the remote device
// revokes the session. Flips the cache to unpaired and kicks the pairing
// flow back up.
func (s *Server) onLoggedOut(ctx context.Context) func() {
	return func() {
		s.cache.Reset()
		onQR := func(code string) { s.cache.SetQR(code) }
		onPairSuccess := func() {
			s.cache.SetPaired()
			_ = s.cfg.Driver.Connect(ctx, s.onLoggedOut(ctx))
		}
		_ = s.cfg.Driver.StartPairing(ctx, onQR, onPairSuccess)
	}
}

// driverLogout adapts a pairDriver to the resetter interface required by
// /pair/reset. Having it as a named type (rather than inline closure) keeps
// the flow easy to read in Run.
type driverLogout struct{ d pairDriver }

func (d driverLogout) Logout(ctx context.Context) error { return d.d.Logout(ctx) }
