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
	PrimaryModel  string
	FallbackModel string
}

type RuntimeConfig struct {
	Config
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
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: goaster [options]")
		flags.PrintDefaults()
		fmt.Fprintln(output, "For Telegram, create a bot with @BotFather and save TELEGRAM_BOT_TOKEN in the local /env file alongside all OVH variables; /env replaces the saved set.")
		fmt.Fprintln(output, "After /ovh succeeds, type SETUP TELEGRAM at its separate confirmation prompt to activate the adapter and pair one private account; provisioning already transfers saved runtime credentials over encrypted SSH stdin.")
	}
	flags.StringVar(&config.PrimaryModel, "primary-model", DefaultPrimaryModel, "primary model name")
	flags.StringVar(&config.FallbackModel, "fallback-model", DefaultFallbackModel, "fallback model name")
	flags.StringVar(&config.AuthFile, "auth-file", "", "path for the local OpenAI subscription credentials")
	if err := flags.Parse(args); err != nil {
		return RuntimeConfig{}, err
	}
	if flags.NArg() != 0 {
		return RuntimeConfig{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	config.PrimaryModel = strings.TrimSpace(config.PrimaryModel)
	config.FallbackModel = strings.TrimSpace(config.FallbackModel)
	config.AuthFile = strings.TrimSpace(config.AuthFile)
	if config.PrimaryModel == "" || config.FallbackModel == "" {
		return RuntimeConfig{}, fmt.Errorf("primary and fallback model names must be non-empty")
	}
	return config, nil
}
