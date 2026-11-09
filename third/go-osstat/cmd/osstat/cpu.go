//go:build linux || (darwin && cgo)

package main

import (
	"iter"

	"github.com/mackerelio/go-osstat/cpu"
)

func cpuGenerator() (iter.Seq[value], error) {
	cpu, err := cpu.Get()
	if err != nil {
		return nil, err
	}
	return func(yield func(value) bool) {
		yield(value{"cpu.user", cpu.User, "-"})
		yield(value{"cpu.system", cpu.System, "-"})
		yield(value{"cpu.idle", cpu.Idle, "-"})
		yield(value{"cpu.total", cpu.Total, "-"})
	}, nil
}
