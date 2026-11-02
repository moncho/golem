package watch

import "syscall"

const (
	getTermios = syscall.TCGETS
	setTermios = syscall.TCSETS
)
