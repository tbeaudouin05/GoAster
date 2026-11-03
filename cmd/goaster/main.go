package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"goaster/internal/artefacts"
	"goaster/internal/chat"
	"goaster/internal/command"
	"goaster/internal/localenv"
	"goaster/internal/scheduler"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "goaster:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	return runWithSSH(ctx, args, input, output, errorOutput, executeSSH)
}

func runWithSSH(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer, sshRun sshRunner) error {
	if len(args) > 0 && args[0] == "remote-login" {
		return runRemoteLogin(ctx, args[1:], input, output, errorOutput, sshRun)
	}
	runtimeConfig, err := chat.ParseRuntimeConfig(args, errorOutput)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	if _, err := chat.NewMemoryStoreWithError(""); err != nil {
		return fmt.Errorf("initialize Markdown memory store: %w", err)
	}
	if err := artefacts.InstallStarters(ctx); err != nil {
		return fmt.Errorf("install starter skills: %w", err)
	}

	environmentStore, err := localenv.NewStore()
	if err != nil {
		return err
	}
	if err := environmentStore.Load(); err != nil {
		return err
	}

	store, err := chat.NewCredentialStore(runtimeConfig.AuthFile)
	if err != nil {
		return err
	}
	codexProvider := chat.NewCodexProvider(store)
	if err := codexProvider.EnsureAuth(ctx, output); err != nil {
		return err
	}
	terminalInput := chat.NewTerminalInput(input)
	toolTerminalFallback := func() (io.ReadWriteCloser, error) {
		return openInheritedTerminal(input, output)
	}
	if err := registerProcessHandoffTool(codexProvider, newProtectedProcessHandoffWithFallback(openControllingTerminal, toolTerminalFallback, sshRun)); err != nil {
		return fmt.Errorf("register process handoff tool: %w", err)
	}
	backgroundManager, err := command.NewBackgroundCommandManager()
	if err != nil {
		return err
	}
	stopSignalHandler := backgroundManager.CloseOnSignal()
	defer stopSignalHandler()
	defer backgroundManager.Close()
	if err := chat.RegisterBackgroundCommandTool(codexProvider, backgroundManager); err != nil {
		return fmt.Errorf("register background command tool: %w", err)
	}
	registry, err := chat.NewModelRegistry("")
	if err != nil {
		return fmt.Errorf("initialize model registry: %w", err)
	}
	runtimeConfig.Config.Registry = registry
	session, err := chat.NewPersistentSession(runtimeConfig.Config, codexProvider)
	if err != nil {
		return fmt.Errorf("initialize conversation session: %w", err)
	}
	cronScheduler, err := scheduler.New(scheduler.Options{
		Executor: scheduledExecutor(session, environmentStore.Values()),
	})
	if err != nil {
		return fmt.Errorf("initialize cron scheduler: %w", err)
	}
	if err := chat.RegisterCronTool(codexProvider, cronScheduler); err != nil {
		return fmt.Errorf("register cron tool: %w", err)
	}
	if err := cronScheduler.Start(ctx); err != nil {
		return fmt.Errorf("start cron scheduler: %w", err)
	}
	defer cronScheduler.Stop()
	return chat.RunWithEnvStore(ctx, terminalInput, output, session, environmentStore)
}

func scheduledExecutor(session *chat.Session, environment map[string]string) scheduler.Executor {
	return func(ctx context.Context, message string) error {
		_, err := session.ReplyScheduled(chat.WithShellEnvironment(ctx, environment), message)
		if errors.Is(err, chat.ErrSessionBusy) {
			return scheduler.ErrRetry
		}
		return err
	}
}
