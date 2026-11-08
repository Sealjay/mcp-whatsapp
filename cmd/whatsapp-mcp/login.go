package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

func runLogin(storeDir string, redactor *security.Redactor, args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "whatsapp-mcp login: pair this device via QR (or a phone-number pairing code with -phone); writes session to <store>/whatsapp.db. Ctrl-C aborts.")
		fmt.Fprintln(os.Stderr, "\nUsage: whatsapp-mcp [-store DIR] login [-phone +<international number>] [-full-history]")
		fmt.Fprintln(os.Stderr)
		fs.PrintDefaults()
	}
	phone := fs.String("phone", "", "pair by phone number instead of QR: enter the printed code in WhatsApp (international format, e.g. +919876543210)")
	fullHistoryFlag := registerFullHistory(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fullHistory, err := resolveFullHistory(fs, *fullHistoryFlag, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "login: %v\n", err)
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
		Logger:      client.NewStderrLogger("Client", "INFO", true),
		Redactor:    redactor,
		FullHistory: fullHistory,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init client: %v\n", err)
		return 1
	}
	defer c.Disconnect()

	if *phone != "" {
		fmt.Fprintln(os.Stderr, "Starting phone-number pairing flow.")
		if err := c.PairPhoneLogin(ctx, *phone, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintln(os.Stderr, "Starting pairing flow — scan the QR code below with your phone.")
		if err := c.Login(ctx, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
			return 1
		}
	}
	fmt.Fprintln(os.Stderr, "Paired successfully. You can now run 'whatsapp-mcp serve'.")
	return 0
}
