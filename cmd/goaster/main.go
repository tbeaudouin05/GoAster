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
	config, err := chat.ParseConfig(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		provider := chat.EchoProvider{UnavailableModel: config.EchoUnavailableModel}
		err = chat.Run(context.Background(), os.Stdin, os.Stdout, chat.NewSession(config, provider))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "goaster:", err)
		os.Exit(1)
	}
}
