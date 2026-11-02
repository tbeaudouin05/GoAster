package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"goaster/internal/chat"
)

const githubAuthProviderNotAllowed = "GitHub authentication was not started because the requested provider is not allowlisted."

type authHandoffToolArguments struct {
	Provider string `json:"provider"`
}

func registerGitHubAuthTool(registrar chat.ToolRegistrar, manager githubAuthManager) error {
	if registrar == nil {
		return fmt.Errorf("authentication handoff registrar is unavailable")
	}
	return registrar.RegisterTool(newGitHubAuthTool(manager))
}

func newGitHubAuthTool(manager githubAuthManager) chat.Tool {
	return chat.Tool{
		Name:        githubAuthToolName,
		Description: "Request the fixed GitHub device-login handoff only after the paired user explicitly sent /github-login. The provider must be github. The verification URL and one-time code are delivered directly to that private Telegram chat through an out-of-band event; the model receives only a generic status. Do not provide commands, tokens, passwords, device codes, account IDs, or another provider.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"enum":        []string{githubAuthProvider},
					"description": "The only supported authentication provider: github",
				},
			},
			"required":             []string{"provider"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeGitHubAuthTool(ctx, arguments, manager), nil
		},
	}
}

func executeGitHubAuthTool(ctx context.Context, arguments string, manager githubAuthManager) string {
	if ctx == nil || ctx.Err() != nil {
		return githubAuthFailed
	}
	parsed, err := parseAuthHandoffToolArguments(arguments)
	if err != nil || parsed.Provider != githubAuthProvider {
		return githubAuthProviderNotAllowed
	}
	chatID := chat.ChatIDFromContext(ctx)
	if chatID <= 0 {
		return githubAuthAuthorizationNeeded
	}
	return manager.start(ctx, chatID, chat.AuthHandoffObserverFromContext(ctx))
}

func parseAuthHandoffToolArguments(arguments string) (authHandoffToolArguments, error) {
	trimmed := strings.TrimSpace(arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return authHandoffToolArguments{}, fmt.Errorf("authentication handoff arguments must be a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var parsed authHandoffToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return authHandoffToolArguments{}, fmt.Errorf("decode authentication handoff arguments: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return authHandoffToolArguments{}, fmt.Errorf("authentication handoff arguments contain trailing data")
	}
	return parsed, nil
}
