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
		yield(value{"memory.free", memory.Free, "bytes"})
		yield(value{"memory.page_file_total", memory.PageFileTotal, "bytes"})
		yield(value{"memory.page_file_free", memory.PageFileFree, "bytes"})
		yield(value{"memory.virtual_total", memory.VirtualTotal, "bytes"})
		yield(value{"memory.virtual_free", memory.VirtualFree, "bytes"})
	}, nil
}
