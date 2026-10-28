package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"goaster/internal/chat"
)

func main() {
	args := os.Args[1:]
	var err error
	if len(args) > 0 && args[0] == "login" {
		var authFile string
		authFile, err = chat.ParseLoginArgs(args[1:], os.Stderr)
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		if err == nil {
			var store chat.CredentialStore
			store, err = chat.NewCredentialStore(authFile)
			if err == nil {
				err = chat.NewOpenAIAuthenticator(store).Login(context.Background(), os.Stdout)
			}
		}
	} else {
		var runtimeConfig chat.RuntimeConfig
		runtimeConfig, err = chat.ParseRuntimeConfig(args, os.Stderr)
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		if err == nil {
			var provider chat.Provider
			if runtimeConfig.Offline || runtimeConfig.EchoUnavailableModel != "" {
				provider = chat.EchoProvider{UnavailableModel: runtimeConfig.EchoUnavailableModel}
			} else {
				var store chat.CredentialStore
				store, err = chat.NewCredentialStore(runtimeConfig.AuthFile)
				if err == nil {
					provider = chat.NewCodexProvider(store)
				}
			}
			if err == nil {
				err = chat.Run(context.Background(), os.Stdin, os.Stdout, chat.NewSession(runtimeConfig.Config, provider))
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "goaster:", err)
		os.Exit(1)
	}
}
