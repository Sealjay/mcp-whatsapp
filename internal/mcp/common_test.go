package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// TestRequireNonEmpty documents the helper's behaviour:
//   - empty string → standardised `<field>: required` error result
//   - any non-empty string (including whitespace-only) → nil
//
// Trimming is the caller's responsibility.
func TestRequireNonEmpty(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		value    string
		wantNil  bool
		wantText string // substring expected in the rendered result when wantNil is false
	}{
		{
			name:    "non-empty returns nil",
			field:   "chat_jid",
			value:   "123@s.whatsapp.net",
			wantNil: true,
		},
		{
			name:    "leading whitespace is preserved; still non-empty so returns nil",
			field:   "chat_jid",
			value:   " 123@s.whatsapp.net",
			wantNil: true,
		},
		{
			name:    "whitespace-only is NOT treated as empty (documented behaviour)",
			field:   "recipient",
			value:   "   ",
			wantNil: true,
		},
		{
			name:     "empty returns standard `<field>: required` error",
			field:    "chat_jid",
			value:    "",
			wantText: "chat_jid: required",
		},
		{
			name:     "empty preserves the supplied field name in the error",
			field:    "poll_message_id",
			value:    "",
			wantText: "poll_message_id: required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := requireNonEmpty(tc.field, tc.value)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("requireNonEmpty(%q, %q): want nil, got %+v", tc.field, tc.value, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("requireNonEmpty(%q, %q): want error result, got nil", tc.field, tc.value)
			}
			rendered := fmt.Sprintf("%+v", got)
			if !strings.Contains(rendered, tc.wantText) {
				t.Errorf("result = %s\nwant substring %q", rendered, tc.wantText)
			}
		})
	}
}
