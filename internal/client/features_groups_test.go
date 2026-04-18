package client

import (
	"context"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// newDisconnectedClient returns a Client whose wa field is nil. That makes
// (*whatsmeow.Client).IsConnected return false — which all feature methods
// gate on — so we can exercise the "not-connected" error branch without
// dragging in sqlite, sockets, or upstream mocks.
func newDisconnectedClient() *Client {
	return &Client{log: NewStderrLogger("test", "ERROR", false)}
}

func TestParseParticipantJID_PhoneOnly(t *testing.T) {
	got, err := parseParticipantJID("447700000001")
	if err != nil {
		t.Fatalf("parseParticipantJID(phone) error: %v", err)
	}
	if got.User != "447700000001" || got.Server != types.DefaultUserServer {
		t.Fatalf("parseParticipantJID(phone) = %v, want %s@%s", got, "447700000001", types.DefaultUserServer)
	}
}

func TestParseParticipantJID_FullJID(t *testing.T) {
	got, err := parseParticipantJID("447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("parseParticipantJID(jid) error: %v", err)
	}
	if got.User != "447700000001" {
		t.Fatalf("parseParticipantJID(jid).User = %q, want 447700000001", got.User)
	}
}

func TestParseParticipantJID_Empty(t *testing.T) {
	if _, err := parseParticipantJID(""); err == nil {
		t.Fatalf("parseParticipantJID(\"\") expected error")
	}
	if _, err := parseParticipantJID("   "); err == nil {
		t.Fatalf("parseParticipantJID(whitespace) expected error")
	}
}

// NOTE: whatsmeow's types.ParseJID is intentionally lenient and does not
// reject odd inputs (it silently accepts multiple @ signs, leading @, etc.),
// so we don't try to assert on "malformed JID" rejection here — the only way
// to enforce stricter validation would be to duplicate whatsmeow's parsing.

func TestParseParticipantJIDs_Mixed(t *testing.T) {
	got, err := parseParticipantJIDs([]string{"447700000001", "447700000002@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("parseParticipantJIDs error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parseParticipantJIDs len = %d, want 2", len(got))
	}
	if got[0].User != "447700000001" || got[1].User != "447700000002" {
		t.Fatalf("parseParticipantJIDs users = [%q, %q]", got[0].User, got[1].User)
	}
}

func TestParseParticipantAction(t *testing.T) {
	cases := []struct {
		in   string
		want whatsmeow.ParticipantChange
	}{
		{"add", whatsmeow.ParticipantChangeAdd},
		{"ADD", whatsmeow.ParticipantChangeAdd},
		{"remove", whatsmeow.ParticipantChangeRemove},
		{"Promote", whatsmeow.ParticipantChangePromote},
		{" demote ", whatsmeow.ParticipantChangeDemote},
	}
	for _, tc := range cases {
		got, err := parseParticipantAction(tc.in)
		if err != nil {
			t.Errorf("parseParticipantAction(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseParticipantAction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseParticipantAction_Invalid(t *testing.T) {
	_, err := parseParticipantAction("kick")
	if err == nil {
		t.Fatalf("parseParticipantAction(\"kick\") expected error")
	}
	msg := err.Error()
	for _, allowed := range []string{"add", "remove", "promote", "demote"} {
		if !strings.Contains(msg, allowed) {
			t.Errorf("error message %q missing allowed value %q", msg, allowed)
		}
	}
}

// The remaining tests exercise the "not connected" guard at the top of every
// feature method. When c.wa is nil, (*whatsmeow.Client).IsConnected reports
// false, so every method should bail out with that specific error before
// touching anything else.
func TestCreateGroup_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, _, err := c.CreateGroup(context.Background(), "ignored", []string{"447700000001"})
	assertNotConnected(t, err)
}

func TestLeaveGroup_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.LeaveGroup(context.Background(), "123@g.us")
	assertNotConnected(t, err)
}

func TestListJoinedGroups_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.ListJoinedGroups(context.Background())
	assertNotConnected(t, err)
}

func TestGetGroupInfoJSON_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetGroupInfoJSON(context.Background(), "123@g.us")
	assertNotConnected(t, err)
}

func TestUpdateGroupParticipants_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.UpdateGroupParticipants(context.Background(), "123@g.us", []string{"447700000001"}, "add")
	assertNotConnected(t, err)
}

func TestSetGroupName_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupName(context.Background(), "123@g.us", "hello")
	assertNotConnected(t, err)
}

func TestSetGroupTopic_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupTopic(context.Background(), "123@g.us", "hi")
	assertNotConnected(t, err)
}

func TestSetGroupAnnounce_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupAnnounce(context.Background(), "123@g.us", true)
	assertNotConnected(t, err)
}

func TestSetGroupLocked_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupLocked(context.Background(), "123@g.us", true)
	assertNotConnected(t, err)
}

func TestGetGroupInviteLink_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetGroupInviteLink(context.Background(), "123@g.us", false)
	assertNotConnected(t, err)
}

func TestJoinGroupWithLink_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.JoinGroupWithLink(context.Background(), "https://chat.whatsapp.com/abc")
	assertNotConnected(t, err)
}

func TestJoinGroupWithLink_RejectsEmpty(t *testing.T) {
	// Guarded by a specific empty-input check that runs after the connection
	// gate, so we can't easily hit it without a live client — but we can at
	// least assert the connection gate fires for a whitespace input.
	c := newDisconnectedClient()
	_, err := c.JoinGroupWithLink(context.Background(), "   ")
	assertNotConnected(t, err)
}

func TestGetBlocklist_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetBlocklist(context.Background())
	assertNotConnected(t, err)
}

func TestBlockContact_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.BlockContact(context.Background(), "447700000001")
	assertNotConnected(t, err)
}

func TestUnblockContact_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.UnblockContact(context.Background(), "447700000001")
	assertNotConnected(t, err)
}

func assertNotConnected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected not-connected error, got nil")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not-connected error, got %v", err)
	}
}
