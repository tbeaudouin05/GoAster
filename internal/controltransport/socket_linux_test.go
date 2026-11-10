//go:build linux

package controltransport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Use a short temporary path: Linux Unix socket names have a small length
// limit. A private child of a searchable fixture tests directory permissions.
func testSocketPath(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("the protected socket requires root")
	}
	root, err := os.MkdirTemp("", "gs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "d")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	if len(path) > 107 {
		t.Fatal("temporary path is too long for a Unix socket; set TMPDIR to a shorter private directory")
	}
	return path
}

func TestSocketServesAllAgentRequests(t *testing.T) {
	path := testSocketPath(t)
	t.Setenv(SocketEnv, path)
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	if listener.Addr().Network() != "unix" {
		t.Fatalf("listener network = %q", listener.Addr().Network())
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || !rootOwned(info) {
		t.Fatalf("socket permissions or owner are unsafe: %v, %v", info, err)
	}
	server := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Header.Get("X-GoAster-GitHub-Auth-Control") != "test-secret" {
			t.Error("control request arrived without its secret")
		}
		_, _ = io.WriteString(w, r.URL.Path)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	transport, err := ClientTransport()
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Transport: transport, Timeout: 2 * time.Second}
	for _, endpoint := range []string{"/livez", "/healthz", "/chat"} {
		method := http.MethodGet
		if endpoint != "/livez" && endpoint != "/healthz" {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, "http://"+Address+endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-GoAster-GitHub-Auth-Control", "test-secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != endpoint {
			t.Fatalf("%s response = %q, %v", endpoint, body, err)
		}
	}
}

func TestUnavailableSocketNeverSendsRequestsToTCPImpersonator(t *testing.T) {
	path := testSocketPath(t)
	t.Setenv(SocketEnv, path)
	var calls atomic.Int32
	spoof := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "ok\n")
	}))
	defer spoof.Close()
	transport, err := ClientTransport()
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Transport: transport, Timeout: 2 * time.Second}
	for _, endpoint := range []string{"/livez", "/healthz", "/chat"} {
		req, err := http.NewRequest(http.MethodPost, spoof.URL+endpoint, strings.NewReader("sensitive message"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-GoAster-GitHub-Auth-Control", "test-secret")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s unexpectedly reached TCP", endpoint)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("TCP impersonator received %d requests", calls.Load())
	}
}

