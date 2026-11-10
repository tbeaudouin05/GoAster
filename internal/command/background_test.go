package command

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	case "fork-exit":
		_ = os.WriteFile(args[0], []byte("parent launched"), 0600)
		child := exec.Command(os.Args[0], backgroundHelperArgs("waitfile", args[0]+"-child")...)
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		os.Exit(0)
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
	case "interactive-input":
		fmt.Print("input-ready")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("input-received:%s", strings.TrimSuffix(line, "\n"))
	case "interactive-closed-input":
		_ = os.Stdin.Close()
		fmt.Print("input-closed")
		time.Sleep(1500 * time.Millisecond)
	case "interactive-no-read":
		fmt.Print("input-ready")
		time.Sleep(1500 * time.Millisecond)
	case "pty-key":
		fmt.Print("pty-key-ready")
		key, err := bufio.NewReader(os.Stdin).ReadByte()
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("pty-key-received:%d", key)
	case "pty-exit-output":
		fmt.Print(strings.Repeat("x", 8192))
		fmt.Print("pty-exit-output")
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
		backgroundPlatformHelper(os.Args[index+1], args)
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
	exerciseBackgroundCloseDeadline(t)
	exerciseBackgroundKeyEncoding(t)
	exerciseBackgroundPlatformLifecycle(t)
	if !backgroundLifecycleSupported() {
		// Runtime integration cases below require containment. Assert refusal
		// before launch instead; never bypass the production safety gate.
		exerciseBackgroundPTYInput(t)
		return
	}
	exerciseBackgroundExactArgvAndReload(t)
	exerciseBackgroundLifecycle(t)
	exerciseBackgroundInteractiveInput(t)
	exerciseBackgroundInputDoesNotBlockManager(t)
	exerciseBackgroundInputWriteFailure(t)
	exerciseBackgroundClosedInputReported(t)
	exerciseBackgroundPTYInput(t)
	exerciseBackgroundCompletionTime(t)
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
	done := make(chan error, 1)
	go func() {
		done <- closeManagedBackgroundSessions(sessions, 50*time.Millisecond)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("shutdown concealed deadline failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager shutdown exceeded its deadline")
	}
	if len(stopped) != len(sessions) {
		t.Fatalf("shutdown stopped %d processes, want %d", len(stopped), len(sessions))
	}
	manager := BackgroundCommandManager{requests: make(chan backgroundManagerRequest), done: make(chan struct{})}
	cleanupFailure := errors.New("cleanup failed during close")
	go func() {
		request := <-manager.requests
		request.response <- backgroundManagerResponse{err: cleanupFailure}
		close(manager.done)
	}()
	if err := manager.Close(); !errors.Is(err, cleanupFailure) {
		t.Fatalf("Close hid cleanup failure: %v", err)
	}
	<-manager.done

	for _, failure := range []error{cleanupFailure, errors.Join(context.Canceled, cleanupFailure), errors.Join(ErrOutputLimit, cleanupFailure)} {
		for _, refreshed := range []bool{false, true} {
			completed := make(chan BackgroundExecution, 1)
			completed <- BackgroundExecution{Err: failure, FinishedAt: time.Now().UTC()}
			close(completed)
			sessions := map[string]managedBackgroundSession{"failed": {stopRequested: true, process: BackgroundProcess{Stop: func() {}, Done: completed}}}
			if refreshed {
				refreshManagedBackgroundSessions(sessions)
			}
			if err := closeManagedBackgroundSessions(sessions, time.Second); !errors.Is(err, cleanupFailure) {
				t.Fatalf("shutdown concealed failure after refresh=%v: %v", refreshed, err)
			}
			if got := sessions["failed"].session; got.State != "failed" || got.Error == "" {
				t.Fatalf("cleanup failure lost: %+v", got)
			}
			if err := closeManagedBackgroundSessions(sessions, time.Second); !errors.Is(err, cleanupFailure) {
				t.Fatalf("repeated shutdown concealed failure: %v", err)
			}
		}
	}
	// Repeated/concurrent Close callers must all observe the same result.
	manager = BackgroundCommandManager{requests: make(chan backgroundManagerRequest), done: make(chan struct{}), closeResult: make(chan error, 1)}
	manager.closeResult <- cleanupFailure
	close(manager.done)
	results := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() { results <- manager.Close() }()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; !errors.Is(err, cleanupFailure) {
			t.Fatalf("Close lost result: %v", err)
		}
	}
	writeFailure := errors.New("diagnostic output failed")
	if err := reportBackgroundShutdown(cleanupFailure, outputWriterFunc(func(data []byte) (int, error) { return 0, writeFailure })); !errors.Is(err, cleanupFailure) || !errors.Is(err, writeFailure) {
		t.Fatalf("shutdown diagnostic lost errors: %v", err)
	}
	if err := reportBackgroundShutdown(cleanupFailure, outputWriterFunc(func(data []byte) (int, error) { return 0, nil })); !errors.Is(err, cleanupFailure) || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("shutdown diagnostic lost short write: %v", err)
	}
	closed := make(chan BackgroundExecution)
	close(closed)
	missing := map[string]managedBackgroundSession{"missing": {process: BackgroundProcess{Done: closed}}}
	refreshManagedBackgroundSessions(missing)
	if err := closeManagedBackgroundSessions(missing, time.Second); err == nil {
		t.Fatal("missing completion result was treated as clean")
	}
	retained := map[string]managedBackgroundSession{"failed": {finished: true, err: cleanupFailure}}
	order := []string{"failed"}
	for i := 1; i < maxBackgroundSessions; i++ {
		id := fmt.Sprint(i)
		retained[id] = managedBackgroundSession{finished: true}
		order = append(order, id)
	}
	if _, err := makeBackgroundSessionRoom(retained, order); err != nil {
		t.Fatal(err)
	}
	if _, ok := retained["failed"]; !ok {
		t.Fatal("session eviction discarded a cleanup failure")
	}
	if err := closeManagedBackgroundSessions(retained, time.Second); !errors.Is(err, cleanupFailure) {
		t.Fatalf("eviction hid cleanup error: %v", err)
	}
	for _, timeout := range []bool{false, true} {
		readFailure := errors.New("PTY output read failed")
		closeFailure := errors.New("PTY output close failed")
		readDone := make(chan error, 1)
		if !timeout {
			readDone <- readFailure
			close(readDone)
		}
		terminal := backgroundOutputTestTerminal{close: func() error {
			if timeout {
				readDone <- readFailure
				close(readDone)
			}
			return closeFailure
		}}
		if err := waitForBackgroundPTYOutput(terminal, readDone); !errors.Is(err, readFailure) || !errors.Is(err, closeFailure) || (timeout && !strings.Contains(err.Error(), "deadline")) {
			t.Fatalf("PTY output lost errors (timeout=%v): %v", timeout, err)
		}
	}

	started := time.Now()
	if err := waitForBackgroundPTYOutput(backgroundOutputTestTerminal{close: func() error { return nil }}, make(chan error)); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("unfinished output reader reported success: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("output error handling exceeded its bound")
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
	interactive := `{"name":"interactive","executable":` + string(executable) + `,"args":[],"interactive":true,"pty":true}`
	config, err = LoadBackgroundCommandConfig(path)
	if err != nil || config.Version != 1 || len(config.Commands) != 0 {
		t.Fatalf("initial background config = %+v, error = %v", config, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"commands":[`+interactive+`]}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err = LoadBackgroundCommandConfig(path)
	if err != nil || len(config.Commands) != 1 || !config.Commands[0].Interactive || !config.Commands[0].PTY {
		t.Fatalf("interactive background config = %+v, error = %v", config, err)
	}
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
		{"interactive flag is not boolean", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"interactive":"yes"}]}`},
		{"pty flag is not boolean", `{"version":1,"commands":[{"name":"server","executable":` + string(executable) + `,"args":[],"pty":1}]}`},
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

func exerciseBackgroundInteractiveInput(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path,
		BackgroundCommandDefinition{
			Name: "input", Executable: executable, Args: backgroundHelperArgs("interactive-input"), Interactive: true,
		},
		BackgroundCommandDefinition{Name: "plain", Executable: executable, Args: backgroundHelperArgs("hold")},
	)
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()

	configured, err := manager.Configured(context.Background())
	if err != nil || len(configured) != 2 || !configured[0].Interactive && !configured[1].Interactive {
		t.Fatalf("configured interactive commands = %+v, error = %v", configured, err)
	}

	interactive, err := manager.Start(context.Background(), "input")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, interactive.ID, "input-ready")
	if _, err := manager.Input(context.Background(), interactive.ID, "hello\n"); err != nil {
		t.Fatalf("interactive input error = %v", err)
	}
	finished := waitBackgroundState(t, manager, interactive.ID, "exited", "failed")
	if finished.State != "exited" || finished.ExitCode != 0 {
		captured, _ := manager.Output(context.Background(), interactive.ID)
		t.Fatalf("interactive session = %+v, output = %+v", finished, captured)
	}
	output, err := manager.Output(context.Background(), interactive.ID)
	if err != nil || !strings.Contains(output.Stdout, "input-received:hello") {
		t.Fatalf("interactive output = %+v, error = %v", output, err)
	}
	if _, err := manager.Input(context.Background(), interactive.ID, "late\n"); !errors.Is(err, ErrBackgroundSessionDone) {
		t.Fatalf("late input error = %v, want ErrBackgroundSessionDone", err)
	}

	plain, err := manager.Start(context.Background(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, plain.ID, "session-ready")
	if _, err := manager.Input(context.Background(), plain.ID, "not-accepted\n"); !errors.Is(err, ErrBackgroundSessionNotInteractive) {
		t.Fatalf("noninteractive input error = %v, want ErrBackgroundSessionNotInteractive", err)
	}
	if _, err := manager.SendKeys(context.Background(), interactive.ID, "enter"); !errors.Is(err, ErrBackgroundSessionDone) {
		t.Fatalf("non-PTY key error = %v, want completed session error", err)
	}
	if _, err := manager.Stop(context.Background(), plain.ID); err != nil {
		t.Fatal(err)
	}
	waitBackgroundState(t, manager, plain.ID, "stopped", "failed")
}

func exerciseBackgroundInputDoesNotBlockManager(t testing.TB) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	executable := backgroundTestExecutable(t)
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name: "no-read", Executable: executable, Args: backgroundHelperArgs("interactive-no-read"), Interactive: true,
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "no-read")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, session.ID, "input-ready")

	data := strings.Repeat("x", MaxSessionInputBytes)
	timedOut := false
	busySeen := false
	for index := 0; index < backgroundInputQueueSize+32; index++ {
		result := make(chan error, 1)
		go func() {
			_, inputErr := manager.Input(context.Background(), session.ID, data)
			result <- inputErr
		}()
		select {
		case inputErr := <-result:
			if errors.Is(inputErr, ErrBackgroundSessionInputBusy) {
				busySeen = true
			}
		case <-time.After(300 * time.Millisecond):
			timedOut = true
			break
		}
		if timedOut {
			break
		}
	}
	if !timedOut {
		statusDone := make(chan error, 1)
		go func() {
			_, statusErr := manager.Get(context.Background(), session.ID)
			statusDone <- statusErr
		}()
		select {
		case err := <-statusDone:
			if err != nil {
				t.Fatalf("status after queued input = %v", err)
			}
		case <-time.After(300 * time.Millisecond):
			timedOut = true
		}
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" {
		t.Fatalf("non-reading input session = %+v", finished)
	}
	if !busySeen {
		t.Fatal("unbounded input queue accepted every write")
	}
	if timedOut {
		t.Fatal("manager blocked on input to a child that was not reading")
	}
}

func exerciseBackgroundPTYInput(t testing.TB) {
	t.Helper()
	if !backgroundPTYSupported() {
		marker := filepath.Join(t.TempDir(), "pty-started")
		_, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("mark-ready", marker), PTY: true})
		if !errors.Is(err, ErrPTYNotSupported) {
			t.Fatalf("unsupported PTY: %v", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported PTY launched a child: %v", err)
		}
		return
	}
	executable := backgroundTestExecutable(t)
	probe, err := StartBackground(BackgroundRequest{
		Executable:  executable,
		Args:        backgroundHelperArgs("pty-key"),
		Interactive: true,
		PTY:         true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("PTY probe error = %v", err)
	}
	probe.Stop()
	select {
	case <-probe.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("PTY probe did not clean up")
	}
	outputProcess, err := StartBackground(BackgroundRequest{
		Executable: executable,
		Args:       backgroundHelperArgs("pty-exit-output"),
		PTY:        true,
	})
	if errors.Is(err, ErrPTYNotSupported) {
		return
	}
	if err != nil {
		t.Fatalf("PTY output process error = %v", err)
	}
	select {
	case execution := <-outputProcess.Done:
		if !strings.Contains(execution.Result.Stdout, "pty-exit-output") {
			t.Fatalf("PTY output after exit = %q", execution.Result.Stdout)
		}
	case <-time.After(3 * time.Second):
		outputProcess.Stop()
		t.Fatal("PTY output process did not finish")
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{
		Name: "pty", Executable: executable, Args: backgroundHelperArgs("pty-key"), PTY: true,
	})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "pty")
	if err != nil {
		t.Fatal(err)
	}
	if !session.Interactive || !session.PTY {
		t.Fatalf("PTY session metadata = %+v", session)
	}
	waitBackgroundOutput(t, manager, session.ID, "pty-key-ready")
	if _, err := manager.SendKeys(context.Background(), session.ID, "enter"); err != nil {
		t.Fatalf("send PTY key error = %v", err)
	}
	finished := waitBackgroundState(t, manager, session.ID, "exited", "failed")
	if finished.State != "exited" || finished.ExitCode != 0 {
		t.Fatalf("PTY session = %+v", finished)
	}
	output, err := manager.Output(context.Background(), session.ID)
	if err != nil || !strings.Contains(output.Stdout, "pty-key-received:") {
		t.Fatalf("PTY output = %+v, error = %v", output, err)
	}
}

func exerciseBackgroundKeyEncoding(t testing.TB) {
	t.Helper()
	cases := []struct {
		name string
		keys string
		mode BackgroundPtyKeyMode
		want string
	}{
		{name: "named key", keys: "enter", mode: BackgroundPtyKeyModeCSI, want: "\r"},
		{name: "control key", keys: "ctrl-c", mode: BackgroundPtyKeyModeCSI, want: "\x03"},
		{name: "CSI arrow", keys: "up", mode: BackgroundPtyKeyModeCSI, want: "\x1b[A"},
		{name: "SS3 arrow", keys: "up", mode: BackgroundPtyKeyModeSS3, want: "\x1bOA"},
	}
	for _, fixture := range cases {
		got, err := EncodeBackgroundKeySequence(fixture.keys, fixture.mode)
		if err != nil || got != fixture.want {
			t.Fatalf("%s encoded key = %q, error = %v, want %q", fixture.name, got, err, fixture.want)
		}
	}
	for _, keys := range []string{"", "unknown", "ctrl-", "alt-xx", ",,,"} {
		if _, err := EncodeBackgroundKeySequence(keys, BackgroundPtyKeyModeCSI); err == nil {
			t.Errorf("accepted invalid key sequence %q", keys)
		}
	}

	detector := backgroundPtyKeyModeDetector{}
	var detected BackgroundPtyKeyMode
	detector, detected = observeBackgroundPtyKeyMode(detector, []byte("\x1b[?1h"+strings.Repeat("x", 64)))
	if detected != BackgroundPtyKeyModeSS3 {
		t.Fatalf("long PTY mode sequence detected as %d, want SS3", detected)
	}
	detector = backgroundPtyKeyModeDetector{}
	detector, detected = observeBackgroundPtyKeyMode(detector, []byte("\x1b[?1"))
	if detected != backgroundPtyKeyModeNotFound {
		t.Fatalf("partial PTY mode sequence detected as %d", detected)
	}
	_, detected = observeBackgroundPtyKeyMode(detector, []byte("h"))
	if detected != BackgroundPtyKeyModeSS3 {
		t.Fatalf("split PTY mode sequence detected as %d, want SS3", detected)
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
		wantState := "failed"
		if runtime.GOOS == "linux" {
			wantState = "exited"
		}
		if incomplete.State != wantState || incomplete.ExitCode != 0 {
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

func exerciseBackgroundInputWriteFailure(t testing.TB) {
	for _, test := range []struct {
		name  string
		write outputWriterFunc
	}{
		{"error", func(p []byte) (int, error) { return 0, errors.New("pipe closed") }},
		{"short", func(p []byte) (int, error) { return len(p) - 1, nil }},
	} {
		func() {
			write, failed, finish := backgroundInputWriter(test.write)
			defer finish()
			if err := write("abc"); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(time.Second)
			for !failed() {
				select {
				case <-deadline:
					t.Fatal("failure not recorded")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if err := write("later"); !errors.Is(err, ErrBackgroundSessionInputUnavailable) {
				t.Fatalf("subsequent write: %v", err)
			}
		}()
	}
}

func exerciseBackgroundClosedInputReported(t testing.TB) {
	path := filepath.Join(t.TempDir(), "background-commands.json")
	writeBackgroundConfig(t, path, BackgroundCommandDefinition{Name: "closed", Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("interactive-closed-input"), Interactive: true})
	manager := NewBackgroundCommandManagerAt(path)
	defer manager.Close()
	session, err := manager.Start(context.Background(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	waitBackgroundOutput(t, manager, session.ID, "input-closed")
	if _, err := manager.Input(context.Background(), session.ID, "control"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		state, err := manager.Get(context.Background(), session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.InputFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("input failure not visible in session")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := manager.Input(context.Background(), session.ID, "later"); !errors.Is(err, ErrBackgroundSessionInputUnavailable) {
		t.Fatalf("subsequent input: %v", err)
	}
}

// Value methods keep output-error fixtures free of pointer declarations.
type backgroundOutputTestTerminal struct{ close func() error }

func (terminal backgroundOutputTestTerminal) Read(data []byte) (int, error)  { return 0, io.EOF }
func (terminal backgroundOutputTestTerminal) Write(data []byte) (int, error) { return len(data), nil }
func (terminal backgroundOutputTestTerminal) Close() error                   { return terminal.close() }

// A fork followed by parent exit invalidates enumeration-based proofs. Refuse
// the entire execution before that race is possible, for pipes and job-control
// PTYs alike. The marker checks cover both direct and manager entry points.
func exerciseBackgroundContainmentRefusal(t testing.TB) {
	t.Helper()
	for _, terminal := range []bool{false, true} {
		marker := filepath.Join(t.TempDir(), "fork-exit")
		args := backgroundHelperArgs("fork-exit", marker)
		want := ErrBackgroundCleanupUnavailable
		if terminal {
			want = ErrPTYNotSupported
		}
		process, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: args, PTY: terminal})
		if !errors.Is(err, want) || process.Done != nil {
			t.Fatalf("unsafe launch accepted: %+v, %v", process, err)
		}
		path := filepath.Join(t.TempDir(), "commands.json")
		writeBackgroundConfig(t, path, BackgroundCommandDefinition{Name: "fork-exit", Executable: backgroundTestExecutable(t), Args: args, PTY: terminal})
		manager := NewBackgroundCommandManagerAt(path)
		session, err := manager.Start(context.Background(), "fork-exit")
		if !errors.Is(err, want) || session.ID != "" {
			t.Fatalf("manager accepted unsafe launch: %+v, %v", session, err)
		}
		sessions, err := manager.List(context.Background())
		if err != nil || len(sessions) != 0 {
			t.Fatalf("refused launch created a session: %+v, %v", sessions, err)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{marker, marker + "-child"} {
			if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused command executed: %s, %v", name, err)
			}
		}
	}
}
