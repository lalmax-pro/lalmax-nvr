//go:build linux || darwin || freebsd || netbsd

package main

import (
	"iter"

	"github.com/mackerelio/go-osstat/network"
)

func networkGenerator() (iter.Seq[value], error) {
	networks, err := network.Get()
	if err != nil {
		return nil, err
	}
	return func(yield func(value) bool) {
		for _, network := range networks {
			yield(value{"network." + network.Name + ".rx_bytes", network.RxBytes, "bytes"})
			yield(value{"network." + network.Name + ".tx_bytes", network.TxBytes, "bytes"})
		}
	}, nil
}
