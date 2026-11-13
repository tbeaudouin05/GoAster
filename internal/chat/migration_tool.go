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

// MigrationToolOperations keeps the model-facing control plane separate from
// the privileged executor. Callers provide plain functions; no model text is
// used as an executor command.
type MigrationToolOperations struct {
	Quote          func(context.Context, migration.QuoteRequest) (migration.QuoteResult, error)
	Start          func(context.Context, migration.Request) (migration.Result, error)
	Status         func(context.Context, string) (migration.StatusResult, error)
	Stop           func(context.Context, string) (migration.Result, error)
	RepairDelivery func(context.Context, string) (migration.Result, error)
}

// RegisterMigrationTool exposes the dedicated migration control plane. It
// does not add a Telegram command and it never accepts credentials or private
// handoff material as arguments.
func RegisterMigrationTool(registrar ToolRegistrar, operations MigrationToolOperations) error {
	if registrar == nil {
		return errors.New("migration tool registrar is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:        migrationToolName,
		Description: "Manage one approved GoAster migration. Quote reads current compatible Hetzner offers; start begins one approved 30-minute monitor; status, stop, and repair_delivery are local migration controls. The tool reads HCLOUD_TOKEN internally and never accepts or returns credentials, private keys, owner URLs, callback secrets, or chat IDs.",
		Parameters:  migrationToolParameters(),
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeMigrationTool(ctx, arguments, operations)
		},
	})
}

func migrationToolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"quote", "start", "status", "stop", "repair_delivery"},
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Stable non-secret identifier for this one migration approval",
			},
			"target": map[string]any{
				"type":                 "object",
				"properties":           migrationTargetParameters(),
				"required":             []string{"server_type_id", "server_type_name", "architecture", "image_id", "image_name", "network_ids", "enable_ipv4", "enable_ipv6"},
				"additionalProperties": false,
			},
			"approved_baseline_gross": map[string]any{
				"type":        "string",
				"description": "Exact TOTAL gross hourly price returned by quote and presented to the owner before approval",
			},
			"approved_currency": map[string]any{
				"type":        "string",
				"description": "Currency returned by quote and included in the owner's approval",
			},
			"approved": map[string]any{
				"type":        "boolean",
				"description": "True only after the skill has presented the baseline and the owner explicitly approved exactly one create",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

func migrationTargetParameters() map[string]any {
	return map[string]any{
		"server_type_id":   map[string]any{"type": "integer", "minimum": 1},
		"server_type_name": map[string]any{"type": "string"},
		"architecture":     map[string]any{"type": "string", "enum": []string{"x86", "arm"}},
		"image_id":         map[string]any{"type": "integer", "minimum": 1},
		"image_name":       map[string]any{"type": "string", "const": "ubuntu-24.04"},
		"location_allowlist": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Optional location names; empty means all compatible locations",
		},
		"network_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}},
		"enable_ipv4": map[string]any{"type": "boolean"},
		"enable_ipv6": map[string]any{"type": "boolean"},
		"primary_ip_type": map[string]any{
			"type":        "string",
			"description": "Optional approved primary IPv4 pricing type; defaults to ipv4",
		},
	}
}

type migrationToolArguments struct {
	Action                string           `json:"action"`
	JobID                 string           `json:"job_id,omitempty"`
	Target                migration.Target `json:"target,omitempty"`
	ApprovedBaselineGross string           `json:"approved_baseline_gross,omitempty"`
	ApprovedCurrency      string           `json:"approved_currency,omitempty"`
	Approved              bool             `json:"approved,omitempty"`
}

func executeMigrationTool(ctx context.Context, arguments string, operations MigrationToolOperations) (string, error) {
	parsed, err := parseMigrationToolArguments(arguments)
	if err != nil {
		return migrationToolRejected(err), nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Action)) {
	case "quote":
		if operations.Quote == nil {
			return migrationToolUnavailable(errors.New("migration quote capability unavailable")), nil
		}
		result, err := operations.Quote(ctx, migration.QuoteRequest{JobID: parsed.JobID, Target: parsed.Target})
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "start":
		if operations.Start == nil {
			return migrationToolUnavailable(errors.New("migration start capability unavailable")), nil
		}
		if !parsed.Approved {
			return `{"status":"approval_required","message":"Present the quote baseline and obtain explicit approval before starting; no action was taken."}`, nil
		}
		result, err := operations.Start(ctx, migration.Request{
			Action:                "monitor",
			JobID:                 parsed.JobID,
			Target:                parsed.Target,
			ApprovedBaselineGross: parsed.ApprovedBaselineGross,
			ApprovedCurrency:      parsed.ApprovedCurrency,
			Approved:              true,
		})
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "status":
		if operations.Status == nil {
			return migrationToolUnavailable(errors.New("migration status capability unavailable")), nil
		}
		result, err := operations.Status(ctx, parsed.JobID)
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "stop":
		if operations.Stop == nil {
			return migrationToolUnavailable(errors.New("migration stop capability unavailable")), nil
		}
		result, err := operations.Stop(ctx, parsed.JobID)
		if err != nil {
			return migrationToolUnavailable(err), nil
		}
		return encodeMigrationToolResult(result)
	case "repair_delivery":
		if operations.RepairDelivery == nil {
			return migrationToolUnavailable(errors.New("migration delivery repair capability unavailable")), nil
		}
		result, err := operations.RepairDelivery(ctx, parsed.JobID)
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
		return "", err
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
