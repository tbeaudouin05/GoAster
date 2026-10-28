package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ToolHandler executes a registered tool with the JSON arguments supplied by
// the model. A returned error stops the current reply; recoverable tool errors
// can instead be returned as the result with a nil error.
type ToolHandler func(context.Context, string) (string, error)

// Tool describes an optional capability that can be registered with a chat
// provider. Registration is programmatic; it does not add user-facing
// configuration.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     ToolHandler
}

// ToolRegistrar is the small seam skills can use to provide model tools
// without depending on a concrete provider implementation.
type ToolRegistrar interface {
	RegisterTool(Tool) error
}

type registeredTool struct {
	definition codexTool
	handler    ToolHandler
}

type toolRegistry struct {
	mu     sync.RWMutex
	tools  []registeredTool
	byName map[string]ToolHandler
}

func (r *toolRegistry) registerTool(tool Tool) error {
	return r.register(codexTool{
		Type:        "function",
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  tool.Parameters,
	}, tool.Handler)
}

func (r *toolRegistry) register(definition codexTool, handler ToolHandler) error {
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	if definition.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if definition.Description == "" {
		return fmt.Errorf("tool %q description is required", definition.Name)
	}
	if definition.Parameters == nil {
		return fmt.Errorf("tool %q parameters are required", definition.Name)
	}
	if handler == nil {
		return fmt.Errorf("tool %q handler is required", definition.Name)
	}
	if definition.Type == "" {
		definition.Type = "function"
	}
	if definition.Type != "function" {
		return fmt.Errorf("tool %q has unsupported type %q", definition.Name, definition.Type)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName == nil {
		r.byName = make(map[string]ToolHandler)
	}
	if _, exists := r.byName[definition.Name]; exists {
		return fmt.Errorf("tool %q is already registered", definition.Name)
	}
	r.tools = append(r.tools, registeredTool{definition: definition, handler: handler})
	r.byName[definition.Name] = handler
	return nil
}

func (r *toolRegistry) definitions() []codexTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]codexTool, len(r.tools))
	for i, tool := range r.tools {
		definitions[i] = tool.definition
	}
	return definitions
}

func (r *toolRegistry) handler(name string) (ToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.byName[name]
	return handler, ok
}
