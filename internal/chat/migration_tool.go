package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"goaster/internal/diagnostics"
	"goaster/internal/migration"
)

const migrationToolName = "goaster_migration"

type conversationTypeKey struct{}

// WithConversationType carries transport-provided conversation context.
func WithConversationType(ctx context.Context, kind string) context.Context {
	switch kind {
	case "private", "group", "supergroup":
	default:
		kind = "unknown"
	}
	return context.WithValue(ctx, conversationTypeKey{}, kind)
}

// MigrationToolOperations keeps the model-facing control plane separate from
// the privileged migration executor. No operation accepts a token, key,
// owner URL, chat ID, or callback secret.
type MigrationToolOperations struct {
	Estimate     func(context.Context, migration.PriceRequest) (migration.PriceEstimate, error)
	Quote        func(context.Context, migration.QuoteRequest) (migration.QuoteResult, error)
	Availability func(context.Context, migration.PriceRequest) (migration.AvailabilityResult, error)
	Start        func(context.Context, migration.Request) (migration.Result, error)
	Status       func(context.Context, string) (migration.StatusResult, error)
	Stop         func(context.Context, string) (migration.Result, error)
}

// RegisterMigrationTool exposes the source-side migration control plane. The
// handoff is delivered directly by the paired Telegram implementation after
// ordinary explicit approval in a private conversation.
func RegisterMigrationTool(registrar ToolRegistrar, operations MigrationToolOperations) error {
	if registrar == nil {
		return errors.New("migration tool registrar is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:        migrationToolName,
		Description: "Estimate the current public gross price, when available, or inspect current eligible locations for an exact Hetzner server type and optional strict location allow-list. Availability is read-only and repeats the requested location policy. Pricing is informational only; an unavailable estimate does not authorize or block a billable operation. Estimates may omit locations not currently available, which remain eligible if allowed. Migration execution is disabled pending cross-service acceptance. Before any future billable operation, obtain ordinary explicit user approval for the exact terms in a private conversation. Conversational approval is not a trusted cryptographic binding and is not recorded by a separate approval mechanism. No provider token, private key, or owner link is accepted or returned here.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"estimate", "quote", "availability", "start", "status", "stop"},
				},
				"server_type": map[string]any{
					"type":        "string",
					"description": "Exact Hetzner server type name, for example cx22; no substitution is allowed",
				},
				"locations": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional strict location allow-list; omit or leave empty to allow every eligible location",
				},
				"job_id": map[string]any{
					"type":        "string",
					"description": "Non-secret monitor ID for status or stop only; start generates its own monitor ID",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeMigrationTool(ctx, arguments, operations)
		},
	})
}

type migrationToolArguments struct {
	Action     string   `json:"action"`
	ServerType string   `json:"server_type,omitempty"`
	Locations  []string `json:"locations,omitempty"`
	JobID      string   `json:"job_id,omitempty"`
}

func executeMigrationTool(ctx context.Context, arguments string, operations MigrationToolOperations) (string, error) {
	parsed, err := parseMigrationToolArguments(arguments)
	if err != nil {
		return migrationToolRejected(err), nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request := migration.PriceRequest{ServerType: parsed.ServerType, Locations: parsed.Locations}
	switch strings.ToLower(strings.TrimSpace(parsed.Action)) {
	case "estimate", "quote":
		estimate := operations.Estimate
		if estimate == nil {
			estimate = operations.Quote
		}
		if estimate == nil {
			return migrationToolUnavailable(errors.New("migration price estimate capability is unavailable")), nil
		}
		result, err := estimate(ctx, request)
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "availability":
		if operations.Availability == nil {
			return migrationToolUnavailable(errors.New("migration availability capability is unavailable")), nil
		}
		result, err := operations.Availability(ctx, request)
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "start":
		if kind, _ := ctx.Value(conversationTypeKey{}).(string); kind != "private" {
			return migrationToolRejected(errors.New("migration start requires a private conversation")), nil
		}
		if operations.Start == nil {
			return migrationToolUnavailable(errors.New("migration start capability is unavailable")), nil
		}
		if parsed.JobID != "" {
			return migrationToolRejected(errors.New("start does not accept a model-selected monitor ID")), nil
		}
		result, err := operations.Start(ctx, migration.Request{
			ServerType: parsed.ServerType,
			Locations:  parsed.Locations,
		})
		if err != nil {
			if result.Status == "stop_unconfirmed" || result.Status == "in_progress" {
				diagnostics.Error("migration.tool", err, diagnostics.Field{Key: "reason", Value: "migration monitor state requires attention"})
				return encodeMigrationToolResult(result)
			}
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "status":
		if operations.Status == nil {
			return migrationToolUnavailable(errors.New("migration status capability is unavailable")), nil
		}
		result, err := operations.Status(ctx, parsed.JobID)
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "stop":
		if operations.Stop == nil {
			return migrationToolUnavailable(errors.New("migration stop capability is unavailable")), nil
		}
		result, err := operations.Stop(ctx, parsed.JobID)
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	default:
		return migrationToolRejected(errors.New("unsupported migration action")), nil
	}
}

func parseMigrationToolArguments(arguments string) (migrationToolArguments, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var parsed migrationToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return migrationToolArguments{}, errors.New("migration arguments are malformed")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return migrationToolArguments{}, errors.New("migration arguments contain trailing data")
	}
	return parsed, nil
}

func encodeMigrationToolResult(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", errors.Join(errors.New("encode migration tool result"), err)
	}
	return string(data), nil
}

func migrationToolRejected(err error) string {
	diagnostics.Error("migration.tool", err, diagnostics.Field{Key: "reason", Value: "invalid migration arguments"})
	return `{"status":"rejected","message":"Migration arguments were rejected; no action was taken."}`
}

func migrationToolUnavailable(err error) string {
	diagnostics.Error("migration.tool", err, diagnostics.Field{Key: "reason", Value: "migration capability or operation unavailable"})
	return `{"status":"unavailable","message":"Migration is unavailable or needs manual review; no action was repeated."}`
}
