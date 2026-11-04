package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/trey-copeland/gotdms"
)

func runDefragment(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("defragment", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outPath := fs.String("o", "", "write the defragmented copy here (replacing it); rewrite the input in place when empty")
	lenient := fs.Bool("lenient", false, "recover a file that ends in a torn tail or a final segment past EOF")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: gotdms defragment [-o out.tdms] [--lenient] file.tdms\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := fs.Arg(0)

	var opts []tdms.ReaderOption
	if *lenient {
		opts = append(opts, tdms.WithLenientTail())
	}

	// DefragmentToFile writes through a temporary file and a rename, so -o
	// naming the input (by any spelling, or through a link) is an in-place
	// rewrite, not a truncation of the file being read.
	var err error
	if *outPath == "" {
		err = tdms.DefragmentFile(path, opts...)
	} else {
		err = tdms.DefragmentToFile(*outPath, path, opts...)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
