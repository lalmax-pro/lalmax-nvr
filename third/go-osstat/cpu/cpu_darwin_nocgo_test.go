//go:build darwin && !cgo

package cpu

import (
	"testing"
	"time"
)

func TestGetCPUNoCgo(t *testing.T) {
	first, err := Get()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	second, err := Get()
	if err != nil {
		t.Fatal(err)
	}
	if first.Total == 0 || second.Total <= first.Total {
		t.Fatalf("CPU counters did not advance: first=%+v second=%+v", first, second)
	}
}
