package daemon

import "testing"

func TestPairCache_Lifecycle(t *testing.T) {
	c := NewPairCache()
	if c.Paired() {
		t.Fatal("new cache must be unpaired")
	}
	if c.QR() != "" {
		t.Fatalf("new cache must have empty QR, got %q", c.QR())
	}

	c.SetQR("abc123")
	if c.Paired() {
		t.Fatal("SetQR must keep paired=false")
	}
	if c.QR() != "abc123" {
		t.Fatalf("QR: want abc123, got %q", c.QR())
	}

	c.SetPaired()
	if !c.Paired() {
		t.Fatal("after SetPaired, Paired() must be true")
	}
	if c.QR() != "" {
		t.Fatalf("SetPaired must clear QR, got %q", c.QR())
	}

	c.Reset()
	if c.Paired() {
		t.Fatal("Reset must clear paired flag")
	}
	if c.QR() != "" {
		t.Fatalf("Reset must clear QR, got %q", c.QR())
	}
}
