package client

import (
	"bytes"
	"strings"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/security"
)

// Identifiers and content that must never reach non-debug logs.
const (
	logTestDMUser      = "447700900123"
	logTestDMJID       = logTestDMUser + "@s.whatsapp.net"
	logTestDMName      = "Alice Example"
	logTestGroupUser   = "120363000000000042"
	logTestGroupJID    = logTestGroupUser + "@g.us"
	logTestGroupName   = "Secret Project Group"
	logTestDocName     = "Alice passport scan.pdf"
	logTestMessageBody = "meet at the usual place"
)

// newLogCapturingClient returns a client whose logger writes every level,
// including Debug, into the returned buffer.
func newLogCapturingClient(t *testing.T, debug bool) (*Client, *bytes.Buffer) {
	t.Helper()
	s := newTestStoreWithLIDMap(t, nil)
	var buf bytes.Buffer
	return &Client{
		store:    s,
		log:      &stderrLogger{out: &buf, mod: "test", min: logLevelToInt["DEBUG"]},
		redactor: &security.Redactor{Debug: debug},
	}, &buf
}

// driveNameResolution exercises GetChatName's cache-hit path (a direct chat)
// and its history-sync path (a group named by the conversation metadata).
// Neither path touches the nil whatsmeow client.
func driveNameResolution(t *testing.T, c *Client) {
	t.Helper()
	if err := c.store.StoreChat(logTestDMJID, logTestDMName, time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if got := c.GetChatName(types.NewJID(logTestDMUser, types.DefaultUserServer), logTestDMJID, nil, ""); got != logTestDMName {
		t.Fatalf("GetChatName(DM) = %q, want %q", got, logTestDMName)
	}
	conv := &waHistorySync.Conversation{
		ID:          proto.String(logTestGroupJID),
		DisplayName: proto.String(logTestGroupName),
	}
	if got := c.GetChatName(types.NewJID(logTestGroupUser, types.GroupServer), logTestGroupJID, conv, ""); got != logTestGroupName {
		t.Fatalf("GetChatName(group) = %q, want %q", got, logTestGroupName)
	}
}

// driveDocumentMessage feeds handleMessage a live document message in the
// seeded direct chat.
func driveDocumentMessage(t *testing.T, c *Client) {
	t.Helper()
	chat := types.NewJID(logTestDMUser, types.DefaultUserServer)
	c.handleMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "log-doc-1",
			Timestamp:     time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			DocumentMessage: &waProto.DocumentMessage{FileName: proto.String(logTestDocName)},
		},
	})
	c.handleMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "log-text-1",
			Timestamp:     time.Date(2026, 2, 1, 10, 1, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String(logTestMessageBody)},
	})
}

func assertLogExcludes(t *testing.T, logs string, values ...string) {
	t.Helper()
	for _, v := range values {
		if strings.Contains(logs, v) {
			t.Errorf("log output contains %q:\n%s", v, logs)
		}
	}
}

func assertLogIncludes(t *testing.T, logs string, values ...string) {
	t.Helper()
	for _, v := range values {
		if !strings.Contains(logs, v) {
			t.Errorf("log output is missing %q:\n%s", v, logs)
		}
	}
}

func TestEventLogs_RedactNamesAndJIDs(t *testing.T) {
	c, buf := newLogCapturingClient(t, false)
	driveNameResolution(t, c)
	driveDocumentMessage(t, c)
	logs := buf.String()

	assertLogExcludes(t, logs,
		logTestDMUser, logTestDMName,
		logTestGroupUser, logTestGroupName,
		logTestDocName, "Alice", logTestMessageBody,
	)
	// The redacted forms keep the lines useful.
	assertLogIncludes(t, logs,
		"Using existing chat name for …0123: [13B: text]",
		"Using group name for …0042: [20B: text]",
		"[document: [23B: text]]",
	)
}

func TestEventLogs_DebugShowsNames(t *testing.T) {
	c, buf := newLogCapturingClient(t, true)
	driveNameResolution(t, c)
	driveDocumentMessage(t, c)
	logs := buf.String()

	assertLogIncludes(t, logs,
		logTestDMName, logTestGroupName,
		logTestDocName, logTestMessageBody,
	)
	// Phone-number-shaped JID user parts stay masked even in debug mode
	// (Redactor.JID treats long group IDs the same way).
	assertLogExcludes(t, logs, logTestDMUser, logTestGroupUser)
	assertLogIncludes(t, logs, "*****00123@s.whatsapp.net", "*****00042@g.us")
}
