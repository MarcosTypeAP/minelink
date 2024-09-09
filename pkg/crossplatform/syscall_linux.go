package crossplatform

import (
	"errors"
	"syscall"
)

func SetSocketReuseAddr(fd uintptr, value bool) error {
	v := 0
	if value {
		v = 1
	}
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, v)
}

func SetsockoptInt(fd uintptr, level int, opt int, value int) (err error) {
	return syscall.SetsockoptInt(int(fd), level, opt, value)
}

func IsConnError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED)
}
