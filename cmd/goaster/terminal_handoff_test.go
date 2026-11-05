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

func TestProtectedProcessHandoffSelectsOnlyControllingTerminal(t *testing.T) {
	output := newChannelWriter()
	closed := make(chan bool, 1)
	opened, calls := 0, 0
	terminal := testTerminal{Reader: strings.NewReader("YES\n"), Writer: output, closed: closed}
	handoff := newProtectedProcessHandoffWithFallback(func() (io.ReadWriteCloser, error) {
		opened++
		return terminal, nil
	}, func() (io.ReadWriteCloser, error) {
		t.Fatal("inherited stdio fallback replaced the controlling terminal")
		return nil, nil
	}, func(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) error {
		calls++
		if input != terminal || stdout != terminal || stderr != terminal {
			t.Fatal("SSH did not use the selected controlling terminal exclusively")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("SSH context has no timeout")
		}
		if calls == 2 && args[len(args)-1] != "/usr/bin/env GOASTER_AGENT_SOCKET=/run/goaster-agent/agent.sock /usr/local/bin/goaster-agent health" {
			t.Fatalf("health command = %q", args[len(args)-1])
		}
		fmt.Fprintln(stderr, "PRIVATE-SSH-OUTPUT")
		return nil
	})
	result, err := newProcessHandoffTool(handoff).Handler(context.Background(), `{"agent_transport":"socket","profile":"openai","target":"root@host.example","key":"/tmp/key","agent_socket":"/run/goaster-agent/agent.sock"}`)
	if err != nil || result != processHandoffToolSucceeded || strings.Contains(result, "PRIVATE") {
		t.Fatalf("result = %q, error = %v", result, err)
	}
	if opened != 1 || calls != 2 || len(closed) != 1 {
		t.Fatalf("opened = %d, SSH calls = %d, closed = %d", opened, calls, len(closed))
	}
	if !strings.Contains(output.String(), `Agent transport: socket "/run/goaster-agent/agent.sock"`) {
		t.Fatal("terminal approval did not identify the reviewed agent transport")
	}
}

func TestProtectedProcessHandoffNoTerminalFailsWithoutSSH(t *testing.T) {
	for _, open := range []controllingTerminalOpener{
		nil,
		func() (io.ReadWriteCloser, error) { return nil, errors.New("SECRET-TERMINAL-DETAIL") },
		func() (io.ReadWriteCloser, error) { return nil, nil },
	} {
		tool := newProcessHandoffTool(newProtectedProcessHandoff(open,
			func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("SSH started without a controlling terminal")
				return nil
			}))
		result, err := tool.Handler(context.Background(), `{"agent_transport":"tcp","profile":"openai","target":"root@host.example","key":"/tmp/key"}`)
		if err != nil || result != processHandoffToolNoTerminal || strings.Contains(result, "SECRET") {
			t.Fatalf("result = %q, error = %v", result, err)
		}
	}
}

func TestRemoteLoginCommandUsesProtectedTerminalAndSelectedTransport(t *testing.T) {
	err := runRemoteLoginOnTerminal(context.Background(), remoteLoginOptions{},
		func() (io.ReadWriteCloser, error) { return nil, errors.New("SECRET-DETAIL") },
		func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
			t.Fatal("SSH started without a controlling terminal")
			return nil
		})
	if err == nil || err.Error() != processHandoffToolNoTerminal {
		t.Fatalf("error = %v, want generic terminal failure", err)
	}

	for _, tc := range []struct {
		name, socket, healthCommand string
		failHealth                  bool
	}{
		{name: "socket", socket: "/run/goaster-agent/agent.sock", healthCommand: "/usr/bin/env GOASTER_AGENT_SOCKET=/run/goaster-agent/agent.sock /usr/local/bin/goaster-agent health"},
		{name: "socket unavailable", socket: "/run/goaster-agent/agent.sock", healthCommand: "/usr/bin/env GOASTER_AGENT_SOCKET=/run/goaster-agent/agent.sock /usr/local/bin/goaster-agent health", failHealth: true},
		{name: "legacy TCP", healthCommand: remoteHealthCommand},
	} {
		transport := remoteTransportTCP
		if tc.socket != "" {
			transport = remoteTransportSocket
		}
		args := []string{"--target", "root@host.example", "--key", "/tmp/key", "--agent-transport", transport}
		if tc.socket != "" {
			args = append(args, "--agent-socket", tc.socket)
		}
		options, err := parseRemoteLogin(args, io.Discard)
		if err != nil {
			t.Fatalf("%s: parse options: %v", tc.name, err)
		}
		terminal := testTerminal{Reader: strings.NewReader(""), Writer: io.Discard}
		calls := 0
		err = runRemoteLoginOnTerminal(context.Background(), options,
			func() (io.ReadWriteCloser, error) { return terminal, nil },
			func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) error {
				calls++
				if calls == 2 && args[len(args)-1] != tc.healthCommand {
					t.Errorf("%s: health command = %q, want %q", tc.name, args[len(args)-1], tc.healthCommand)
				}
				if calls == 2 && tc.failHealth {
					return errors.New("socket unavailable")
				}
				return nil
			})
		if calls != 2 || (err != nil) != tc.failHealth {
			t.Fatalf("%s: SSH calls = %d, error = %v", tc.name, calls, err)
		}
	}
}
