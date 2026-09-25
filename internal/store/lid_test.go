package store

import "testing"

func TestResolveLIDToJID(t *testing.T) {
	s := openTestStore(t)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"known lid", "99887766@lid", "447700000002@s.whatsapp.net"},
		{"known lid with device", "99887766:5@lid", "447700000002@s.whatsapp.net"},
		{"unknown lid", "11112222@lid", "11112222@lid"},
		{"plain s.whatsapp.net", "447700000001@s.whatsapp.net", "447700000001@s.whatsapp.net"},
		{"group jid", "123456789@g.us", "123456789@g.us"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.ResolveLIDToJID(tc.in)
			if got != tc.want {
				t.Fatalf("ResolveLIDToJID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveJIDToPhone(t *testing.T) {
	s := openTestStore(t)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"s.whatsapp.net", "447700000001@s.whatsapp.net", "447700000001"},
		{"known lid", "99887766@lid", "447700000002"},
		{"unknown lid", "11112222@lid", ""},
		{"group", "123456789@g.us", ""},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.ResolveJIDToPhone(tc.in)
			if got != tc.want {
				t.Fatalf("ResolveJIDToPhone(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveSenderPhone(t *testing.T) {
	s := openTestStore(t)

	cases := map[string]string{
		"99887766":     "447700000002", // LID user part (group participant or unresolved DM)
		"447700000001": "447700000001", // already a phone
		"11112222":     "11112222",     // unknown LID falls back unchanged
		"me":           "me",
		"":             "",
	}
	for in, want := range cases {
		if got := s.resolveSenderPhone(in); got != want {
			t.Errorf("resolveSenderPhone(%q) = %q, want %q", in, got, want)
		}
	}
}
