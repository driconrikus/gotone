//go:build darwin || linux

package tui

import (
	"os"
	"syscall"
	"unsafe"
)

var originalTermios syscall.Termios

func init() {
	fd := int(os.Stdin.Fd())
	// Save original termios
	syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCGETA),
		uintptr(unsafe.Pointer(&originalTermios)),
		0, 0, 0,
	)
}

func enableRawMode() {
	fd := int(os.Stdin.Fd())
	raw := originalTermios
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1
	syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCSETA),
		uintptr(unsafe.Pointer(&raw)),
		0, 0, 0,
	)
}

func disableRawMode() {
	fd := int(os.Stdin.Fd())
	syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCSETA),
		uintptr(unsafe.Pointer(&originalTermios)),
		0, 0, 0,
	)
}

func readRawInput(buf []byte) (int, error) {
	enableRawMode()
	defer disableRawMode()
	return os.Stdin.Read(buf)
}
