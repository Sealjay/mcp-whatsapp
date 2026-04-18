package client

import (
	"context"
	"strings"
	"testing"
)

func TestBuildVCard_Basic(t *testing.T) {
	got := buildVCard("Alice", "447700900000")
	want := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"FN:Alice\r\n" +
		"N:Alice;;;;\r\n" +
		"TEL;TYPE=CELL;waid=447700900000:+447700900000\r\n" +
		"END:VCARD\r\n"
	if got != want {
		t.Fatalf("buildVCard mismatch.\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildVCard_EmptyPhone(t *testing.T) {
	got := buildVCard("Alice", "")
	if strings.Contains(got, "TEL") {
		t.Fatalf("buildVCard(empty phone) contained TEL line: %q", got)
	}
	if !strings.Contains(got, "FN:Alice\r\n") {
		t.Fatalf("buildVCard(empty phone) missing FN:Alice: %q", got)
	}
	if !strings.Contains(got, "N:Alice;;;;\r\n") {
		t.Fatalf("buildVCard(empty phone) missing N:Alice;;;;: %q", got)
	}
	if !strings.HasPrefix(got, "BEGIN:VCARD\r\n") {
		t.Fatalf("buildVCard missing BEGIN:VCARD header: %q", got)
	}
	if !strings.HasSuffix(got, "END:VCARD\r\n") {
		t.Fatalf("buildVCard missing END:VCARD footer: %q", got)
	}
}

func TestBuildVCard_EmptyName(t *testing.T) {
	got := buildVCard("", "447700900000")
	// Phone-only fallback: FN/N contain +<digits>.
	if !strings.Contains(got, "FN:+447700900000\r\n") {
		t.Fatalf("buildVCard(empty name) missing FN:+<phone>: %q", got)
	}
	if !strings.Contains(got, "TEL;TYPE=CELL;waid=447700900000:+447700900000\r\n") {
		t.Fatalf("buildVCard(empty name) missing TEL line: %q", got)
	}
}

func TestBuildVCard_EscapesSpecials(t *testing.T) {
	// Name with every special char the RFC 6350 text-value grammar reserves,
	// plus a CR and LF.
	got := buildVCard("a,b;c\\d\ne\rf", "")
	// Check that escaping produced the right sequence in the FN line.
	// Expected inside FN: a\,b\;c\\d\ne f  (CR is stripped entirely, LF -> \n)
	wantFN := "FN:a\\,b\\;c\\\\d\\nef\r\n"
	if !strings.Contains(got, wantFN) {
		t.Fatalf("buildVCard escaping wrong.\n got: %q\nwant substring: %q", got, wantFN)
	}
	// And the N line: same escaped form followed by ";;;;".
	wantN := "N:a\\,b\\;c\\\\d\\nef;;;;\r\n"
	if !strings.Contains(got, wantN) {
		t.Fatalf("buildVCard N escaping wrong.\n got: %q\nwant substring: %q", got, wantN)
	}
}

func TestSendPoll_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	r := c.SendPoll(context.Background(), "447700000001", "Q?", []string{"a", "b"}, 1)
	if r.Success {
		t.Fatalf("expected Success=false, got %+v", r)
	}
	if r.Message != "Not connected to WhatsApp" {
		t.Fatalf("expected not-connected message, got %q", r.Message)
	}
}

func TestSendPoll_TooFewOptions(t *testing.T) {
	// The options-count guard runs before the connection gate so we can
	// exercise it without a live whatsmeow client.
	c := newDisconnectedClient()
	r := c.SendPoll(context.Background(), "447700000001", "Q?", []string{"only"}, 1)
	if r.Success {
		t.Fatalf("expected Success=false, got %+v", r)
	}
	if !strings.Contains(r.Message, "at least 2") {
		t.Fatalf("expected 'at least 2' in error, got %q", r.Message)
	}
}

func TestSendContactCard_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	r := c.SendContactCard(context.Background(), "447700000001", "Alice", "447700900000", "")
	if r.Success {
		t.Fatalf("expected Success=false, got %+v", r)
	}
	if r.Message != "Not connected to WhatsApp" {
		t.Fatalf("expected not-connected message, got %q", r.Message)
	}
}

func TestSendPollVote_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	r := c.SendPollVote(context.Background(), "447700000001@s.whatsapp.net", "POLL1", []string{"a"})
	if r.Success {
		t.Fatalf("expected Success=false, got %+v", r)
	}
	if r.Message != "Not connected to WhatsApp" {
		t.Fatalf("expected not-connected message, got %q", r.Message)
	}
}

func TestGetPollResults_NotConnected(t *testing.T) {
	// GetPollResults hits the local store only (no whatsmeow). The guard we
	// exercise here is input validation + the nil-store fallback.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("GetPollResults on disconnected client panicked: %v", r)
		}
	}()
	c := newDisconnectedClient()
	if _, err := c.GetPollResults(context.Background(), "", "POLL1"); err == nil {
		t.Fatalf("expected error for empty chat_jid, got nil")
	}
	if _, err := c.GetPollResults(context.Background(), "447700000001@s.whatsapp.net", ""); err == nil {
		t.Fatalf("expected error for empty poll_message_id, got nil")
	}
	// With both args set but no store, must error cleanly instead of panicking.
	if _, err := c.GetPollResults(context.Background(), "447700000001@s.whatsapp.net", "POLL1"); err == nil {
		t.Fatalf("expected error for nil store, got nil")
	}
}
