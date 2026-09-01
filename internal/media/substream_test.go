package media

import "testing"

func TestSubStreamIDHelpers(t *testing.T) {
	id := SubStreamID("cam-1")
	if id != "cam-1_sub" {
		t.Fatalf("got %q", id)
	}
	if !IsSubStreamID(id) {
		t.Fatal("expected sub stream")
	}
	if IsSubStreamID("cam-1") {
		t.Fatal("main stream must not match")
	}
	if MainStreamID(id) != "cam-1" {
		t.Fatalf("main=%q", MainStreamID(id))
	}
}
