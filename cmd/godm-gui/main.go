//go:build windows

// Command GoDM is the Windows desktop download manager.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/lxn/walk"

	"godm/internal/config"
	"godm/internal/gui"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			report(fmt.Sprintf("%v\n\n%s", r, debug.Stack()))
		}
	}()
	if err := gui.Run(os.Args[1:]); err != nil {
		report(err.Error())
	}
}

func report(msg string) {
	path := filepath.Join(config.Home(), "crash.log")
	os.MkdirAll(config.Home(), 0o700)
	os.WriteFile(path, []byte(msg), 0o600)
	short := msg
	if lines := strings.SplitN(msg, "\n", 6); len(lines) > 5 {
		short = strings.Join(lines[:5], "\n") + "\n..."
	}
	walk.MsgBox(nil, "GoDM", "GoDM stopped because of an error:\n\n"+short+"\n\nDetails were saved to:\n"+path, walk.MsgBoxIconError)
}
