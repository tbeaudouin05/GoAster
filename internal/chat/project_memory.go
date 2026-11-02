package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type projectMemoryRequest struct {
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

type projectMemorySessionKey struct{}

type projectMemorySessionContext struct {
	selected func() string
	status   func() (string, error)
	operate  func(projectMemoryRequest) (string, error)
	required bool
}

var projectMemoryCodexTool = codexTool{
	Type:        "function",
	Name:        "project_memory",
	Description: "Discover, select, create, read, or update the bounded project memory required for the current task.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"discover", "select", "create", "read", "update", "clear"},
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Lowercase project-memory name, without the .md suffix",
				"maxLength":   80,
			},
			"content": map[string]any{
				"type":      "string",
				"maxLength": maxProjectMemoryBytes,
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	},
}

func parseProjectMemoryArguments(arguments string) (projectMemoryRequest, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var request projectMemoryRequest
	if err := decoder.Decode(&request); err != nil {
		return projectMemoryRequest{}, fmt.Errorf("project_memory arguments are malformed: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return projectMemoryRequest{}, errors.New("project_memory arguments contain more than one JSON value")
	}
	request.Action = strings.TrimSpace(request.Action)
	request.Name = strings.TrimSpace(request.Name)
	if request.Action == "" {
		return projectMemoryRequest{}, errors.New("project_memory action is required")
	}
	if len(request.Content) > maxProjectMemoryBytes {
		return projectMemoryRequest{}, fmt.Errorf("project_memory content exceeds %d bytes", maxProjectMemoryBytes)
	}
	switch request.Action {
	case "discover", "clear":
		if request.Name != "" || request.Content != "" {
			return projectMemoryRequest{}, fmt.Errorf("project_memory %s does not accept name or content", request.Action)
		}
	case "select", "create":
		if !validProjectMemoryName(request.Name) {
			return projectMemoryRequest{}, errors.New("project_memory name must use lowercase letters, digits, and hyphens")
		}
	case "read":
		if request.Name != "" && !validProjectMemoryName(request.Name) {
			return projectMemoryRequest{}, errors.New("project_memory name must use lowercase letters, digits, and hyphens")
		}
		if request.Content != "" {
			return projectMemoryRequest{}, errors.New("project_memory read does not accept content")
		}
	case "update":
		if request.Name != "" && !validProjectMemoryName(request.Name) {
			return projectMemoryRequest{}, errors.New("project_memory name must use lowercase letters, digits, and hyphens")
		}
		if strings.TrimSpace(request.Content) == "" {
			return projectMemoryRequest{}, errors.New("project_memory update requires non-empty content")
		}
	default:
		return projectMemoryRequest{}, fmt.Errorf("unsupported project_memory action %q", request.Action)
	}
	return request, nil
}

func executeProjectMemory(ctx context.Context, arguments string) (string, error) {
	request, err := parseProjectMemoryArguments(arguments)
	if err != nil {
		return "", fmt.Errorf("project memory operation failed: %w", err)
	}
	if ctx == nil {
		return "", errors.New("project memory is unavailable")
	}
	session, ok := ctx.Value(projectMemorySessionKey{}).(projectMemorySessionContext)
	if !ok || session.operate == nil {
		return "", errors.New("project memory is unavailable for this session")
	}
	return session.operate(request)
}

func projectMemoryInstructions(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", nil
	}
	session, ok := ctx.Value(projectMemorySessionKey{}).(projectMemorySessionContext)
	if !ok || session.status == nil {
		return "", nil
	}
	return session.status()
}
