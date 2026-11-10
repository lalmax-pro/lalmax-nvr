package config

import "testing"

func TestSFUPacingRequiresEstimator(t *testing.T) {
	for _, tc := range []struct {
		cfg   SFUConfig
		valid bool
	}{
		{SFUConfig{}, true},
		{SFUConfig{ObserveBandwidth: true}, true},
		{SFUConfig{ObserveBandwidth: true, Pacing: true}, true},
		{SFUConfig{Pacing: true}, false},
	} {
		if err := tc.cfg.ValidatePacing(); (err == nil) != tc.valid {
			t.Errorf("%+v: %v", tc.cfg, err)
		}
	}
}
