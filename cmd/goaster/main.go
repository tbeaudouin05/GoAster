package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"goaster/internal/chat"
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

	var provider chat.Provider
	if runtimeConfig.Offline || runtimeConfig.EchoUnavailableModel != "" {
		provider = chat.EchoProvider{UnavailableModel: runtimeConfig.EchoUnavailableModel}
	} else {
		store, err := chat.NewCredentialStore(runtimeConfig.AuthFile)
		if err != nil {
			return err
		}
		codexProvider := chat.NewCodexProvider(store)
		provider = codexProvider
		if err := codexProvider.EnsureAuth(ctx, output); err != nil {
			return err
		}
	}
	return chat.Run(ctx, input, output, chat.NewSession(runtimeConfig.Config, provider))
}
