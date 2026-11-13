package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"goaster/internal/migration"
)

type migrationToolTestRegistrar struct {
	tool Tool
}

func (registrar *migrationToolTestRegistrar) RegisterTool(tool Tool) error {
	registrar.tool = tool
	return nil
}

func TestMigrationToolHasNoSecretOrChatParametersAndRequiresOneApproval(t *testing.T) {
	registrar := &migrationToolTestRegistrar{}
	var starts atomic.Int32
	if err := RegisterMigrationTool(registrar, MigrationToolOperations{
		Start: func(context.Context, migration.Request) (migration.Result, error) {
			starts.Add(1)
			return migration.Result{Status: "created"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != migrationToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered migration tool = %#v", registrar.tool)
	}
	parameters, err := json.Marshal(registrar.tool.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	parameterText := string(parameters)
	for _, forbidden := range []string{"provider_token", "private_key", "owner_url", "callback_secret", "chat_id"} {
		if strings.Contains(parameterText, forbidden) {
			t.Fatalf("migration schema exposes forbidden parameter %q: %s", forbidden, parameterText)
		}
	}

	result, err := registrar.tool.Handler(context.Background(), `{"action":"start","job_id":"one"}`)
	if err != nil || !strings.Contains(result, `"approval_required"`) || starts.Load() != 0 {
		t.Fatalf("unapproved start = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(context.Background(), `{"action":"start","provider_token":"secret"}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("secret-bearing start = %q, %v; starts=%d", result, err, starts.Load())
	}
}
