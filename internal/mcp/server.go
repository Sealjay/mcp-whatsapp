// Package mcp wires mark3labs/mcp-go to the internal/client and internal/store
// packages, exposing the WhatsApp bridge over MCP stdio.
package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/server"

	"github.com/sealjay/mcp-whatsapp/internal/client"
)

// Server holds the MCP server and its bound WhatsApp client.
type Server struct {
	client *client.Client
	mcp    *server.MCPServer
}

// NewServer constructs an MCP server with all tools registered against the
// provided WhatsApp client.
func NewServer(c *client.Client) *Server {
	mcpSrv := server.NewMCPServer(
		"whatsapp",
		"2.0.0",
		server.WithToolCapabilities(true),
	)
	s := &Server{client: c, mcp: mcpSrv}
	s.registerTools()
	return s
}

// MCP returns the underlying mcp-go server (tests only).
func (s *Server) MCP() *server.MCPServer { return s.mcp }

// ServeStdio serves the MCP protocol on stdin/stdout until the context is
// cancelled or stdin closes.
func (s *Server) ServeStdio(_ context.Context) error {
	return server.ServeStdio(s.mcp)
}
