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

type RuntimeConfig struct {
	Config
	Offline  bool
	AuthFile string
}

func ParseConfig(args []string, output io.Writer) (Config, error) {
	runtimeConfig, err := ParseRuntimeConfig(args, output)
	return runtimeConfig.Config, err
}

func ParseRuntimeConfig(args []string, output io.Writer) (RuntimeConfig, error) {
	var config RuntimeConfig
	flags := flag.NewFlagSet("goaster", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&config.PrimaryModel, "primary-model", DefaultPrimaryModel, "primary model name")
	flags.StringVar(&config.FallbackModel, "fallback-model", DefaultFallbackModel, "fallback model name")
	flags.StringVar(&config.EchoUnavailableModel, "echo-unavailable-model", "", "simulate this model being unavailable in the local echo provider")
	flags.BoolVar(&config.Offline, "offline", false, "use the local echo provider instead of OpenAI subscription chat")
	flags.StringVar(&config.AuthFile, "auth-file", "", "path for the local OpenAI subscription credentials")
	if err := flags.Parse(args); err != nil {
		return RuntimeConfig{}, err
	}
	if flags.NArg() != 0 {
		return RuntimeConfig{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	config.PrimaryModel = strings.TrimSpace(config.PrimaryModel)
	config.FallbackModel = strings.TrimSpace(config.FallbackModel)
	config.EchoUnavailableModel = strings.TrimSpace(config.EchoUnavailableModel)
	config.AuthFile = strings.TrimSpace(config.AuthFile)
	if config.PrimaryModel == "" || config.FallbackModel == "" {
		return RuntimeConfig{}, fmt.Errorf("primary and fallback model names must be non-empty")
	}
	return config, nil
}

func ParseLoginArgs(args []string, output io.Writer) (string, error) {
	var authFile string
	flags := flag.NewFlagSet("goaster login", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&authFile, "auth-file", "", "path for the local OpenAI subscription credentials")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return strings.TrimSpace(authFile), nil
}
