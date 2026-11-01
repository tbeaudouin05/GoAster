package config

import _ "embed"

// OpenAIModels is the initial OpenAI-only runtime model configuration.
//
//go:embed openai-models.json
var OpenAIModels []byte
