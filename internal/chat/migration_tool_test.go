package chat

import (
	"context"
	"encoding/json"
	"errors"
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

func TestMigrationToolKeepsSecretsAndModelApprovalOutOfSchema(t *testing.T) {
	registrar := &migrationToolTestRegistrar{}
	var starts atomic.Int32
	var received migration.Request
	var availabilityRequest migration.PriceRequest
	if err := RegisterMigrationTool(registrar, MigrationToolOperations{
		Availability: func(_ context.Context, request migration.PriceRequest) (migration.AvailabilityResult, error) {
			availabilityRequest = request
			return migration.AvailabilityResult{
				Version:            "v1",
				ServerType:         request.ServerType,
				LocationPolicy:     migration.LocationPolicyAllowlist,
				RequestedLocations: append([]string(nil), request.Locations...),
				Locations:          []string{"fsn1"},
			}, nil
		},
		Start: func(_ context.Context, request migration.Request) (migration.Result, error) {
			starts.Add(1)
			received = request
			return migration.Result{Status: "created"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != migrationToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered migration tool = %#v", registrar.tool)
	}
	for _, required := range []string{
		"may omit locations not currently available",
		"read-only",
		"repeats the requested location policy",
		"Pricing is informational only",
		"ordinary explicit user approval",
		"private conversation",
		"not a trusted cryptographic binding",
		"not recorded by a separate approval mechanism",
	} {
		if !strings.Contains(registrar.tool.Description, required) {
			t.Fatalf("tool description missing %q: %s", required, registrar.tool.Description)
		}
	}
	for _, forbidden := range []string{
		"trusted paired-private",
		"approval code",
		"APPROVE GOASTER MIGRATION",
	} {
		if strings.Contains(registrar.tool.Description, forbidden) {
			t.Fatalf("rejected approval prototype remained in tool description: %q", forbidden)
		}
	}
	parameters, err := json.Marshal(registrar.tool.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	parameterText := string(parameters)
	for _, forbidden := range []string{"hetzner_token", "private_key", "owner_url", "owner_secret", "callback_secret", "chat_id", "approved_hourly_gross", "approved_currency", "approved"} {
		if strings.Contains(parameterText, forbidden) {
			t.Fatalf("migration schema exposes forbidden parameter %q: %s", forbidden, parameterText)
		}
	}

	result, err := registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","approved":true}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("model approval forgery = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","approved_hourly_gross":"0.10","approved_currency":"EUR","approved":true,"hetzner_token":"secret"}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("secret-bearing or model-approved start = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22","job_id":"migration-forged"}`)
	if err != nil || !strings.Contains(result, `"rejected"`) || starts.Load() != 0 {
		t.Fatalf("model-selected monitor ID = %q, %v; starts=%d", result, err, starts.Load())
	}
	result, err = registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"created"`) || starts.Load() != 1 {
		t.Fatalf("trusted start delegation = %q, %v; starts=%d", result, err, starts.Load())
	}
	if received.ServerType != "cx22" || received.ApprovedHourlyGross != "" || received.ApprovedCurrency != "" || received.JobID != "" {
		t.Fatalf("model-controlled approval fields reached start operation: %#v", received)
	}
	result, err = registrar.tool.Handler(context.Background(), `{"action":"availability","server_type":"cx22","locations":["fsn1"]}`)
	if err != nil || !strings.Contains(result, `"location_policy":"allowlist"`) || !strings.Contains(result, `"requested_locations":["fsn1"]`) {
		t.Fatalf("availability action = %q, %v", result, err)
	}
	if availabilityRequest.ServerType != "cx22" || len(availabilityRequest.Locations) != 1 || availabilityRequest.Locations[0] != "fsn1" {
		t.Fatalf("availability request = %#v", availabilityRequest)
	}
}

func TestMigrationToolReportsUnconfirmedStop(t *testing.T) {
	registrar := &migrationToolTestRegistrar{}
	if err := RegisterMigrationTool(registrar, MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		return migration.Result{Status: "stop_unconfirmed", MonitorID: "migration-test"}, errors.New("synthetic failure")
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := registrar.tool.Handler(WithConversationType(context.Background(), "private"), `{"action":"start","server_type":"cx22"}`)
	if err != nil || !strings.Contains(result, `"stop_unconfirmed"`) || !strings.Contains(result, `"migration-test"`) {
		t.Fatalf("result = %s, %v", result, err)
	}
	calls := 0
	ops := MigrationToolOperations{Start: func(context.Context, migration.Request) (migration.Result, error) {
		calls++
		return migration.Result{}, nil
	}}
	for _, kind := range []string{"", "group", "supergroup", "forged"} {
		result, err := executeMigrationTool(WithConversationType(context.Background(), kind), `{"action":"start","server_type":"cx22"}`, ops)
		if err != nil || !strings.Contains(result, `"rejected"`) || calls != 0 {
			t.Fatalf("kind %q: %s %v calls=%d", kind, result, err, calls)
		}
	}

}
