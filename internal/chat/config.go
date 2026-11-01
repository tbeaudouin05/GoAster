package chat

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

type Config struct {
	Model    string
	Registry ModelRegistry
	ChatID   int64
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
		fmt.Fprintln(output, "Describe what you want in chat, such as a new OVH server or GoAster installed on an existing one.")
		fmt.Fprintln(output, "Chat commands:")
		fmt.Fprintln(output, "  /env: securely import environment variables.")
		fmt.Fprintln(output, "  /get-env: edit the current env file or get a blank template.")
		fmt.Fprintln(output, "  /file: inspect non-secret files.")
		flags.PrintDefaults()
	}
	flags.StringVar(&config.AuthFile, "auth-file", "", "path for the local OpenAI subscription credentials")
	if err := flags.Parse(args); err != nil {
		return RuntimeConfig{}, err
	}
	if flags.NArg() != 0 {
		return RuntimeConfig{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	config.Model = DefaultModel
	config.AuthFile = strings.TrimSpace(config.AuthFile)
	return config, nil
}
