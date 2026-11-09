//go:build !windows

package main

import (
	"iter"

	"github.com/mackerelio/go-osstat/memory"
)

func memoryGenerator() (iter.Seq[value], error) {
	memory, err := memory.Get()
	if err != nil {
		return nil, err
	}
	return func(yield func(value) bool) {
		yield(value{"memory.total", memory.Total, "bytes"})
		yield(value{"memory.used", memory.Used, "bytes"})
		yield(value{"memory.cached", memory.Cached, "bytes"})
		yield(value{"memory.free", memory.Free, "bytes"})
		yield(value{"memory.active", memory.Active, "bytes"})
		yield(value{"memory.inactive", memory.Inactive, "bytes"})
		yield(value{"memory.swap_total", memory.SwapTotal, "bytes"})
		yield(value{"memory.swap_used", memory.SwapUsed, "bytes"})
		yield(value{"memory.swap_free", memory.SwapFree, "bytes"})
	}, nil
}
