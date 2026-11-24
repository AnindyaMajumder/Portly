package cli

import (
	"os"
	"syscall"
	"unsafe"
)

const defaultWidth = 80

var screenInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleScreenBufferInfo")

// windowSize returns the width, and whether the file is a console.
func windowSize(file *os.File) (int, bool) {
	var mode uint32
	if syscall.GetConsoleMode(syscall.Handle(file.Fd()), &mode) != nil {
		return 0, false
	}
	var info struct {
		sizeX, sizeY, cursorX, cursorY int16
		attributes                     uint16
		left, top, right, bottom       int16
		maxX, maxY                     int16
	}
	if result, _, _ := screenInfo.Call(file.Fd(), uintptr(unsafe.Pointer(&info))); result == 0 || info.right < info.left {
		return defaultWidth, true
	}
	return int(info.right-info.left) + 1, true
}
