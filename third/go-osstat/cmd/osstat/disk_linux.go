package main

import (
	"iter"

	"github.com/mackerelio/go-osstat/disk"
)

func diskGenerator() (iter.Seq[value], error) {
	disks, err := disk.Get()
	if err != nil {
		return nil, err
	}
	return func(yield func(value) bool) {
		for _, disk := range disks {
			yield(value{"disk." + disk.Name + ".reads", disk.ReadsCompleted, "-"})
			yield(value{"disk." + disk.Name + ".writes", disk.WritesCompleted, "-"})
		}
	}, nil
}