func TestSocketRejectsUnsafePathsAndExistingFiles(t *testing.T) {
	path := testSocketPath(t)
	dir := filepath.Dir(path)
	for _, invalid := range []string{"", "relative.sock", path + "/../s", filepath.Join(dir, "missing", "s")} {
		t.Setenv(SocketEnv, invalid)
		if _, err := ClientTransport(); err == nil {
			t.Fatalf("client accepted unsafe path %q", invalid)
		}
		if listener, err := Listen(); err == nil {
			listener.Close()
			t.Fatalf("listener accepted unsafe path %q", invalid)
		}
	}
	t.Setenv(SocketEnv, path)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ClientTransport(); err == nil {
		t.Fatal("client accepted a non-private socket directory")
	}
	if listener, err := Listen(); err == nil {
		listener.Close()
		t.Fatal("listener accepted a non-private socket directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "l")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SocketEnv, filepath.Join(link, "s"))
	if _, err := ClientTransport(); err == nil {
		t.Fatal("client accepted a symlinked socket directory")
	}
	if listener, err := Listen(); err == nil {
		listener.Close()
		t.Fatal("listener accepted a symlinked socket directory")
	}
	untrusted := filepath.Join(dir, "u")
	if err := os.Mkdir(untrusted, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(untrusted, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SocketEnv, filepath.Join(untrusted, "s"))
	if _, err := ClientTransport(); err == nil {
		t.Fatal("client accepted a directory owned by another user")
	}
	if listener, err := Listen(); err == nil {
		listener.Close()
		t.Fatal("listener accepted a directory owned by another user")
	}
	t.Setenv(SocketEnv, path)
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if listener, err := Listen(); err == nil {
		listener.Close()
		t.Fatal("listener replaced an existing file")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "keep" {
		t.Fatalf("existing file changed: %q, %v", content, err)
	}
	transport, err := ClientTransport()
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Transport: transport, Timeout: time.Second}
	if resp, err := client.Get("http://" + Address + "/livez"); err == nil {
		resp.Body.Close()
		t.Fatal("client accepted a regular file as the socket")
	}
}

func TestSocketDoesNotReplaceLiveListenerOrTrustSymlink(t *testing.T) {
	path := testSocketPath(t)
	t.Setenv(SocketEnv, path)
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if other, err := Listen(); err == nil {
		other.Close()
		t.Fatal("second listener replaced the live socket")
	}
	// Keep the symlink path as short as the socket path near the Unix limit.
	link := filepath.Join(filepath.Dir(path), "l")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SocketEnv, link)
	transport, err := ClientTransport()
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Transport: transport, Timeout: time.Second}
	if response, err := client.Get("http://" + Address + "/livez"); err == nil {
		response.Body.Close()
		t.Fatal("client followed a socket symlink")
	}
}

// A hostile process can own a listening socket even if the filesystem entry
// is later made root-owned. The client must reject it before writing HTTP.
func TestSocketRejectsUnprivilegedPeerBeforeSendingSecret(t *testing.T) {
	path := testSocketPath(t)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	child := exec.Command("/proc/self/exe", "-test.run=^TestUnprivilegedSocketPeerHelper$")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	child.ExtraFiles = []*os.File{directory}
	child.Env = append(os.Environ(), "GOASTER_SOCKET_PEER_HELPER=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	output := bufio.NewReader(stdout)
	ready, err := output.ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("unprivileged peer did not start: %q, %v", ready, err)
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SocketEnv, path)
	transport, err := ClientTransport()
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://"+Address+"/chat", strings.NewReader("private"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-GoAster-GitHub-Auth-Control", "test-secret")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("client trusted an unprivileged peer")
	}
	result, err := output.ReadString('\n')
	if err != nil || result != "bytes=0\n" {
		t.Fatalf("unprivileged peer received data: %q, %v", result, err)
	}
}

func TestUnprivilegedSocketPeerHelper(t *testing.T) {
	if os.Getenv("GOASTER_SOCKET_PEER_HELPER") != "1" {
		t.Skip("child helper")
	}
	if os.Geteuid() != 65534 {
		t.Fatal("helper is not unprivileged")
	}
	if err := unix.Fchdir(3); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	fmt.Println("ready")
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 4096)
	n, _ := conn.Read(buffer)
	fmt.Printf("bytes=%d\n", n)
}

func TestSocketRevalidatesDirectoryOnEachConnection(t *testing.T) {
	path := testSocketPath(t)
	t.Setenv(SocketEnv, path)
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	transport, err := ClientTransport()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+Address+"/livez", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Transport: transport, Timeout: time.Second}
	if response, err := client.Do(request); err == nil {
		response.Body.Close()
		t.Fatal("client used a socket after its directory became unsafe")
	}
}

func TestPrivateSocketDirectoryRejectsUnprivilegedAccess(t *testing.T) {
	path := testSocketPath(t)
	t.Setenv(SocketEnv, path)
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	parent, err := os.Open(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	child := exec.Command("/proc/self/exe", "-test.run=^TestUnprivilegedSocketAccessHelper$")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	child.ExtraFiles = []*os.File{parent}
	child.Env = append(os.Environ(), "GOASTER_SOCKET_ACCESS_HELPER="+filepath.Base(filepath.Dir(path)))
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged access was not rejected: %v\n%s", err, output)
	}
}

func TestUnprivilegedSocketAccessHelper(t *testing.T) {
	dir := os.Getenv("GOASTER_SOCKET_ACCESS_HELPER")
	if dir == "" {
		t.Skip("child helper")
	}
	if os.Geteuid() != 65534 {
		t.Fatal("helper is not unprivileged")
	}
	if err := unix.Fchdir(3); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.Dial("unix", filepath.Join(dir, "s")); err == nil {
		conn.Close()
		t.Fatal("unprivileged client connected to the root socket")
	}
	if listener, err := net.Listen("unix", filepath.Join(dir, "spoof")); err == nil {
		listener.Close()
		t.Fatal("unprivileged process created a socket in the private directory")
	}
}
