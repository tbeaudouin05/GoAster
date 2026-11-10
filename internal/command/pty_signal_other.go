//go:build !linux

package command

import "io"

func signalPTYForeground(terminal io.ReadWriteCloser) {}
