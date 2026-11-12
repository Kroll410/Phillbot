//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// Вмикає UTF-8 (кирилиця) і ANSI-кольори в консолі Windows.
func setupConsole() {
	k := syscall.NewLazyDLL("kernel32.dll")
	k.NewProc("SetConsoleOutputCP").Call(65001)
	k.NewProc("SetConsoleCP").Call(65001)
	getMode, setMode := k.NewProc("GetConsoleMode"), k.NewProc("SetConsoleMode")
	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := getMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
		setMode.Call(uintptr(h), uintptr(mode|0x0004)) // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	}
	k.NewProc("SetConsoleTitleW").Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("СабДей — тестовий бот"))))
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

// Вимикає QuickEdit: інакше клік у вікні консолі заморожує вивід до натискання клавіші.
func disableQuickEdit() {
	k := syscall.NewLazyDLL("kernel32.dll")
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if r, _, _ := k.NewProc("GetConsoleMode").Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
		mode &^= 0x0040 // ENABLE_QUICK_EDIT_MODE
		mode |= 0x0080  // ENABLE_EXTENDED_FLAGS
		k.NewProc("SetConsoleMode").Call(uintptr(h), uintptr(mode))
	}
}
