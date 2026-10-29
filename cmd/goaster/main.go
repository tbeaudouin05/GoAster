package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"goaster/internal/chat"
	"goaster/internal/localenv"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "goaster:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	runtimeConfig, err := chat.ParseRuntimeConfig(args, errorOutput)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
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
	session, err := chat.NewPersistentSession(runtimeConfig.Config, codexProvider)
	if err != nil {
		return fmt.Errorf("initialize conversation session: %w", err)
	}
	return chat.RunWithEnvStore(ctx, input, output, session, environmentStore)
}
