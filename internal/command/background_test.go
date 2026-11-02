package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	appconfig "goaster/config"
)

// This helper runs in a separate copy of the test binary, without a shell.
func backgroundTestHelper() {
	index := -1
	for i, arg := range os.Args {
		if arg == "--background-test-helper" {
			index = i
			break
		}
	}
	if index < 0 || len(os.Args) <= index+1 {
		return
	}
	args := os.Args[index+2:]
	switch os.Args[index+1] {
	case "report":
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Args []string `json:"args"`
			Dir  string   `json:"dir"`
		}{args, dir})
	case "hold":
		fmt.Print("session-ready")
		time.Sleep(30 * time.Second)
	case "fail":
		fmt.Fprint(os.Stderr, "expected failure")
		os.Exit(7)
	case "overflow":
		fmt.Print(strings.Repeat("x", MaxOutputBytes+1))
	case "open-output":
		child := exec.Command(os.Args[0], backgroundHelperArgs("hold")...)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
	case "waitfile":
		fmt.Print("session-ready")
		time.Sleep(2 * time.Second)
		_ = os.WriteFile(args[0], []byte("process survived"), 0600)
	case "mark-ready":
		_ = os.WriteFile(args[0], []byte("ready"), 0600)
		time.Sleep(30 * time.Second)
	case "signal-host":
		manager := NewBackgroundCommandManagerAt(args[0])
		unregister := manager.CloseOnSignal()
		defer unregister()
		defer manager.Close()
		session, err := manager.Start(context.Background(), "worker")
		if err != nil {
			os.Exit(3)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			output, err := manager.Output(context.Background(), session.ID)
			if err == nil && strings.Contains(output.Stdout, "session-ready") {
				fmt.Println("host-ready")
				time.Sleep(30 * time.Second)
				os.Exit(4)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(5)
	default:
		os.Exit(6)
	}
	os.Exit(0)
}

func backgroundHelperArgs(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestExecuteSuccess$", "--", "--background-test-helper", mode}, args...)
}

