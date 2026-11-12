//go:build windows

package main

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

const createNoWindow = 0x08000000

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

func openURL(u string) {
	_ = hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", u)).Start()
}

func marblesRunning() bool {
	out, err := hidden(exec.Command("tasklist", "/FO", "CSV", "/NH")).Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "marbles")
}

// Копіює текст у буфер обміну Windows (Unicode).
func setClipboard(text string) bool {
	u := syscall.NewLazyDLL("user32.dll")
	k := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := u.NewProc("OpenClipboard").Call(0); r == 0 {
		return false
	}
	defer u.NewProc("CloseClipboard").Call()
	u.NewProc("EmptyClipboard").Call()
	data, _ := syscall.UTF16FromString(text)
	size := uintptr(len(data) * 2)
	h, _, _ := k.NewProc("GlobalAlloc").Call(0x0002, size) // GMEM_MOVEABLE
	if h == 0 {
		return false
	}
	p, _, _ := k.NewProc("GlobalLock").Call(h)
	if p == 0 {
		return false
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(data))
	copy(dst, data)
	k.NewProc("GlobalUnlock").Call(h)
	r, _, _ := u.NewProc("SetClipboardData").Call(13, h) // CF_UNICODETEXT
	return r != 0
}
