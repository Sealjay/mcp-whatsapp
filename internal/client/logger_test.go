package client

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sealjay/mcp-whatsapp/internal/security"
)

// whatsmeow logs full JIDs in its own lines, for example at WARN in
// message.go: "Error unmarshaling plaintext message from <chat> (<sender>)".
// The logger newWAClient hands to whatsmeow masks them in both modes.
func TestNewWAClient_MasksJIDsInWhatsmeowLogs(t *testing.T) {
	for _, debug := range []bool{false, true} {
		container, err := sqlstore.New(context.Background(), "sqlite3",
			"file:"+filepath.Join(t.TempDir(), "whatsapp.db")+"?_foreign_keys=on", waLog.Noop)
		if err != nil {
			t.Fatalf("sqlstore.New: %v", err)
		}
		t.Cleanup(func() { _ = container.Close() })

		var buf bytes.Buffer
		logger := &stderrLogger{out: &buf, mod: "Client", min: logLevelToInt["INFO"]}
		wa := newWAClient(container.NewDevice(), logger, &security.Redactor{Debug: debug})

		source := logTestGroupJID + " (" + logTestDMUser + ":12@s.whatsapp.net, 123456789012345@lid)"
		wa.Log.Warnf("Error unmarshaling plaintext message from %s: %v", source, errors.New("proto: cannot parse"))
		wa.Log.Sub("Recv").Infof("Got receipt from %s", logTestDMJID)

		out := buf.String()
		for _, leak := range []string{logTestDMUser, logTestGroupUser, "123456789012345"} {
			if strings.Contains(out, leak) {
				t.Errorf("debug=%v: whatsmeow log leaks %q:\n%s", debug, leak, out)
			}
		}
		for _, want := range []string{"[Client WARN] Error unmarshaling plaintext message from ****00042@g.us (****00123:12@s.whatsapp.net, ****12345@lid): proto: cannot parse", "[Client/Recv INFO] Got receipt from ****00123@s.whatsapp.net"} {
			if !strings.Contains(out, want) {
				t.Errorf("debug=%v: log missing %q:\n%s", debug, want, out)
			}
		}
	}
}
