package recorder

import "testing"

func TestPrerollRingKeepsRecentGOPs(t *testing.T) {
	r := newPrerollRing(2)
	r.Push([]byte{0, 0, 0, 1, 0x65, 1}, true)
	r.Push([]byte{0, 0, 0, 1, 0x41, 2}, false)
	r.Push([]byte{0, 0, 0, 1, 0x65, 3}, true)
	r.Push([]byte{0, 0, 0, 1, 0x41, 4}, false)
	r.Push([]byte{0, 0, 0, 1, 0x65, 5}, true)
	got := r.Drain()
	if len(got) < 2 {
		t.Fatalf("expected preroll frames, got %d", len(got))
	}
	if got[0][5] == 1 {
		t.Fatalf("oldest GOP should have been dropped")
	}
	if len(r.Drain()) != 0 {
		t.Fatal("drain should empty the ring")
	}
}
