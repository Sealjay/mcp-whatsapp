package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	mcpsrv "github.com/sealjay/mcp-whatsapp/internal/mcp"
	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// runSmoke boots the store + client (without connecting to WhatsApp servers)
// and constructs the MCP server. Used by CI to catch wire-up regressions.
func runSmoke(storeDir string, redactor *security.Redactor, args []string) int {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "whatsapp-mcp smoke: (CI) construct MCP server without connecting to WhatsApp; fails fast on wiring regressions.")
		fmt.Fprintln(os.Stderr, "\nUsage: whatsapp-mcp [-store DIR] smoke")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()

	st, err := store.Open(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	c, err := client.New(ctx, client.Config{
		StoreDir: storeDir,
		Store:    st,
		Logger:   client.NewStderrLogger("Smoke", "WARN", false),
		Redactor: redactor,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}
	defer c.Disconnect()

	srv := mcpsrv.NewServer(c)
	_ = srv // construction is the smoke test; not starting the server here

	fmt.Fprintln(os.Stderr, "smoke: OK — store opens, client initialises, MCP tools register.")
	return 0
}
