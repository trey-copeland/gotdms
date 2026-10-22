package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/trey-copeland/gotdms"
)

func runIndex(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "rebuild every sidecar")
	check := fs.Bool("check", false, "validate existing sidecars without writing")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: gotdms index [--force] [--check] file.tdms...\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *force && *check {
		fmt.Fprintln(stderr, "gotdms index: --check does not write, so it cannot be combined with --force")
		fs.Usage()
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	var paths []string
	failed := false
	for _, arg := range fs.Args() {
		got, err := expandIndexArg(arg)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", arg, err)
			failed = true
			continue
		}
		paths = append(paths, got...)
	}
	for _, path := range paths {
		if err := indexOne(path, *force, *check, stdout, stderr); err != nil {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func indexOne(path string, force, check bool, stdout, stderr io.Writer) error {
	if check {
		side := tdms.SegmentIndexPath(path)
		if _, err := os.Stat(side); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(stderr, "%s: missing sidecar\n", path)
				return err
			}
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return err
		}
		ok, err := tdms.SegmentIndexValid(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return err
		}
		if !ok {
			fmt.Fprintf(stderr, "%s: sidecar does not match\n", path)
			return fmt.Errorf("sidecar does not match")
		}
		fmt.Fprintf(stdout, "%s: ok\n", path)
		return nil
	}
	if !force {
		ok, err := tdms.SegmentIndexValid(path)
		if err == nil && ok {
			fmt.Fprintf(stdout, "%s: current\n", path)
			return nil
		}
	}
	side, err := tdms.BuildSegmentIndexFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", path, err)
		return err
	}
	fmt.Fprintf(stdout, "%s: wrote %s\n", path, side)
	return nil
}

func expandIndexArg(arg string) ([]string, error) {
	fi, err := os.Stat(arg)
	if err == nil {
		if !fi.IsDir() {
			return []string{arg}, nil
		}
		return tdmsInDir(arg)
	}
	if !os.IsNotExist(err) || !hasGlobMeta(arg) {
		return nil, err
	}
	matches, gerr := filepath.Glob(arg)
	if gerr != nil {
		return nil, gerr
	}
	var out []string
	for _, m := range matches {
		st, serr := os.Stat(m)
		if serr != nil || !st.Mode().IsRegular() {
			continue
		}
		if strings.EqualFold(filepath.Ext(m), ".tdms") {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no .tdms files")
	}
	return out, nil
}

func tdmsInDir(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".tdms") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no .tdms files")
	}
	return out, nil
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}
