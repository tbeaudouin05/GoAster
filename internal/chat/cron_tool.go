package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"goaster/internal/scheduler"
)

const cronToolName = "cron"

// RegisterCronTool exposes local chat-turn scheduling through the existing
// provider tool registrar. The scheduler owns persistence and execution; the
// tool only translates model arguments into scheduler operations.
func RegisterCronTool(registrar ToolRegistrar, jobs *scheduler.Scheduler) error {
	if registrar == nil {
		return fmt.Errorf("cron tool registrar is nil")
	}
	if jobs == nil {
		return fmt.Errorf("cron scheduler is nil")
	}
	return registrar.RegisterTool(Tool{
		Name:        cronToolName,
		Description: "Schedule chat messages for a later time or recurring interval. Manage only local GoAster chat turns; it does not schedule shell commands or route to channels.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"add", "list", "get", "update", "remove", "enable", "disable"},
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Job ID returned by add or list",
				},
				"at_seconds": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Positive number of seconds from now for a one-time chat turn",
				},
				"every_seconds": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Positive interval in seconds for a recurring chat turn",
				},
				"cron_expr": map[string]any{
					"type":        "string",
					"description": "Five-field cron expression, or six fields including seconds",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Message GoAster should send through the existing chat session",
				},
				"max_runs": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Maximum executions; zero means unlimited",
				},
				"delete_after_run": map[string]any{
					"type":        "boolean",
					"description": "Delete the job after its next execution; at jobs always have this behavior",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, arguments string) (string, error) {
			return executeCronTool(ctx, arguments, jobs)
		},
	})
}

type cronToolArguments struct {
	Action         string  `json:"action"`
	ID             string  `json:"id,omitempty"`
	AtSeconds      *int64  `json:"at_seconds,omitempty"`
	EverySeconds   *int64  `json:"every_seconds,omitempty"`
	CronExpr       *string `json:"cron_expr,omitempty"`
	Message        *string `json:"message,omitempty"`
	MaxRuns        *int    `json:"max_runs,omitempty"`
	DeleteAfterRun *bool   `json:"delete_after_run,omitempty"`
}

func executeCronTool(ctx context.Context, arguments string, jobs *scheduler.Scheduler) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parsed, err := parseCronToolArguments(arguments)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Action)) {
	case "add":
		return cronToolAdd(parsed, jobs)
	case "list":
		return encodeCronToolResult(struct {
			Jobs []scheduler.Job `json:"jobs"`
		}{Jobs: jobs.List()})
	case "get":
		job, err := requireCronJob(parsed.ID, jobs)
		if err != nil {
			return "", err
		}
		return encodeCronToolResult(job)
	case "update":
		if strings.TrimSpace(parsed.ID) == "" {
			return "", fmt.Errorf("cron update requires id")
		}
		if parsed.AtSeconds == nil && parsed.EverySeconds == nil && parsed.CronExpr == nil && parsed.Message == nil && parsed.MaxRuns == nil && parsed.DeleteAfterRun == nil {
			return "", fmt.Errorf("cron update requires at least one field")
		}
		patch := scheduler.Patch{
			AtSeconds:      parsed.AtSeconds,
			EverySeconds:   parsed.EverySeconds,
			CronExpr:       parsed.CronExpr,
			Message:        parsed.Message,
			MaxRuns:        parsed.MaxRuns,
			DeleteAfterRun: parsed.DeleteAfterRun,
		}
		job, err := jobs.Update(parsed.ID, patch)
		if err != nil {
			return "", err
		}
		return encodeCronToolResult(job)
	case "remove":
		if strings.TrimSpace(parsed.ID) == "" {
			return "", fmt.Errorf("cron remove requires id")
		}
		if err := jobs.Remove(parsed.ID); err != nil {
			return "", err
		}
		return encodeCronToolResult(struct {
			Removed bool   `json:"removed"`
			ID      string `json:"id"`
		}{Removed: true, ID: strings.TrimSpace(parsed.ID)})
	case "enable", "disable":
		if strings.TrimSpace(parsed.ID) == "" {
			return "", fmt.Errorf("cron %s requires id", strings.ToLower(strings.TrimSpace(parsed.Action)))
		}
		job, err := jobs.SetEnabled(parsed.ID, strings.EqualFold(strings.TrimSpace(parsed.Action), "enable"))
		if err != nil {
			return "", err
		}
		return encodeCronToolResult(job)
	default:
		return "", fmt.Errorf("unsupported cron action %q", parsed.Action)
	}
}

func parseCronToolArguments(arguments string) (cronToolArguments, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var parsed cronToolArguments
	if err := decoder.Decode(&parsed); err != nil {
		return cronToolArguments{}, fmt.Errorf("cron arguments are not valid JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return cronToolArguments{}, fmt.Errorf("cron arguments contain more than one JSON value")
	}
	return parsed, nil
}

func cronToolAdd(arguments cronToolArguments, jobs *scheduler.Scheduler) (string, error) {
	scheduleFields := 0
	if arguments.AtSeconds != nil {
		scheduleFields++
	}
	if arguments.EverySeconds != nil {
		scheduleFields++
	}
	if arguments.CronExpr != nil {
		scheduleFields++
	}
	if scheduleFields != 1 {
		return "", fmt.Errorf("cron add requires exactly one of at_seconds, every_seconds, or cron_expr")
	}
	if arguments.Message == nil || strings.TrimSpace(*arguments.Message) == "" {
		return "", fmt.Errorf("cron add requires message")
	}
	if arguments.MaxRuns != nil && *arguments.MaxRuns < 0 {
		return "", fmt.Errorf("max_runs must not be negative")
	}
	job := scheduler.Job{Message: *arguments.Message}
	switch {
	case arguments.AtSeconds != nil:
		job.Kind = scheduler.ScheduleAt
		job.AtSeconds = *arguments.AtSeconds
		job.DeleteAfterRun = true
	case arguments.EverySeconds != nil:
		job.Kind = scheduler.ScheduleEvery
		job.EverySeconds = *arguments.EverySeconds
	case arguments.CronExpr != nil:
		job.Kind = scheduler.ScheduleCron
		job.CronExpr = strings.TrimSpace(*arguments.CronExpr)
	}
	if arguments.MaxRuns != nil {
		job.MaxRuns = *arguments.MaxRuns
	}
	if arguments.DeleteAfterRun != nil {
		job.DeleteAfterRun = *arguments.DeleteAfterRun
	}
	created, err := jobs.Add(job)
	if err != nil {
		return "", err
	}
	return encodeCronToolResult(created)
}

func requireCronJob(id string, jobs *scheduler.Scheduler) (scheduler.Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return scheduler.Job{}, fmt.Errorf("cron get requires id")
	}
	job, ok := jobs.Get(id)
	if !ok {
		return scheduler.Job{}, fmt.Errorf("%w: %s", scheduler.ErrJobNotFound, id)
	}
	return job, nil
}

func encodeCronToolResult(value any) (string, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("encode cron result: %w", err)
	}
	return strings.TrimSpace(output.String()), nil
}
