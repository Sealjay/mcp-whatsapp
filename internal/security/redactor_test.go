package security

import "testing"

func TestRedactor_JID(t *testing.T) {
	r := &Redactor{}
	cases := map[string]string{
		"15551234567@s.whatsapp.net": "…4567",
		"123@s.whatsapp.net":         "…123",
		"":                           "…",
		"abc":                        "…abc",
		"12345":                      "…2345",
		"120363040000000000@g.us":    "…0000",
		"abcdef@lid":                 "…cdef",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := r.JID(in); got != want {
				t.Fatalf("JID(%q): want %q, got %q", in, want, got)
			}
		})
	}
}

func TestRedactor_JID_Debug(t *testing.T) {
	r := &Redactor{Debug: true}
	in := "15551234567@s.whatsapp.net"
	if got := r.JID(in); got != in {
		t.Fatalf("debug mode should pass through, got %q", got)
	}
}

func TestRedactor_Body(t *testing.T) {
	r := &Redactor{}
	cases := map[string]string{
		"":                    "[0B: text]",
		"hello world":         "[11B: text]",
		"https://example.com": "[19B: url]",
		"http://example.com":  "[18B: url]",
		"/ping":               "[5B: command]",
		"!invite":             "[7B: command]",
		"hey /slash":          "[10B: text]",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := r.Body(in); got != want {
				t.Fatalf("Body(%q): want %q, got %q", in, want, got)
			}
		})
	}
}

func TestRedactor_Body_Debug(t *testing.T) {
	r := &Redactor{Debug: true}
	in := "hello world"
	if got := r.Body(in); got != in {
		t.Fatalf("debug mode should pass through, got %q", got)
	}
}
