//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func openURL(u string) {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", u).Start()
		return
	}
	_ = exec.Command("xdg-open", u).Start()
}

func marblesRunning() bool          { return false }
func setClipboard(text string) bool { return false }
