package crossplatform

import (
	"errors"
	"syscall"
)

const WSAECONNREFUSED syscall.Errno = 10061

func SetSocketReuseAddr(fd uintptr, value bool) error {
	v := 0
	if value {
		v = 1
	}
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, v)
}

func SetsockoptInt(fd uintptr, level int, opt int, value int) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), level, opt, value)
}

func IsConnError(err error) bool {
	return errors.Is(err, WSAECONNREFUSED) || errors.Is(err, syscall.WSAECONNRESET) || errors.Is(err, syscall.ECONNABORTED)
}
