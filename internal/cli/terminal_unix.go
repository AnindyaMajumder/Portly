//go:build unix

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

const defaultWidth = 80

// windowSize returns the width, and whether the file is a terminal.
func windowSize(file *os.File) (int, bool) {
	var size struct{ rows, columns, x, y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, false
	}
	if size.columns == 0 {
		return defaultWidth, true
	}
	return int(size.columns), true
}
