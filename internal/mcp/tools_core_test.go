package mcp

import "testing"

// TestNormalizeRecipientToChatJID documents the chat-JID derivation used by
// the post-send MarkChatRead path. It mirrors parseRecipient's leading-`+`
// stripping so a successful send (which already accepted `+447…`) is followed
// by a valid chat-JID lookup rather than `+447…@s.whatsapp.net` which the
// store cannot resolve.
func TestNormalizeRecipientToChatJID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "bare digits", in: "447967960994", want: "447967960994@s.whatsapp.net"},
		{name: "leading plus is stripped", in: "+447967960994", want: "447967960994@s.whatsapp.net"},
		{name: "user JID passes through", in: "447967960994@s.whatsapp.net", want: "447967960994@s.whatsapp.net"},
		{name: "group JID passes through", in: "120363025246125486@g.us", want: "120363025246125486@g.us"},
		{name: "invalid bare input returns empty", in: "447 967 960994", want: ""},
		{name: "garbage returns empty", in: "abc", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRecipientToChatJID(tc.in); got != tc.want {
				t.Errorf("normalizeRecipientToChatJID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
