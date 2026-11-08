package client

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/util/keys"
)

// restoreDeviceProps snapshots whatsmeow's package-level DeviceProps and
// puts it back when the test ends, so tests that write it stay isolated.
func restoreDeviceProps(t *testing.T) {
	t.Helper()
	saved := proto.Clone(wmstore.DeviceProps).(*waCompanionReg.DeviceProps)
	t.Cleanup(func() { wmstore.DeviceProps = saved })
}

// handshakeProps returns the DeviceProps whatsmeow marshals into the pairing
// handshake of an unpaired device, without connecting.
func handshakeProps(t *testing.T) *waCompanionReg.DeviceProps {
	t.Helper()
	identity := keys.NewKeyPair()
	device := &wmstore.Device{
		NoiseKey:     keys.NewKeyPair(),
		IdentityKey:  identity,
		SignedPreKey: identity.CreateSignedPreKey(1),
	}
	raw := device.GetClientPayload().GetDevicePairingData().GetDeviceProps()
	if len(raw) == 0 {
		t.Fatal("registration payload carries no DeviceProps")
	}
	var sent waCompanionReg.DeviceProps
	if err := proto.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("unmarshal DeviceProps: %v", err)
	}
	return &sent
}

func TestRequestFullHistory_DefaultHandshakeAsksForNone(t *testing.T) {
	restoreDeviceProps(t)
	if handshakeProps(t).GetRequireFullSync() {
		t.Fatal("handshake asks for a full sync without RequestFullHistory")
	}
}

func TestRequestFullHistory_ReachesRegistrationPayload(t *testing.T) {
	restoreDeviceProps(t)
	before := proto.Clone(wmstore.DeviceProps).(*waCompanionReg.DeviceProps)
	RequestFullHistory()

	sent := handshakeProps(t)
	if !sent.GetRequireFullSync() {
		t.Fatal("handshake RequireFullSync = false, want true")
	}
	// Only the switch changes: every other capability whatsmeow advertises stays as shipped.
	sent.RequireFullSync = nil
	before.RequireFullSync = nil
	if !proto.Equal(sent.GetHistorySyncConfig(), before.GetHistorySyncConfig()) {
		t.Fatalf("history sync config changed:\nbefore %v\nafter  %v", before.GetHistorySyncConfig(), sent.GetHistorySyncConfig())
	}
}
