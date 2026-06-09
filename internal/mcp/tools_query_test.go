package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// fakePairCache is a minimal pairingCache for exercising pairing_status
// without standing up the daemon's real PairCache.
type fakePairCache struct {
	paired bool
	qr     string
}

func (f fakePairCache) Paired() bool { return f.paired }
func (f fakePairCache) QR() string   { return f.qr }

// callPairingStatus invokes the pairing_status handler and decodes its JSON
// "setup_state" envelope into a map.
func callPairingStatus(t *testing.T, s *Server) map[string]any {
	t.Helper()
	tool, ok := s.MCP().ListTools()["pairing_status"]
	if !ok || tool == nil || tool.Handler == nil {
		t.Fatal("pairing_status not registered")
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("pairing_status handler: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("pairing_status returned no content")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("pairing_status content not text: %T", res.Content[0])
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(text.Text), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", text.Text, err)
	}
	if env["type"] != "setup_state" {
		t.Errorf("type = %v, want setup_state", env["type"])
	}
	return env
}

// A nil pairing cache (smoke/tests) yields the error envelope without touching
// the WhatsApp client — so passing a nil client here must not panic.
func TestPairingStatus_NilCache(t *testing.T) {
	env := callPairingStatus(t, NewServer(nil, nil))
	if env["state"] != "error" {
		t.Errorf("state = %v, want error", env["state"])
	}
	if env["detail"] != "pairing cache unavailable" {
		t.Errorf("detail = %v, want pairing cache unavailable", env["detail"])
	}
}

// An unpaired cache with a pending QR yields awaiting_qr carrying the payload.
func TestPairingStatus_AwaitingQRWithPayload(t *testing.T) {
	env := callPairingStatus(t, NewServer(nil, fakePairCache{qr: "2@QRPAYLOAD"}))
	if env["state"] != "awaiting_qr" {
		t.Fatalf("state = %v, want awaiting_qr", env["state"])
	}
	if env["qr_payload"] != "2@QRPAYLOAD" {
		t.Errorf("qr_payload = %v, want 2@QRPAYLOAD", env["qr_payload"])
	}
	if env["detail"] != "Scan with WhatsApp → Linked devices" {
		t.Errorf("detail = %v, want the scan instruction", env["detail"])
	}
}

// An unpaired cache with no QR yet yields the generating-code envelope and
// omits qr_payload.
func TestPairingStatus_AwaitingQRGenerating(t *testing.T) {
	env := callPairingStatus(t, NewServer(nil, fakePairCache{}))
	if env["state"] != "awaiting_qr" {
		t.Fatalf("state = %v, want awaiting_qr", env["state"])
	}
	if _, ok := env["qr_payload"]; ok {
		t.Errorf("qr_payload should be absent, got %v", env["qr_payload"])
	}
	if env["detail"] != "Generating pairing code…" {
		t.Errorf("detail = %v, want generating message", env["detail"])
	}
}
