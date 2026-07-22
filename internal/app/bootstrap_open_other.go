//go:build !linux

package app

import "os"

func openBootstrapCredentialFile(path string) (*os.File, error) {
	return os.Open(path)
}
