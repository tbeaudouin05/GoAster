package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// A separate session proves that even inherited standard streams cannot be
// mistaken for a controlling terminal. No SSH or network process is started.
func TestDetachedProcessCannotStartProtectedLogin(t *testing.T) {
	const helper = "GOASTER_TEST_DETACHED_HANDOFF"
	if os.Getenv(helper) == "1" {
		if _, err := syscall.Setsid(); err != nil {
			t.Fatal(err)
		}
		tool := newRemoteLoginTool(newProtectedLoginHandoff(openControllingTerminal,
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
	command := exec.Command(os.Args[0], "-test.run=^TestDetachedProcessCannotStartProtectedLogin$")
	command.Env = append(os.Environ(), helper+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("detached handoff test: %v\n%s", err, output)
	}
}
