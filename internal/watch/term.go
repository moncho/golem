//go:build darwin || linux

package watch

import (
	"os"
	"syscall"
	"unsafe"
)

type winsize struct{ Row, Col, X, Y uint16 }

// Size reports the terminal's columns and rows, or ok false when f is not one.
func Size(f *os.File) (cols, rows int, ok bool) {
	var ws winsize
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 || ws.Col == 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	var t syscall.Termios
	return ioctl(f.Fd(), getTermios, &t) == nil
}

// MakeRaw turns off echo and line buffering so single keys arrive at once,
// keeping signals on so Ctrl-C still works. The returned function restores
// the previous settings.
func MakeRaw(f *os.File) (func(), error) {
	var old syscall.Termios
	if err := ioctl(f.Fd(), getTermios, &old); err != nil {
		return nil, err
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(f.Fd(), setTermios, &raw); err != nil {
		return nil, err
	}
	return func() { _ = ioctl(f.Fd(), setTermios, &old) }, nil
}

func ioctl(fd, req uintptr, t *syscall.Termios) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t))); errno != 0 {
		return errno
	}
	return nil
}
