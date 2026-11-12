//go:build !windows

package main

func setupConsole() {}

func setClipboard(string) bool { return false }
func disableQuickEdit()        {}
