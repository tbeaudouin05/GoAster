package config

import _ "embed"

// OpenAIModels is the initial OpenAI-only runtime model configuration.
//
//go:embed openai-models.json
var OpenAIModels []byte

// GitHubLoginCommandName is the fixed name of the bundled browser-login
// profile. Existing user-managed command files are never merged with it.
const GitHubLoginCommandName = "github-login"

// BackgroundCommands is the initial background-command allowlist. It contains
// only the fixed GitHub browser-login profile.
//
//go:embed background-commands.json
var BackgroundCommands []byte
