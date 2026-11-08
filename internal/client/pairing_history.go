package client

import (
	"google.golang.org/protobuf/proto"

	wmstore "go.mau.fi/whatsmeow/store"
)

// RequestFullHistory makes this device ask the primary phone for a full
// history sync when it links, instead of the recent window (about three
// months) the phone sends by default. whatsmeow serialises its package-level
// store.DeviceProps into every pairing handshake (QR and phone-number code
// alike), so call this once at startup, before the first Connect. It has no
// effect on a device that is already linked, and the phone decides what it
// actually sends.
func RequestFullHistory() {
	wmstore.DeviceProps.RequireFullSync = proto.Bool(true)
}
