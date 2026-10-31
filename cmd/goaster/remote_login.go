package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	remoteAgentPath      = "/usr/local/bin/goaster-agent"
	remoteLoginCommand   = remoteAgentPath + " login"
	remoteHealthCommand  = "curl --fail --silent --show-error --max-time 10 http://127.0.0.1:8765/healthz"
	remoteLoginTimeout   = 20 * time.Minute
	remoteHealthTimeout  = 30 * time.Second
	remoteConnectTimeout = 10
)

type remoteLoginOptions struct {
	Target         string
	IdentityFile   string
	KnownHostsFile string
	Port           int
}

type sshRunner func(context.Context, []string, io.Reader, io.Writer, io.Writer) error

func runRemoteLogin(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer, sshRun sshRunner) error {
	options, err := parseRemoteLogin(args, errorOutput)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	return runRemoteLoginOnTerminal(ctx, options, openControllingTerminal, sshRun)
}

func parseRemoteLogin(args []string, output io.Writer) (remoteLoginOptions, error) {
	if output == nil {
		output = io.Discard
	}
	options := remoteLoginOptions{Port: 22}
	flags := flag.NewFlagSet("goaster remote-login", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: goaster remote-login --target USER@HOST --key PATH [options]")
		fmt.Fprintln(output, "This is an internal command for exceptional use. Normally, ask GoAster in chat to set up or sign in to the remote agent.")
		flags.PrintDefaults()
	}
	flags.StringVar(&options.Target, "target", "", "SSH target for the remote agent service account (required)")
	flags.StringVar(&options.IdentityFile, "key", "", "local SSH private-key path (required)")
	flags.StringVar(&options.IdentityFile, "identity", "", "alias for --key")
	flags.StringVar(&options.KnownHostsFile, "known-hosts", "", "optional known_hosts file; strict verification is always enabled")
	flags.IntVar(&options.Port, "port", options.Port, "SSH port")
	if err := flags.Parse(args); err != nil {
		return remoteLoginOptions{}, err
	}
	if flags.NArg() != 0 {
		return remoteLoginOptions{}, fmt.Errorf("unexpected remote-login arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := validateRemoteLoginOptions(options); err != nil {
		return remoteLoginOptions{}, err
	}
	return options, nil
}

func validateRemoteLoginOptions(options remoteLoginOptions) error {
	if strings.TrimSpace(options.Target) == "" {
		return errors.New("remote-login requires --target USER@HOST")
	}
	if hasWhitespaceOrControl(options.Target) || strings.HasPrefix(options.Target, "-") {
		return errors.New("remote-login target must be a single SSH destination")
	}
	if strings.TrimSpace(options.IdentityFile) == "" {
		return errors.New("remote-login requires --key PATH")
	}
	if strings.IndexByte(options.IdentityFile, 0) >= 0 {
		return errors.New("remote-login key path contains an invalid NUL byte")
	}
	if strings.IndexByte(options.KnownHostsFile, 0) >= 0 {
		return errors.New("remote-login known-hosts path contains an invalid NUL byte")
	}
	if options.Port < 1 || options.Port > 65535 {
		return errors.New("remote-login port must be between 1 and 65535")
	}
	return nil
}

func hasWhitespaceOrControl(value string) bool {
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func runRemoteLoginWith(ctx context.Context, options remoteLoginOptions, input io.Reader, output, errorOutput io.Writer, sshRun sshRunner) error {
	if ctx == nil {
		return errors.New("remote-login context must not be nil")
	}
	if sshRun == nil {
		return errors.New("remote-login SSH runner is unavailable")
	}
	if err := validateRemoteLoginOptions(options); err != nil {
		return err
	}
	if input == nil {
		input = strings.NewReader("")
	}
	if output == nil {
		output = io.Discard
	}
	if errorOutput == nil {
		errorOutput = io.Discard
	}

	loginContext, cancelLogin := context.WithTimeout(ctx, remoteLoginTimeout)
	loginErr := sshRun(loginContext, sshArguments(options, true, remoteLoginCommand), input, output, errorOutput)
	loginContextErr := loginContext.Err()
	cancelLogin()
	if loginErr != nil || loginContextErr != nil {
		return sshFailure("remote agent login", loginContextErr, loginErr)
	}

	healthContext, cancelHealth := context.WithTimeout(ctx, remoteHealthTimeout)
	healthErr := sshRun(healthContext, sshArguments(options, false, remoteHealthCommand), input, output, errorOutput)
	healthContextErr := healthContext.Err()
	cancelHealth()
	if healthErr != nil || healthContextErr != nil {
		return sshFailure("remote agent health check", healthContextErr, healthErr)
	}
	if err := persistRemoteTargetAfterVerification(options); err != nil {
		_, _ = fmt.Fprintln(output, "Remote agent login and health check succeeded, but the local server connection could not be saved.")
		return fmt.Errorf("save verified remote connection: %w", err)
	}
	if _, err := fmt.Fprintln(output, "Remote agent login and health check completed."); err != nil {
		return fmt.Errorf("report remote login result: %w", err)
	}
	return nil
}

func sshArguments(options remoteLoginOptions, allocateTTY bool, remoteCommand string) []string {
	ttyOption := "-T"
	if allocateTTY {
		ttyOption = "-tt"
	}
	arguments := []string{
		ttyOption,
		"-i", options.IdentityFile,
		"-p", strconv.Itoa(options.Port),
		"-o", "BatchMode=no",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PreferredAuthentications=publickey",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "ConnectTimeout=" + strconv.Itoa(remoteConnectTimeout),
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if options.KnownHostsFile != "" {
		arguments = append(arguments,
			"-o", "GlobalKnownHostsFile=none",
			"-o", "UserKnownHostsFile="+options.KnownHostsFile,
		)
	}
	return append(arguments, "--", options.Target, remoteCommand)
}

func executeSSH(ctx context.Context, arguments []string, input io.Reader, output, errorOutput io.Writer) error {
	command := exec.CommandContext(ctx, "ssh", arguments...)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = errorOutput
	return command.Run()
}

func sshFailure(action string, contextErr, commandErr error) error {
	if errors.Is(contextErr, context.DeadlineExceeded) {
		if action == "remote agent login" {
			return fmt.Errorf("%s timed out after 20 minutes", action)
		}
		return fmt.Errorf("%s timed out after 30 seconds", action)
	}
	if errors.Is(contextErr, context.Canceled) {
		return fmt.Errorf("%s canceled", action)
	}
	if commandErr == nil {
		return fmt.Errorf("%s failed; see SSH output for details", action)
	}
	detail := strings.TrimSpace(commandErr.Error())
	if strings.HasPrefix(detail, "exit status ") && len(detail) <= 32 {
		return fmt.Errorf("%s failed: SSH %s", action, detail)
	}
	if strings.Contains(detail, "executable file not found") {
		return fmt.Errorf("%s failed: OpenSSH was not found", action)
	}
	return fmt.Errorf("%s failed; see SSH output for details", action)
}
