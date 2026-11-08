package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func runHealthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	addr := fs.String("addr", "", "local daemon address (default: WHATSAPP_MCP_ADDR or 127.0.0.1:8765)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	host, port, err := net.SplitHostPort(resolveAddr(*addr))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: invalid address")
		return 2
	}
	ip := net.ParseIP(host)
	switch {
	case ip != nil && ip.IsUnspecified():
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	case ip != nil && ip.IsLoopback():
	case host == "localhost":
		host = "127.0.0.1"
	default:
		fmt.Fprintln(os.Stderr, "healthcheck: address must be loopback or a wildcard bind")
		return 2
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{
		Timeout:   3 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	target := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/readyz"}
	resp, err := httpClient.Get(target.String())
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: daemon unreachable")
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: not ready (HTTP %d)\n", resp.StatusCode)
		return 1
	}
	return 0
}
