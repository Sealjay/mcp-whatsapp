package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func runLogin(storeDir string, args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
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
		// login flow logs normally to stderr; the QR itself goes to stdout.
		Logger: client.NewStderrLogger("Client", "INFO", true),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}
	defer c.Disconnect()

	fmt.Fprintln(os.Stderr, "Starting pairing flow — scan the QR code below with your phone.")
	if err := c.Login(ctx, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Paired successfully. You can now run 'whatsapp-mcp serve'.")
	return 0
}
