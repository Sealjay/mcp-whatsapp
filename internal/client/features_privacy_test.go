package client

import (
	"context"
	"strings"
	"testing"
)

func TestSendPresence_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SendPresence(context.Background(), "available")
	assertNotConnected(t, err)
}

func TestSendPresence_InvalidState(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SendPresence(context.Background(), "foo")
	if err == nil {
		t.Fatal("expected error for invalid state")
	}
	msg := err.Error()
	for _, want := range []string{"available", "unavailable"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing allowed value %q", msg, want)
		}
	}
}

func TestSetPrivacySetting_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.SetPrivacySetting(context.Background(), "groupadd", "all")
	assertNotConnected(t, err)
}

func TestSetPrivacySetting_InvalidName(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.SetPrivacySetting(context.Background(), "notAThing", "all")
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
	msg := err.Error()
	for _, want := range []string{
		"groupadd", "last", "status", "profile", "readreceipts",
		"online", "calladd", "messages", "defense", "stickers",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing allowed name %q", msg, want)
		}
	}
	// The value list should NOT appear when the name is the offending arg.
	if strings.Contains(msg, "contact_allowlist") {
		t.Errorf("error %q leaked value list while rejecting a bad name", msg)
	}
}

func TestSetPrivacySetting_InvalidValue(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.SetPrivacySetting(context.Background(), "groupadd", "huh")
	if err == nil {
		t.Fatal("expected error for invalid value")
	}
	msg := err.Error()
	for _, want := range []string{
		"all", "contacts", "contact_allowlist", "contact_blacklist",
		"match_last_seen", "known", "none", "on_standard", "off",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing allowed value %q", msg, want)
		}
	}
}

func TestSetStatusMessage_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetStatusMessage(context.Background(), "hello")
	assertNotConnected(t, err)
}

func TestGetPrivacySettings_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetPrivacySettings(context.Background())
	assertNotConnected(t, err)
}
