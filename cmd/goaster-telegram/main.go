// goaster-telegram is an outbound-only adapter for the local GoAster agent.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"goaster/internal/telegram"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "telegram: root is required to protect the adapter state")
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "setup" {
		if err := telegram.Setup(os.Stdin, telegram.DefaultPaths); err != nil {
			fmt.Fprintln(os.Stderr, "telegram: setup failed")
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "telegram: invalid invocation")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := telegram.Run(ctx, telegram.DefaultPaths, notifyReady); err != nil {
		// The adapter returns only static, secret-free messages.
		fmt.Fprintln(os.Stderr, "telegram:", err)
		os.Exit(1)
	}
}

// systemd waits for an available agent process and a successful Telegram poll.
func notifyReady() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return nil
	}
	if strings.HasPrefix(address, "@") {
		address = "\x00" + address[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte("READY=1"))
	return err
}
