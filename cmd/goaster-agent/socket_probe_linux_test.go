//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"goaster/internal/chat"
	"goaster/internal/controlsecret"
	"goaster/internal/controltransport"
)

func TestAgentSocketProbesAndGracefulStop(t *testing.T) {
	switch os.Getenv("GOASTER_SOCKET_STOP_HELPER") {
	case "1":
		if err := serve(chat.CredentialStore{}); err != nil {
			t.Fatal(err)
		}
		return
	case "reject":
		if err := serve(chat.CredentialStore{}); err == nil || !strings.Contains(err.Error(), "agent socket configuration is invalid or unavailable") {
			t.Fatalf("agent accepted file secret without socket: %v", err)
		}
		return
	case "wait":
		if err := serve(chat.CredentialStore{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("agent lock wait did not return cancellation: %v", err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("the protected socket requires root")
	}
	dir, err := os.MkdirTemp("", "gp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	if len(path) > 107 {
		t.Fatal("temporary path is too long for a Unix socket; set TMPDIR to a shorter private directory")
	}
	t.Setenv(controltransport.SocketEnv, path)
	listener, err := controltransport.Listen()
	if err != nil {
		t.Fatal(err)
	}
	server := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/livez" || r.URL.Path == "/healthz" {
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		http.NotFound(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	for _, probe := range []string{"live", "health"} {
		var output bytes.Buffer
		if err := run(context.Background(), []string{probe}, &output, nil, nil); err != nil || output.String() != "ok\n" {
			t.Fatalf("%s probe = %q, %v", probe, output.String(), err)
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"health"}, io.Discard, nil, nil); err == nil {
		t.Fatal("health probe succeeded after the socket was closed")
	}

	// Exercise the actual agent startup and signal handler with a file-backed
	// test secret. A normal termination must leave the path ready to rebind.
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(dir, "control-secret")
	if err := os.WriteFile(secretPath, []byte(strings.Repeat("ab", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("GOASTER_STARTER_DIR", filepath.Join(filepath.Dir(filepath.Dir(working)), "skills"))
	t.Setenv(controlsecret.ValueEnv, "")
	t.Setenv(controlsecret.FileEnv, secretPath)
	child := exec.Command(os.Args[0], "-test.run=^TestAgentSocketProbesAndGracefulStop$")
	child.Env = append(os.Environ(), "GOASTER_SOCKET_STOP_HELPER=1")
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := probeAgent(context.Background(), "live", io.Discard); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("agent exited before becoming live: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not become live")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent did not stop cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not stop after SIGTERM")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains after SIGTERM: %v", err)
	}
	listener, err = controltransport.Listen()
	if err != nil {
		t.Fatalf("socket could not be rebound after SIGTERM: %v", err)
	}
	listener.Close()

	// Hold the real starter lock while starting the actual service. Seeing its
	// open lock descriptor proves the signal handler is installed and startup
	// is waiting on this lock, rather than signaling a process before startup.
	lockPath := filepath.Join(dir, ".goaster", "artefacts", ".skills-install.lock")
	lockFD, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(lockFD)
	if err := syscall.Flock(lockFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		child = exec.Command(os.Args[0], "-test.run=^TestAgentSocketProbesAndGracefulStop$")
		child.Env = append(os.Environ(), "GOASTER_SOCKET_STOP_HELPER=wait")
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer child.Process.Kill()
		done = make(chan error, 1)
		go func() { done <- child.Wait() }()
		deadline = time.Now().Add(10 * time.Second)
		for {
			entries, _ := os.ReadDir(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "fd"))
			found := false
			for _, entry := range entries {
				target, _ := os.Readlink(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "fd", entry.Name()))
				if target == lockPath {
					found = true
					break
				}
			}
			if found {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("agent exited before waiting on the starter lock: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("agent did not open the starter lock")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := child.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("agent waiting on starter lock did not stop after %s: %v", sig, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("agent waiting on starter lock did not stop after %s", sig)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("agent opened a socket while waiting on the starter lock: %v", err)
		}
	}
	if err := syscall.Flock(lockFD, syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	if err := os.Unsetenv(controltransport.SocketEnv); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rejected := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentSocketProbesAndGracefulStop$")
	rejected.Env = append(os.Environ(), "GOASTER_SOCKET_STOP_HELPER=reject")
	if output, err := rejected.CombinedOutput(); err != nil {
		t.Fatalf("agent did not reject a file secret without a socket: %v\n%s", err, output)
	}
}
