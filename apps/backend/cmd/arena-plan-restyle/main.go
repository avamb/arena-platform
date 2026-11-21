// Command arena-plan-restyle converts Bil24 sbt seating plans of the Forzum
// library into arena's one house style. See ops/forzum-library/README.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("arena-plan-restyle", flag.ContinueOnError)
	defLib := os.Getenv("FORZUM_LIBRARY_DIR")
	if defLib == "" {
		defLib = `C:\Arena\forzum-library`
	}
	lib := fs.String("lib", defLib, "library folder (plans are read from <lib>/plans)")
	plan := fs.String("plan", "", "convert one plan folder instead of the whole library")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var dirs []string
	if *plan != "" {
		dirs = []string{*plan}
	} else {
		found, err := FindPlans(filepath.Join(*lib, "plans"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			return 1
		}
		dirs = found
	}

	counts := map[string]int{}
	ioErrors := 0
	for _, d := range dirs {
		rep, err := ConvertPlan(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", d, err)
			ioErrors++
			continue
		}
		counts[rep.Status]++
		fmt.Printf("%s\t%s\n", rep.Status, d)
	}
	fmt.Printf("ok=%d needs_review=%d failed=%d skipped=%d errors=%d\n",
		counts[StatusOK], counts[StatusNeedsReview], counts[StatusFailed], counts[StatusSkipped], ioErrors)
	if ioErrors > 0 {
		return 1
	}
	return 0
}
