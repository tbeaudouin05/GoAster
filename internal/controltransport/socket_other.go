//go:build !linux

package controltransport

import (
	"net"
	"net/http"
)

// The protected socket is currently supported only for the Linux root setup.
func listenSocket(string) (net.Listener, error)         { return nil, invalidSocket }
func socketTransport(string) (http.RoundTripper, error) { return nil, invalidSocket }
