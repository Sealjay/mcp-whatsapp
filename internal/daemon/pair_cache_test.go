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

func TestPairCache_Expired(t *testing.T) {
	c := NewPairCache()
	c.SetQR("abc123")
	c.SetExpired()
	if !c.Expired() {
		t.Fatal("SetExpired must set the expired flag")
	}
	if c.QR() != "" {
		t.Fatalf("SetExpired must drop the stale QR, got %q", c.QR())
	}
	if c.Paired() {
		t.Fatal("SetExpired must keep paired=false")
	}

	c.SetQR("def456")
	if c.Expired() {
		t.Fatal("a new QR must clear the expired flag")
	}

	c.SetExpired()
	c.Reset()
	if c.Expired() {
		t.Fatal("Reset must clear the expired flag")
	}

	c.SetPaired()
	c.SetExpired()
	if c.Expired() || !c.Paired() {
		t.Fatal("SetExpired must be a no-op once paired")
	}
}
