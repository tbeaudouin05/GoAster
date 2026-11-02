package config

import _ "embed"

// OpenAIModels is the initial OpenAI-only runtime model configuration.
//
//go:embed openai-models.json
var OpenAIModels []byte

// BackgroundCommands is the initial empty background-command allowlist.
//
//go:embed background-commands.json
var BackgroundCommands []byte
