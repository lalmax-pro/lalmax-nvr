//go:build !windows

package main

import (
	"iter"

	"github.com/mackerelio/go-osstat/loadavg"
)

func loadavgGenerator() (iter.Seq[value], error) {
	loadavg, err := loadavg.Get()
	if err != nil {
		return nil, err
	}
	return func(yield func(value) bool) {
		yield(value{"loadavg.1m", loadavg.Loadavg1, "-"})
		yield(value{"loadavg.5m", loadavg.Loadavg5, "-"})
		yield(value{"loadavg.15m", loadavg.Loadavg15, "-"})
	}, nil
}
