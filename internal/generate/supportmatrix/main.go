// Package main generates the checked-in Agent API support matrix.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Fullstop000/unio/internal/supportmatrix"
)

func main() {
	output := flag.String("output", "docs/API_SUPPORT.md", "path to the generated support matrix")
	flag.Parse()

	markdown, err := supportmatrix.Markdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, markdown, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
