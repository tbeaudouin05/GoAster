package main

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyReady(t *testing.T) {
	address := filepath.Join(t.TempDir(), "notify.sock")
	socket, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	t.Setenv("NOTIFY_SOCKET", address)
	if err := notifyReady(); err != nil {
		t.Fatal(err)
	}
	if err := socket.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _, err := socket.ReadFromUnix(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatal("systemd did not receive the ready notification")
	}
}
