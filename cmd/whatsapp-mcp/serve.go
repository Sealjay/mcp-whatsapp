package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	mcpsrv "github.com/sealjay/mcp-whatsapp/internal/mcp"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func runServe(storeDir string, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	lock, err := store.TryLock(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer lock.Release()

	st, err := store.Open(storeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		return 1
	}
	defer st.Close()

	c, err := client.New(ctx, client.Config{
		StoreDir: storeDir,
		Store:    st,
		Logger:   client.NewStderrLogger("Client", "INFO", false),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}
	defer c.Disconnect()

	c.StartEventHandler()
	if err := c.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\nHint: run 'whatsapp-mcp login' if this is the first run.\n", err)
		return 1
	}

	s := mcpsrv.NewServer(c)
	if err := s.ServeStdio(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}
