package main

import (
	"flag"
	"fmt"
	"strconv"
)

// envFullHistory is the environment equivalent of -full-history. A flag given
// on the command line wins over it.
const envFullHistory = "WHATSAPP_MCP_FULL_HISTORY"

// registerFullHistory adds -full-history, shared by login and serve (both can pair).
func registerFullHistory(fs *flag.FlagSet) *bool {
	return fs.Bool("full-history", false, "when pairing, ask the phone for a full history sync instead of its recent window (env "+envFullHistory+")")
}

// resolveFullHistory combines the parsed flag with the environment (flag
// wins, including an explicit -full-history=false). fs must already be parsed.
func resolveFullHistory(fs *flag.FlagSet, flagValue bool, getenv func(string) string) (bool, error) {
	set := false
	fs.Visit(func(fl *flag.Flag) { set = set || fl.Name == "full-history" })
	if set {
		return flagValue, nil
	}
	v := getenv(envFullHistory)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q: not a boolean", envFullHistory, v)
	}
	return b, nil
}
