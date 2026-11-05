// Package controltransport selects the local agent transport.
package controltransport

import (
	"errors"
	"net"
	"net/http"
	"os"

	"goaster/internal/controlsecret"
)

const (
	SocketEnv = "GOASTER_AGENT_SOCKET"
	Address   = "127.0.0.1:8765"
)

var invalidSocket = errors.New("agent socket configuration is invalid or unavailable")

// Listen keeps the original loopback listener for legacy configurations. A
// configured secret file requires a socket, and socket errors never enable TCP.
func Listen() (net.Listener, error) {
	path, configured := os.LookupEnv(SocketEnv)
	if !configured {
		if _, fileConfigured := os.LookupEnv(controlsecret.FileEnv); fileConfigured {
			return nil, invalidSocket
		}
		return net.Listen("tcp", Address)
	}
	return listenSocket(path)
}

// ClientTransport returns nil for the original HTTP transport. When a socket
// is configured, every connection must use that socket, including retries.
func ClientTransport() (http.RoundTripper, error) {
	path, configured := os.LookupEnv(SocketEnv)
	if !configured {
		if _, fileConfigured := os.LookupEnv(controlsecret.FileEnv); fileConfigured {
			return nil, invalidSocket
		}
		return nil, nil
	}
	return socketTransport(path)
}
