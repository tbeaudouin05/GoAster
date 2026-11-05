package controltransport

import (
	"os"
	"testing"

	"goaster/internal/controlsecret"
)

func TestUnconfiguredTransportKeepsLoopbackDefault(t *testing.T) {
	t.Setenv(controlsecret.FileEnv, "")
	if err := os.Unsetenv(controlsecret.FileEnv); err != nil {
		t.Fatal(err)
	}
	t.Setenv(controlsecret.ValueEnv, "legacy-secret")
	previous, set := os.LookupEnv(SocketEnv)
	if err := os.Unsetenv(SocketEnv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if set {
			_ = os.Setenv(SocketEnv, previous)
		} else {
			_ = os.Unsetenv(SocketEnv)
		}
	})
	transport, err := ClientTransport()
	if err != nil || transport != nil || Address != "127.0.0.1:8765" {
		t.Fatalf("default transport = %v, %v; address = %q", transport, err, Address)
	}
}

func TestSecretFileRequiresSocketAndEmptySocketFailsClosed(t *testing.T) {
	t.Setenv(SocketEnv, "")
	if err := os.Unsetenv(SocketEnv); err != nil {
		t.Fatal(err)
	}
	t.Setenv(controlsecret.ValueEnv, "")
	for _, path := range []string{"", "/configured/control-secret"} {
		t.Setenv(controlsecret.FileEnv, path)
		if _, err := ClientTransport(); err == nil {
			t.Fatal("configured secret file without a socket enabled the default transport")
		}
		if listener, err := Listen(); err == nil {
			listener.Close()
			t.Fatal("configured secret file without a socket enabled a TCP listener")
		}
	}

	if err := os.Unsetenv(controlsecret.FileEnv); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SocketEnv, "")
	if _, err := ClientTransport(); err == nil {
		t.Fatal("empty configured socket enabled the default transport")
	}
	if listener, err := Listen(); err == nil {
		listener.Close()
		t.Fatal("empty configured socket enabled a listener")
	}
}
