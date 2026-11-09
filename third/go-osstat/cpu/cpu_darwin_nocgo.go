//go:build darwin && !cgo

package cpu

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Stats represents CPU statistics for Darwin.
type Stats struct {
	User, System, Idle, Nice, Total uint64
}

var darwinCPU struct {
	sync.Once
	host       uint32
	statistics func(uint32, int32, *[4]uint32, *uint32) int32
	err        error
}

func initDarwinCPU() {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		darwinCPU.err = err
		return
	}
	hostSelf, err := purego.Dlsym(lib, "mach_host_self")
	if err != nil {
		darwinCPU.err = err
		return
	}
	statistics, err := purego.Dlsym(lib, "host_statistics")
	if err != nil {
		darwinCPU.err = err
		return
	}
	var machHostSelf func() uint32
	purego.RegisterFunc(&machHostSelf, hostSelf)
	purego.RegisterFunc(&darwinCPU.statistics, statistics)
	// Reuse the host port for the life of the process.
	darwinCPU.host = machHostSelf()
}

// Get returns the host CPU counters using Mach without requiring cgo.
func Get() (*Stats, error) {
	darwinCPU.Do(initDarwinCPU)
	if darwinCPU.err != nil {
		return nil, fmt.Errorf("load Mach CPU statistics: %w", darwinCPU.err)
	}
	var ticks [4]uint32
	count := uint32(len(ticks))
	if ret := darwinCPU.statistics(darwinCPU.host, 3, &ticks, &count); ret != 0 {
		return nil, fmt.Errorf("host_statistics failed: %d", ret)
	}
	if count < uint32(len(ticks)) {
		return nil, fmt.Errorf("host_statistics returned %d CPU counters", count)
	}
	cpu := &Stats{User: uint64(ticks[0]), System: uint64(ticks[1]), Idle: uint64(ticks[2]), Nice: uint64(ticks[3])}
	cpu.Total = cpu.User + cpu.System + cpu.Idle + cpu.Nice
	return cpu, nil
}
