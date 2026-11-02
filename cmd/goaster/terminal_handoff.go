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

	"golang.org/x/term"

	"goaster/internal/chat"
)

// controllingTerminalOpener selects this process's controlling terminal.
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

func newProtectedProcessHandoff(open controllingTerminalOpener, sshRun sshRunner) processHandoff {
	return newProtectedProcessHandoffWithFallback(open, nil, sshRun)
}

func newProtectedProcessHandoffWithFallback(open, fallback controllingTerminalOpener, sshRun sshRunner) processHandoff {
	return func(ctx context.Context, request processHandoffRequest) string {
		if ctx == nil || ctx.Err() != nil {
			return processHandoffToolFailed
		}
		profile, ok := processHandoffProfileFor(request.Profile)
		if !ok || validateProcessHandoffRequest(request) != nil {
			return processHandoffToolInvalid
		}
		if request.Mode == processHandoffModeBackground {
			if profile.background == nil {
				return processHandoffToolInvalid
			}
			if err := profile.background(ctx, request); err != nil {
				return processHandoffToolFailed
			}
			return processHandoffToolSucceeded
		}
		if request.Mode != processHandoffModeInteractive || profile.interactive == nil || sshRun == nil {
			return processHandoffToolFailed
		}
		var terminal io.ReadWriteCloser
		var err error
		if open != nil {
			terminal, err = open()
		}
		if err != nil || terminal == nil {
			if fallback != nil {
				terminal, err = fallback()
			}
		}
		if err != nil || terminal == nil {
			return processHandoffToolNoTerminal
		}
		defer terminal.Close()
		return approveProcessHandoff(ctx, request, terminal, profile.interactive, sshRun)
	}
}

type terminalDevice interface {
	Fd() uintptr
	Stat() (os.FileInfo, error)
}

type inheritedTerminal struct {
	input  io.Reader
	output io.Writer
}

func (terminal inheritedTerminal) Read(content []byte) (int, error) {
	return terminal.input.Read(content)
}

func (terminal inheritedTerminal) Write(content []byte) (int, error) {
	return terminal.output.Write(content)
}

// Close leaves the process-owned standard streams open.
func (terminal inheritedTerminal) Close() error {
	return nil
}

func openInheritedTerminal(input io.Reader, output io.Writer) (io.ReadWriteCloser, error) {
	inputDevice, inputIsDevice := input.(terminalDevice)
	outputDevice, outputIsDevice := output.(terminalDevice)
	if !inputIsDevice || !outputIsDevice {
		return nil, errors.New("inherited standard streams are not terminal devices")
	}
	inputInfo, err := inputDevice.Stat()
	if err != nil || inputInfo == nil {
		return nil, errors.New("inherited standard input is unavailable")
	}
	outputInfo, err := outputDevice.Stat()
	if err != nil || outputInfo == nil {
		return nil, errors.New("inherited standard output is unavailable")
	}
	if !os.SameFile(inputInfo, outputInfo) || !term.IsTerminal(int(inputDevice.Fd())) || !term.IsTerminal(int(outputDevice.Fd())) {
		return nil, errors.New("inherited standard streams are not the same terminal device")
	}
	return inheritedTerminal{input: input, output: output}, nil
}

// The compatibility CLI command uses its configured controlling-terminal
// opener without adding the model tool's separate approval prompt.
func runRemoteLoginOnTerminal(ctx context.Context, options remoteLoginOptions, open controllingTerminalOpener, sshRun sshRunner) error {
	if open == nil {
		return errors.New(processHandoffToolNoTerminal)
	}
	terminal, err := open()
	if err != nil || terminal == nil {
		return errors.New(processHandoffToolNoTerminal)
	}
	defer terminal.Close()
	request := processHandoffRequest{
		Profile:        processHandoffProfileOpenAI,
		Mode:           processHandoffModeInteractive,
		Target:         options.Target,
		IdentityFile:   options.IdentityFile,
		KnownHostsFile: options.KnownHostsFile,
		Port:           options.Port,
	}
	return runOpenAIInteractiveProcessHandoff(ctx, request, terminal, sshRun)
}

func approveProcessHandoff(ctx context.Context, request processHandoffRequest, terminal io.ReadWriter, run processHandoffInteractiveRunner, sshRun sshRunner) string {
	output, errorOutput := io.Writer(terminal), io.Writer(terminal)
	approvalInput := chat.NewTerminalInput(terminal)
	knownHosts := "default known_hosts file"
	if request.KnownHostsFile != "" {
		knownHosts = strconv.Quote(request.KnownHostsFile)
	}

	if _, err := fmt.Fprintf(output,
		"\nProtected process handoff approval required.\nProfile: %q\nTarget: %q\nSSH port: %d\nLocal SSH key: %s\nKnown-hosts file: %s\nHost-key verification: strict; an unknown or changed key will stop the connection.\nType YES to start the interactive handoff and verification: ",
		request.Profile, request.Target, request.Port, strconv.Quote(request.IdentityFile), knownHosts); err != nil {
		return processHandoffToolFailed
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return processHandoffToolFailed
		}
	}
	answer, completed, err := approvalInput.ReadLine(4096)
	if err != nil || !completed || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		fmt.Fprintln(output, "\nProcess handoff canceled; no connection was made.")
		return processHandoffToolDeclined
	}

	if err := run(ctx, request, terminal, sshRun); err != nil {
		fmt.Fprintf(errorOutput, "Process handoff did not complete: %v. Review the local terminal output before continuing.\n", err)
		return processHandoffToolFailed
	}
	return processHandoffToolSucceeded
}
