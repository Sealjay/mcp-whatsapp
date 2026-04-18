package daemon

import "testing"

func TestRenderQRPNG_PrefixAndSize(t *testing.T) {
	b, err := renderQRPNG("test payload", 256)
	if err != nil {
		t.Fatalf("renderQRPNG: %v", err)
	}
	if len(b) < 100 {
		t.Fatalf("expected non-trivial PNG, got %d bytes", len(b))
	}
	// PNG magic bytes: 89 50 4E 47 0D 0A 1A 0A
	want := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	for i, x := range want {
		if b[i] != x {
			t.Fatalf("byte %d: want %x, got %x", i, x, b[i])
		}
	}
}

func TestRenderQRPNG_EmptyPayloadErrors(t *testing.T) {
	_, err := renderQRPNG("", 256)
	if err == nil {
		t.Fatal("empty payload must error")
	}
}