func backgroundTestExecutable(t testing.TB) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func writeBackgroundConfig(t testing.TB, path string, definitions ...BackgroundCommandDefinition) {
	t.Helper()
	if definitions == nil {
		definitions = []BackgroundCommandDefinition{}
	}
	data, err := json.Marshal(BackgroundCommandConfig{Version: 1, Commands: definitions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func waitBackgroundState(t testing.TB, manager BackgroundCommandManager, id string, states ...string) BackgroundSession {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var session BackgroundSession
	for time.Now().Before(deadline) {
		var err error
		session, err = manager.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if session.State == state {
				return session
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session = %+v, want state in %v", session, states)
	return BackgroundSession{}
}

func waitBackgroundOutput(t testing.TB, manager BackgroundCommandManager, id, want string) BackgroundSessionOutput {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var output BackgroundSessionOutput
	for time.Now().Before(deadline) {
		var err error
		output, err = manager.Output(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.Stdout, want) {
			return output
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output = %+v, want %q", output, want)
	return BackgroundSessionOutput{}
}

func exerciseBackgroundCommandSupport(t testing.TB) {
	t.Helper()
	exerciseBackgroundConfig(t)
	exerciseBackgroundExactArgvAndReload(t)
	exerciseBackgroundLifecycle(t)
	exerciseBackgroundCompletionTime(t)
	exerciseBackgroundCloseDeadline(t)
	exerciseBackgroundCanceledStart(t)
	exerciseBackgroundSignalShutdown(t)
}

func exerciseBackgroundCompletionTime(t testing.TB) {
	t.Helper()
	startedAt := time.Now().UTC()
	process, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("report")})
	if err != nil {
		t.Fatal(err)
	}
	var execution BackgroundExecution
	select {
	case execution = <-process.Done:
	case <-time.After(6 * time.Second):
		process.Stop()
		t.Fatal("process did not finish")
	}
	if execution.FinishedAt.Before(startedAt) || execution.FinishedAt.After(time.Now().UTC()) {
		t.Fatalf("invalid process finish time: %v", execution.FinishedAt)
	}
	time.Sleep(50 * time.Millisecond)
	completed := make(chan BackgroundExecution, 1)
	completed <- execution
	sessions := map[string]managedBackgroundSession{
		"completed": {process: BackgroundProcess{Done: completed}},
	}
	refreshManagedBackgroundSessions(sessions)
	if got := sessions["completed"].session.FinishedAt; !got.Equal(execution.FinishedAt) {
		t.Fatalf("session finish time = %v, process finished at %v", got, execution.FinishedAt)
	}
}

func exerciseBackgroundCloseDeadline(t testing.TB) {
	t.Helper()
	stopped := make(chan string, 2)
	sessions := map[string]managedBackgroundSession{
		"first":  {process: BackgroundProcess{Stop: func() { stopped <- "first" }, Done: make(chan BackgroundExecution)}},
		"second": {process: BackgroundProcess{Stop: func() { stopped <- "second" }, Done: make(chan BackgroundExecution)}},
	}
	done := make(chan struct{})
	go func() {
		closeManagedBackgroundSessions(sessions, 50*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager shutdown exceeded its deadline")
	}
	if len(stopped) != len(sessions) {
		t.Fatalf("shutdown stopped %d processes, want %d", len(stopped), len(sessions))
	}
}

func exerciseBackgroundConfig(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "new", "background-commands.json")
	config, err := LoadBackgroundCommandConfig(path)
	if err != nil || config.Version != 1 || len(config.Commands) != 0 {
		t.Fatalf("default config = %+v, error = %v", config, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(appconfig.BackgroundCommands) {
		t.Fatalf("installed template = %q, error = %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("config file permissions = %v, %v; want 0600", info, err)
	}

	executable, err := json.Marshal(backgroundTestExecutable(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"name":"server","executable":` + string(executable) + `,"args":[]}`
	tests := []struct{ name, data string }{
		{"unknown config field", `{"version":1,"commands":[],"enabled":true}`},
		{"duplicate field", `{"version":1,"version":1,"commands":[]}`},
		{"unsupported version", `{"version":2,"commands":[]}`},
		{"duplicate name", `{"version":1,"commands":[` + valid + `,` + valid + `]}`},
		{"legacy shell command", `{"version":1,"commands":[{"name":"server","command":"echo ready"}]}`},
		{"shell fragment instead of executable", `{"version":1,"commands":[{"name":"server","executable":"echo ready; true","args":[]}]}`},
		{"missing arguments", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `}]}`},
		{"arguments are shell text", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":"one two"}]}`},
		{"nonstring argument", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[1]}]}`},
		{"NUL argument", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":["\u0000"]}]}`},
		{"invalid name", `{"version":1,"commands":[{"name":"../server","executable":` + string(executable) + `,"args":[]}]}`},
		{"unknown entry field", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"command":"echo ready"}]}`},
	}
	for _, test := range tests {
		if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBackgroundCommandConfig(path); err == nil {
			t.Fatalf("accepted invalid configuration: %s", test.name)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBackgroundCommandConfig(path); err == nil {
			t.Fatal("accepted a non-private config")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(directory, "missing-target"), path); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBackgroundCommandConfig(path); err == nil {
			t.Fatal("accepted a symbolic-link config")
		}
	}
}

func exerciseBackgroundExactArgvAndReload(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	sentinel := filepath.Join(directory, "shell-executed")
	args := []string{"two words", "", `"quotes"`, "$HOME", "; touch " + sentinel, "line\nbreak"}
	executable := backgroundTestExecutable(t)
	definition := BackgroundCommandDefinition{
		Name: "report", Description: "Report arguments", Executable: executable,
		Args: backgroundHelperArgs("report", args...), CWD: directory,
	}
	writeBackgroundConfig(t, path, definition)
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	if _, err := manager.Start(context.Background(), "missing"); err == nil {
		t.Fatal("started an unlisted name")
	}
	configured, err := manager.Configured(context.Background())
	if err != nil || len(configured) != 1 || configured[0] != (BackgroundCommandInfo{Name: "report", Description: "Report arguments"}) {
		t.Fatalf("configured = %+v, error = %v", configured, err)
	}
	session, err := manager.Start(context.Background(), "report")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" || finished.ExitCode != 0 || finished.FinishedAt.IsZero() {
		t.Fatalf("finished = %+v", finished)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir, err := filepath.EvalSymlinks(directory)
	expected, encodeErr := json.Marshal(struct {
		Args []string `json:"args"`
		Dir  string   `json:"dir"`
	}{args, resolvedDir})
	if err != nil || encodeErr != nil || strings.TrimSpace(output.Stdout) != string(expected) {
		t.Fatalf("child output = %q; want %s (errors %v, %v)", output.Stdout, expected, err, encodeErr)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("shell fragment was executed or could not be checked: %v", err)
	}

	definition.Args = backgroundHelperArgs("report", "changed argument")
	writeBackgroundConfig(t, path, definition)
	second, err := manager.Start(context.Background(), "report")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundState(t, manager, second.ID, "exited")
	output, err = manager.Output(context.Background(), second.ID)
	if err != nil || !strings.Contains(output.Stdout, "changed argument") {
		t.Fatalf("reloaded output = %+v, error = %v", output, err)
	}
	writeBackgroundConfig(t, path)
	configured, err = manager.Configured(context.Background())
	if err != nil || len(configured) != 0 {
		t.Fatalf("reloaded allowlist = %+v, error = %v", configured, err)
	}
	if _, err := manager.Start(context.Background(), "report"); err == nil {
		t.Fatal("started a command removed from the allowlist")
	}
	if _, err := manager.Get(context.Background(), session.ID); err != nil {
		t.Fatalf("removing an allowlist entry lost its existing session: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), "report"); err == nil {
		t.Fatal("started while configuration was invalid")
	}
}

func exerciseBackgroundLifecycle(t testing.TB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background-commands.json")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path,
		BackgroundCommandDefinition{Name: "hold", Executable: executable, Args: backgroundHelperArgs("hold")},
		BackgroundCommandDefinition{Name: "fail", Executable: executable, Args: backgroundHelperArgs("fail")},
		BackgroundCommandDefinition{Name: "overflow", Executable: executable, Args: backgroundHelperArgs("overflow")},
		BackgroundCommandDefinition{Name: "open-output", Executable: executable, Args: backgroundHelperArgs("open-output")},
	)
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	ctx := context.Background()
	failed, err := manager.Start(ctx, "fail")
	if err != nil {
		t.Fatal(err)
	}
	failed = waitBackgroundState(t, manager, failed.ID, "exited", "failed")
	if failed.State != "exited" || failed.ExitCode != 7 {
		t.Fatalf("nonzero exit = %+v", failed)
	}
	output, err := manager.Output(ctx, failed.ID)
	if err != nil || output.Stderr != "expected failure" {
		t.Fatalf("nonzero exit output = %+v, error = %v", output, err)
	}
	limited, err := manager.Start(ctx, "overflow")
	if err != nil {
		t.Fatal(err)
	}
	limited = waitBackgroundState(t, manager, limited.ID, "output_limited", "exited", "failed")
	output, err = manager.Output(ctx, limited.ID)
	if err != nil || limited.State != "output_limited" || !output.Session.StdoutTruncated || len(output.Stdout) != MaxOutputBytes {
		t.Fatalf("output-limited session = %+v, captured = %d, error = %v", limited, len(output.Stdout), err)
	}
	if runtime.GOOS != "windows" {
		incomplete, err := manager.Start(ctx, "open-output")
		if err != nil {
			t.Fatal(err)
		}
		incomplete = waitBackgroundState(t, manager, incomplete.ID, "failed", "exited")
		if incomplete.State != "failed" || incomplete.ExitCode != 0 {
			t.Fatalf("session with incomplete output = %+v", incomplete)
		}
	}
	running, err := manager.Start(ctx, "hold")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, running.ID, "session-ready")
	if _, err := manager.Stop(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	stopped := waitBackgroundState(t, manager, running.ID, "stopped", "failed")
	if stopped.State != "stopped" || stopped.FinishedAt.IsZero() {
		t.Fatalf("stopped session = %+v", stopped)
	}
	if _, err := manager.Output(ctx, "unknown"); err == nil {
		t.Fatal("accepted an unknown session")
	}
	for i := 0; i < 2; i++ {
		session, err := manager.Start(ctx, "hold")
		if err != nil {
			t.Fatal(err)
		}
		waitBackgroundOutput(t, manager, session.ID, "session-ready")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx, "hold"); err == nil {
		t.Fatal("closed manager accepted a start")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func exerciseBackgroundSignalShutdown(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	marker := filepath.Join(directory, "survived")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{Name: "worker", Executable: executable, Args: backgroundHelperArgs("waitfile", marker)})
	host := exec.Command(executable, backgroundHelperArgs("signal-host", path)...)
	stdout, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer host.Process.Kill()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if strings.TrimSpace(line) != "host-ready" {
			t.Fatalf("host reported %q", line)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("host did not start its child")
	}
	if err := host.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Wait() }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("host did not terminate")
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived host termination or marker could not be checked: %v", err)
	}
}

func exerciseBackgroundCanceledStart(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	marker := filepath.Join(directory, "ready")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name: "worker", Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("mark-ready", marker),
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Withhold the reply to simulate a caller disappearing after the child starts.
	manager.requests <- backgroundManagerRequest{action: "start", name: "worker", ctx: ctx, response: make(chan backgroundManagerResponse)}
	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	sessions, err := manager.List(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions after cancellation = %+v, error = %v", sessions, err)
	}
	stopped := waitBackgroundState(t, manager, sessions[0].ID, "stopped", "failed")
	if stopped.State != "stopped" {
		t.Fatalf("canceled start left session = %+v", stopped)
	}
}
