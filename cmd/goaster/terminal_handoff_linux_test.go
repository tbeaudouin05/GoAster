package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Exercise both safe handoff paths in child sessions with no controlling tty:
// pipes must fail, while the same inherited PTY on stdin and stdout may proceed.
func TestDetachedProcessCannotStartProtectedLogin(t *testing.T) {
	const pipeHelper = "GOASTER_TEST_DETACHED_HANDOFF"
	const ptyHelper = "GOASTER_TEST_ATTACHED_PTY_HANDOFF"
	if os.Getenv(pipeHelper) == "1" {
		if _, err := syscall.Setsid(); err != nil {
			t.Fatal(err)
		}
		tool := newRemoteLoginTool(newProtectedLoginHandoffWithFallback(openControllingTerminal,
			func() (io.ReadWriteCloser, error) { return openInheritedTerminal(os.Stdin, os.Stdout) },
			func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("detached process started SSH")
				return nil
			}))
		result, err := tool.Handler(context.Background(), `{"target":"root@host.example","key":"/tmp/key"}`)
		if err != nil || result != remoteLoginToolNoTerminal {
			t.Fatalf("result = %q, error = %v", result, err)
		}
		return
	}
	if os.Getenv(ptyHelper) == "1" {
		if _, err := syscall.Setsid(); err != nil {
			t.Fatal(err)
		}
		calls := 0
		tool := newRemoteLoginTool(newProtectedLoginHandoffWithFallback(openControllingTerminal,
			func() (io.ReadWriteCloser, error) { return openInheritedTerminal(os.Stdin, os.Stdout) },
			func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				calls++
				return nil
			}))
		result, err := tool.Handler(context.Background(), `{"target":"root@host.example","key":"/tmp/key"}`)
		if err != nil || result != remoteLoginToolSucceeded || calls != 2 {
			t.Fatalf("PTY handoff result = %q, error = %v, SSH calls = %d", result, err, calls)
		}
		return
	}
	pipeCommand := exec.Command(os.Args[0], "-test.run=^TestDetachedProcessCannotStartProtectedLogin$")
	pipeCommand.Env = append(os.Environ(), pipeHelper+"=1")
	if output, err := pipeCommand.CombinedOutput(); err != nil {
		t.Fatalf("detached pipe handoff test: %v\n%s", err, output)
	}

	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	defer master.Close()
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}
	ptyNumber, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("read PTY number: %v", err)
	}
	slaveName := fmt.Sprintf("/dev/pts/%d", ptyNumber)
	slaveFD, err := unix.Open(slaveName, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open PTY slave: %v", err)
	}
	slave := os.NewFile(uintptr(slaveFD), slaveName)
	defer slave.Close()

	ptyCommand := exec.Command(os.Args[0], "-test.run=^TestDetachedProcessCannotStartProtectedLogin$")
	ptyCommand.Env = append(os.Environ(), ptyHelper+"=1")
	ptyCommand.Stdin = slave
	ptyCommand.Stdout = slave
	ptyCommand.Stderr = io.Discard
	if err := ptyCommand.Start(); err != nil {
		t.Fatalf("start attached PTY handoff test: %v", err)
	}
	if _, err := master.Write([]byte("YES\n")); err != nil {
		t.Fatalf("approve attached PTY handoff: %v", err)
	}
	if err := ptyCommand.Wait(); err != nil {
		t.Fatalf("attached PTY handoff test: %v", err)
	}
}
