package main

import (
	"iter"

	"github.com/mackerelio/go-osstat/uptime"
)

func uptimeGenerator() (iter.Seq[value], error) {
	uptime, err := uptime.Get()
	if err != nil {
		return nil, err
	}
	return func(yield func(value) bool) {
		yield(value{"uptime", float64(uptime.Nanoseconds()) / 1e9, "seconds"})
	}, nil
}
