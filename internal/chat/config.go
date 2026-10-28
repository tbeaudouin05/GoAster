package chat

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

const (
	DefaultPrimaryModel  = "gpt-6.1-sol"
	DefaultFallbackModel = "gpt-5.6-luna"
)

type Config struct {
	PrimaryModel         string
	FallbackModel        string
	EchoUnavailableModel string
}

func ParseConfig(args []string, output io.Writer) (Config, error) {
	var config Config
	flags := flag.NewFlagSet("goaster", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&config.PrimaryModel, "primary-model", DefaultPrimaryModel, "primary model name")
	flags.StringVar(&config.FallbackModel, "fallback-model", DefaultFallbackModel, "fallback model name")
	flags.StringVar(&config.EchoUnavailableModel, "echo-unavailable-model", "", "simulate this model being unavailable in the local echo provider")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	config.PrimaryModel = strings.TrimSpace(config.PrimaryModel)
	config.FallbackModel = strings.TrimSpace(config.FallbackModel)
	config.EchoUnavailableModel = strings.TrimSpace(config.EchoUnavailableModel)
	if config.PrimaryModel == "" || config.FallbackModel == "" {
		return Config{}, fmt.Errorf("primary and fallback model names must be non-empty")
	}
	return config, nil
}
