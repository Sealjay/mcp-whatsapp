package mcp

import (
	"context"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/ratelimit"
)

// TestWithRateLimitOverride verifies the header → context bypass mapping. The
// bypass must be set only for recognised truthy header values, and never in
// their absence — this is the boundary that keeps the LLM (which cannot set
// HTTP headers) from reaching the override.
func TestWithRateLimitOverride(t *testing.T) {
	cases := []struct {
		name       string
		headerVal  string // "" means header absent
		wantBypass bool
	}{
		{"absent", "", false},
		{"true", "true", true},
		{"one", "1", true},
		{"yes", "yes", true},
		{"TitleTrue", "True", true},
		{"UPPER", "TRUE", true},
		{"false", "false", false},
		{"zero", "0", false},
		{"garbage", "please", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			if tc.headerVal != "" {
				req.Header = http.Header{}
				req.Header.Set(rateLimitOverrideHeader, tc.headerVal)
			}
			ctx := withRateLimitOverride(context.Background(), req)
			if got := ratelimit.BypassFromContext(ctx); got != tc.wantBypass {
				t.Fatalf("header %q → bypass %v, want %v", tc.headerVal, got, tc.wantBypass)
			}
		})
	}
}

// TestWithRateLimitOverride_NilHeaderSafe: a request with no Header map at all
// (the common case for non-HTTP transports or unit calls) must not panic and
// must not grant bypass.
func TestWithRateLimitOverride_NilHeaderSafe(t *testing.T) {
	ctx := withRateLimitOverride(context.Background(), mcp.CallToolRequest{})
	if ratelimit.BypassFromContext(ctx) {
		t.Fatal("nil-header request must not grant bypass")
	}
}
