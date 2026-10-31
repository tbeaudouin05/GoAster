package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type testTerminal struct {
	io.Reader
	io.Writer
	closed chan bool
}

func (terminal testTerminal) Close() error {
	if terminal.closed != nil {
		terminal.closed <- true
	}
	return nil
}

func testTerminalOpener(input io.Reader, output io.Writer) controllingTerminalOpener {
	return func() (io.ReadWriteCloser, error) {
		return testTerminal{Reader: input, Writer: output}, nil
	}
}

func TestProtectedHandoffSelectsOnlyControllingTerminal(t *testing.T) {
	output := newChannelWriter()
	closed := make(chan bool, 1)
	opened, calls := 0, 0
	terminal := testTerminal{Reader: strings.NewReader("YES\n"), Writer: output, closed: closed}
	handoff := newProtectedLoginHandoff(func() (io.ReadWriteCloser, error) {
		opened++
		return terminal, nil
	}, func(ctx context.Context, _ []string, input io.Reader, stdout, stderr io.Writer) error {
		calls++
		if input != terminal || stdout != terminal || stderr != terminal {
			t.Fatal("SSH did not use the selected controlling terminal exclusively")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("SSH context has no timeout")
		}
		fmt.Fprintln(stderr, "PRIVATE-SSH-OUTPUT")
		return nil
	})
	result, err := newRemoteLoginTool(handoff).Handler(context.Background(), `{"target":"root@host.example","key":"/tmp/key"}`)
	if err != nil || result != remoteLoginToolSucceeded || strings.Contains(result, "PRIVATE") {
		t.Fatalf("result = %q, error = %v", result, err)
	}
	if opened != 1 || calls != 2 || len(closed) != 1 {
		t.Fatalf("opened = %d, SSH calls = %d, closed = %d", opened, calls, len(closed))
	}
}

func TestProtectedHandoffNoTerminalFailsWithoutSSH(t *testing.T) {
	for _, open := range []controllingTerminalOpener{
		nil,
		func() (io.ReadWriteCloser, error) { return nil, errors.New("SECRET-TERMINAL-DETAIL") },
		func() (io.ReadWriteCloser, error) { return nil, nil },
	} {
		tool := newRemoteLoginTool(newProtectedLoginHandoff(open,
			func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("SSH started without a controlling terminal")
				return nil
			}))
		result, err := tool.Handler(context.Background(), `{"target":"root@host.example","key":"/tmp/key"}`)
		if err != nil || result != remoteLoginToolNoTerminal || strings.Contains(result, "SECRET") {
			t.Fatalf("result = %q, error = %v", result, err)
		}
	}
}

func TestRemoteLoginCommandNoTerminalReturnsGenericError(t *testing.T) {
	err := runRemoteLoginOnTerminal(context.Background(), remoteLoginOptions{},
		func() (io.ReadWriteCloser, error) { return nil, errors.New("SECRET-DETAIL") },
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			t.Fatal("SSH started without a controlling terminal")
			return nil
		})
	if err == nil || err.Error() != remoteLoginToolNoTerminal {
		t.Fatalf("error = %v, want generic terminal failure", err)
	}
}
