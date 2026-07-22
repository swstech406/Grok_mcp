//go:build linux

package app

import (
	"os"
	"syscall"
)

func openBootstrapCredentialFile(path string) (*os.File, error) {
	fileDescriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fileDescriptor), path), nil
}
