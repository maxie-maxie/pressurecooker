//go:build windows

package console

import (
	"os"
	"syscall"
	"unsafe"
)

func enableVT(f *os.File) bool {
	k := syscall.NewLazyDLL("kernel32.dll")
	get, set := k.NewProc("GetConsoleMode"), k.NewProc("SetConsoleMode")
	var mode uint32
	h := f.Fd()
	if r, _, _ := get.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	const vt = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if mode&vt != 0 {
		return true
	}
	r, _, _ := set.Call(h, uintptr(mode|vt))
	return r != 0
}
