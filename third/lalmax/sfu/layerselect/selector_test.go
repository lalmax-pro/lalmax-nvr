package layerselect

import (
	"testing"
	"time"
)

func TestSelectorDowngradesImmediatelyAndUpgradesAfterStableHeadroom(t *testing.T) {
	base := time.Unix(100, 0)
	layers := []Layer{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}, {RID: "h", MaxBitrate: 1_500_000}}
	var selector Selector

	if got := selector.Choose("h", 400_000, layers, base); got != "l" {
		t.Fatalf("low estimate selected %q, want l", got)
	}
	if got := selector.Choose("l", 1_000_000, layers, base); got != "l" {
		t.Fatalf("upgrade candidate selected %q before hold, want l", got)
	}
	if got := selector.Choose("l", 1_000_000, layers, base.Add(upgradeStability-time.Millisecond)); got != "l" {
		t.Fatalf("upgrade selected %q before stable interval, want l", got)
	}
	if got := selector.Choose("l", 1_000_000, layers, base.Add(upgradeStability)); got != "m" {
		t.Fatalf("stable estimate selected %q, want m", got)
	}
}

func TestSelectorImmediatelyDowngradesWhenBudgetCannotSupportCurrentLayer(t *testing.T) {
	base := time.Unix(200, 0)
	layers := []Layer{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}, {RID: "h", MaxBitrate: 1_500_000}}
	var selector Selector

	if got := selector.Choose("m", 520_000, layers, base); got != "l" {
		t.Fatalf("insufficient estimate selected %q, want immediate l downgrade", got)
	}
	if got := selector.Choose("h", 1_000_000, layers, base.Add(3*time.Second)); got != "m" {
		t.Fatalf("missing current layer selected %q, want m", got)
	}
}

func TestSelectorFallsBackWhenLayerDisappears(t *testing.T) {
	layers := []Layer{{RID: "l", MaxBitrate: 150_000}, {RID: "m", MaxBitrate: 500_000}}
	var selector Selector
	if got := selector.Choose("h", 2_000_000, layers, time.Now()); got != "m" {
		t.Fatalf("removed layer fallback selected %q, want m", got)
	}
}
