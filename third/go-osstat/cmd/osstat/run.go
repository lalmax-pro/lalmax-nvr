package main

import (
	"fmt"
	"io"
	"iter"
)

type generator func() (iter.Seq[value], error)

type value struct {
	name  string
	value any
	unit  string
}

func run(args []string, out io.Writer) []error {
	var errs []error
	for _, gen := range generators {
		values, err := gen()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for v := range values {
			fmt.Fprintf(out, "%-25s %-14v %s\n", v.name, v.value, v.unit) // nolint
		}
	}
	return errs
}
