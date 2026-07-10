package main

import (
	"fmt"
	"os"

	"github.com/Fullstop000/unio/internal/supportmatrix"
)

func main() {
	markdown, err := supportmatrix.Markdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile("docs/API_SUPPORT.md", markdown, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
