package main

import (
	"flag"
	"io"
	"testing"
)

func TestResolveFullHistory(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		env     string
		want    bool
		wantErr bool
	}{
		{name: "default off"},
		{name: "flag", args: []string{"-full-history"}, want: true},
		{name: "env 1", env: "1", want: true},
		{name: "env true", env: "true", want: true},
		{name: "env false", env: "false"},
		{name: "explicit false flag wins over env", args: []string{"-full-history=false"}, env: "1"},
		{name: "flag wins over bad env", args: []string{"-full-history"}, env: "yes please", want: true},
		{name: "bad env", env: "yes please", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			full := registerFullHistory(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parse %v: %v", tc.args, err)
			}
			got, err := resolveFullHistory(fs, *full, func(k string) string {
				if k == envFullHistory {
					return tc.env
				}
				return ""
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveFullHistory() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("resolveFullHistory() = %v, want %v", got, tc.want)
			}
		})
	}
}
