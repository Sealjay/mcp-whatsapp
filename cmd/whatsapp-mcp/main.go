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
)

const usage = `whatsapp-mcp: WhatsApp bridge over MCP

Usage:
  whatsapp-mcp <command> [flags]

Commands:
  login   Pair this device with your WhatsApp account via QR code
  serve   Run the MCP stdio server (default for Claude Desktop etc.)
  smoke   Run a non-interactive boot check
  help    Show this help

Global flags (before the command):
  -store DIR   Directory holding messages.db and whatsapp.db (default: ./store)
`

func main() {
	var storeDir string
	fs := flag.NewFlagSet("whatsapp-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&storeDir, "store", "./store", "directory holding messages.db and whatsapp.db")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, rest := args[0], args[1:]
	var code int
	switch cmd {
	case "login":
		code = runLogin(storeDir, rest)
	case "serve":
		code = runServe(storeDir, rest)
	case "smoke":
		code = runSmoke(storeDir, rest)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		code = 2
	}
	os.Exit(code)
}
