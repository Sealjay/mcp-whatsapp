// Command whatsapp-mcp is a single-binary WhatsApp bridge that speaks MCP
// over stdio. Subcommands:
//
//	login  — one-shot interactive QR pairing (writes session to store/whatsapp.db)
//	serve  — MCP stdio server for Claude/Cursor/etc.
//	smoke  — non-interactive smoke test used by CI
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sealjay/mcp-whatsapp/internal/security"
)

const usage = `whatsapp-mcp: WhatsApp bridge over MCP

Usage:
  whatsapp-mcp <command> [flags]

Commands:
  login   Pair this device with your WhatsApp account via QR code (terminal)
  serve   Run the always-on HTTP MCP daemon (tracks events + serves MCP on 127.0.0.1:8765)
  smoke   Run a non-interactive boot check
  help    Show this help

Global flags (before the command):
  -store DIR   Directory holding messages.db and whatsapp.db (default: ./store)
  -debug       Show full JIDs and message bodies in logs (default: redacted)

Serve-specific flags (after 'serve'):
  -addr host:port   HTTP bind address (default: 127.0.0.1:8765, env WHATSAPP_MCP_ADDR)
  -allow-remote     Allow binding to a non-loopback address (dangerous)
`

func main() {
	var (
		storeDir string
		debug    bool
	)
	fs := flag.NewFlagSet("whatsapp-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&storeDir, "store", "./store", "directory holding messages.db and whatsapp.db")
	fs.BoolVar(&debug, "debug", false, "show full JIDs and message bodies in logs (default: redacted)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	if os.Getenv("WHATSAPP_MCP_DEBUG") == "1" {
		debug = true
	}
	redactor := &security.Redactor{Debug: debug}

	cmd, rest := args[0], args[1:]
	var code int
	switch cmd {
	case "login":
		code = runLogin(storeDir, redactor, rest)
	case "serve":
		code = runServe(storeDir, redactor, rest)
	case "smoke":
		code = runSmoke(storeDir, redactor, rest)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		code = 2
	}
	os.Exit(code)
}
