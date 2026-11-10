//go:build !windows

// Command GoDM is the Windows desktop download manager. On other systems use
// the "godm" command line program instead.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "The GoDM desktop app runs on Windows. On this system use the \"godm-cli\" command (godm-cli serve / godm-cli get).")
	os.Exit(1)
}
