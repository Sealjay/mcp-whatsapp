package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestFlexInt_UnmarshalJSON covers request_sync's count argument, which must
// accept both a JSON number and a JSON string (clients working from a cached
// tool schema that predates the parameter send it as a string).
func TestFlexInt_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    flexInt
		wantErr bool
	}{
		{name: "number", in: `10`, want: 10},
		{name: "numeric string", in: `"10"`, want: 10},
		{name: "empty string", in: `""`, want: 0},
		{name: "null", in: `null`, want: 0},
		{name: "non-numeric string", in: `"abc"`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f flexInt
			err := f.UnmarshalJSON([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s): want error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tc.in, err)
			}
			if f != tc.want {
				t.Errorf("UnmarshalJSON(%s) = %d, want %d", tc.in, f, tc.want)
			}
		})
	}
}

// TestFlexBool_UnmarshalJSON covers request_sync's use_lid argument, same
// string-tolerant contract as flexInt above.
func TestFlexBool_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    flexBool
		wantErr bool
	}{
		{name: "bool true", in: `true`, want: true},
		{name: "string true", in: `"true"`, want: true},
		{name: "string false", in: `"false"`, want: false},
		{name: "empty string leaves zero value", in: `""`, want: false},
		{name: "non-bool string", in: `"maybe"`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f flexBool
			err := f.UnmarshalJSON([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s): want error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tc.in, err)
			}
			if f != tc.want {
				t.Errorf("UnmarshalJSON(%s) = %v, want %v", tc.in, f, tc.want)
			}
		})
	}
}

// TestRequestSync_ArgValidation exercises the handler's own guards (anchor
// enum, non-negative count, required chat_jid) directly through the
// registered tool. All cases here must be rejected before the handler
// reaches s.client, so a nil client is safe.
func TestRequestSync_ArgValidation(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{name: "missing chat_jid", args: map[string]any{}},
		{name: "invalid anchor", args: map[string]any{"chat_jid": "447967960994@s.whatsapp.net", "anchor": "sideways"}},
		{name: "negative count", args: map[string]any{"chat_jid": "447967960994@s.whatsapp.net", "count": -1}},
		{name: "negative count as string", args: map[string]any{"chat_jid": "447967960994@s.whatsapp.net", "count": "-1"}},
	}
	s := NewServer(nil, nil)
	tool, ok := s.MCP().ListTools()["request_sync"]
	if !ok || tool == nil || tool.Handler == nil {
		t.Fatal("request_sync not registered")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tc.args
			res, err := tool.Handler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("args %v: want a rejected (IsError) result, got %+v", tc.args, res)
			}
		})
	}
}
