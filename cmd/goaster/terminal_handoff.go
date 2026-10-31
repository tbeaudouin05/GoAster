package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"goaster/internal/chat"
)

// controllingTerminalOpener never selects a terminal from tool stdio, an
// environment variable, or a list of other processes' pseudoterminals.
type controllingTerminalOpener func() (io.ReadWriteCloser, error)

func openControllingTerminal() (io.ReadWriteCloser, error) {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris", "illumos", "aix":
		// /dev/tty refers to this process's controlling terminal, even when
		// its standard streams are redirected. A detached process cannot open it.
		return os.OpenFile("/dev/tty", os.O_RDWR, 0)
	default:
		return nil, errors.New("protected controlling terminal unavailable")
	}
}

func newProtectedLoginHandoff(open controllingTerminalOpener, sshRun sshRunner) remoteLoginHandoff {
	return func(ctx context.Context, options remoteLoginOptions) string {
		if ctx == nil || ctx.Err() != nil || sshRun == nil {
			return remoteLoginToolFailed
		}
		if open == nil {
			return remoteLoginToolNoTerminal
		}
		terminal, err := open()
		if err != nil || terminal == nil {
			return remoteLoginToolNoTerminal
		}
		defer terminal.Close()
		return approveRemoteLogin(ctx, options, terminal, sshRun)
	}
}

// The explicit CLI command uses the same protected terminal selection, without
// adding the model tool's separate approval prompt to that existing command.
func runRemoteLoginOnTerminal(ctx context.Context, options remoteLoginOptions, open controllingTerminalOpener, sshRun sshRunner) error {
	terminal, err := open()
	if err != nil || terminal == nil {
		return errors.New(remoteLoginToolNoTerminal)
	}
	defer terminal.Close()
	return runRemoteLoginWith(ctx, options, terminal, terminal, terminal, sshRun)
}

func approveRemoteLogin(ctx context.Context, options remoteLoginOptions, terminal io.ReadWriter, sshRun sshRunner) string {
	output, errorOutput := io.Writer(terminal), io.Writer(terminal)
	approvalInput := chat.NewTerminalInput(terminal)
	knownHosts := "default known_hosts file"
	if options.KnownHostsFile != "" {
		knownHosts = strconv.Quote(options.KnownHostsFile)
	}

	if _, err := fmt.Fprintf(output,
		"\nRemote login approval required.\nTarget: %q\nSSH port: %d\nLocal SSH key: %s\nKnown-hosts file: %s\nHost-key verification: strict; an unknown or changed key will stop the connection.\nType YES to start the interactive login and health check: ",
		options.Target, options.Port, strconv.Quote(options.IdentityFile), knownHosts); err != nil {
		return remoteLoginToolFailed
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return remoteLoginToolFailed
		}
	}
	answer, completed, err := approvalInput.ReadLine(4096)
	if err != nil || !completed || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fmt.Fprintln(output, "\nRemote login canceled; no connection was made.")
		return remoteLoginToolDeclined
	}

	if err := runRemoteLoginWith(ctx, options, terminal, output, errorOutput, sshRun); err != nil {
		fmt.Fprintf(errorOutput, "Remote login did not complete: %v. Review the local terminal output before continuing.\n", err)
		return remoteLoginToolFailed
	}
	return remoteLoginToolSucceeded
}
