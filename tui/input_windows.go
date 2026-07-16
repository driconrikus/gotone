//go:build windows

package tui

import (
	"os"
	"unsafe"

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

	h := windows.Handle(os.Stdin.Fd())
	var n uint32
	events := make([]windows.InputRecord, 1)
	err := windows.ReadConsoleInput(h, &events[0], 1, &n)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}

	ev := events[0]
	if ev.EventType != windows.KEY_EVENT {
		return 0, nil
	}
	if !*ev.KeyEvent.KeyDown {
		return 0, nil
	}

	kc := ev.KeyEvent.UnicodeChar
	if kc == 0 {
		// Virtual key (arrows etc)
		vk := ev.VirtualKeyCode
		switch vk {
		case 38: // Up
			copy(buf, "\033[A")
			return 3, nil
		case 40: // Down
			copy(buf, "\033[B")
			return 3, nil
		case 27: // Escape
			copy(buf, "\033")
			return 1, nil
		}
		return 0, nil
	}

	buf[0] = byte(kc)
	return 1, nil
}

// Ensure import is used
var _ = unsafe.Sizeof(0)
