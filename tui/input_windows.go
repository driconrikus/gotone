//go:build windows

package tui

import (
	"os"

	"golang.org/x/sys/windows"
)

var originalMode uint32

func init() {
	h := windows.Handle(os.Stdin.Fd())
	windows.GetConsoleMode(h, &originalMode)
}

func enableRawMode() {
	h := windows.Handle(os.Stdin.Fd())
	windows.SetConsoleMode(h, windows.ENABLE_VIRTUAL_TERMINAL_INPUT)
}

func disableRawMode() {
	h := windows.Handle(os.Stdin.Fd())
	windows.SetConsoleMode(h, originalMode)
}

func readRawInput(buf []byte) (int, error) {
	enableRawMode()
	defer disableRawMode()
	return os.Stdin.Read(buf)
}
