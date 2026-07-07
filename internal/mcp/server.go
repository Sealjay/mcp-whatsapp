// Package mcp wires mark3labs/mcp-go to the internal/client and internal/store
// packages, exposing the WhatsApp bridge over MCP Streamable HTTP.
package mcp

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// pairingCache is the minimal pairing-state surface pairing_status needs.
// *daemon.PairCache satisfies it. The smoke command and tests pass nil, in
// which case pairing_status reports an "error" envelope.
type pairingCache interface {
	Paired() bool
	QR() string
}

// Server holds the MCP server, its bound WhatsApp client, and an optional
// pairing cache (used only by pairing_status).
type Server struct {
	client *client.Client
	mcp    *server.MCPServer
	cache  pairingCache
}

// NewServer constructs an MCP server with all tools registered against the
// provided WhatsApp client. cache may be nil (smoke/tests); pairing_status is
// the only tool that consults it.
func NewServer(c *client.Client, cache pairingCache) *Server {
	mcpSrv := server.NewMCPServer(
		"whatsapp",
		"2.0.0",
		server.WithToolCapabilities(true),
	)
	s := &Server{client: c, mcp: mcpSrv, cache: cache}
	s.registerTools()
	s.registerResources()
	return s
}

// MCP returns the underlying mcp-go server (tests only).
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// AttachHTTP mounts the MCP Streamable HTTP handler on mux at /mcp. The
// actual listener lifecycle is owned by the caller (internal/daemon).
func (s *Server) AttachHTTP(mux *http.ServeMux) {
	httpHandler := server.NewStreamableHTTPServer(s.mcp)
	mux.Handle("/mcp", httpHandler)
}
