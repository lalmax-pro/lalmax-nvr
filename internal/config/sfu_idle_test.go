package config

import (
	"testing"
	"time"
)

func TestSFURoomIdleTimeout(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
		valid bool
	}{{"", 0, true}, {"5m", 5 * time.Minute, true}, {"0s", 0, false}, {"-1s", 0, false}, {"invalid", 0, false}} {
		d, err := (SFUConfig{IdleTimeout: tc.value}).RoomIdleTimeout()
		if (err == nil) != tc.valid || d != tc.want {
			t.Errorf("%q: %v %v", tc.value, d, err)
		}
	}
}
