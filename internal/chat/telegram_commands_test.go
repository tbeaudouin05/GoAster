package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/artefacts"
	"goaster/internal/telegramcommands"
)

func TestTelegramCommandSkillUsesRuntimeReferenceAcrossStarterUpgrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOASTER_STARTER_DIR", filepath.Join(filepath.Dir(filepath.Dir(working)), "skills"))
	if err := artefacts.InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}

	root, err := artefacts.Root()
	if err != nil {
		t.Fatal(err)
	}
	legacyReferencePath := filepath.Join(root, "telegram-commands.md")
	if _, err := os.Lstat(legacyReferencePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh starter installation created a persistent command reference: %v", err)
	}

	provider := NewCodexProvider(CredentialStore{})
	definition, found := findToolDefinition(provider.tools.definitions(), telegramcommands.CommandReferenceToolName)
	if !found {
		t.Fatalf("agent did not expose %q", telegramcommands.CommandReferenceToolName)
	}
	if definition.Type != "function" || definition.Parameters["additionalProperties"] != false {
		t.Fatalf("runtime reference tool definition = %+v", definition)
	}
	handler, found := provider.tools.handler(telegramcommands.CommandReferenceToolName)
	if !found {
		t.Fatalf("agent did not register a handler for %q", telegramcommands.CommandReferenceToolName)
	}

	skill, err := artefacts.Load("telegram-commands")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{telegramcommands.CommandReferenceToolName, "current running GoAster binary", "aliases", "access requirements"} {
		if !strings.Contains(strings.ToLower(skill), strings.ToLower(want)) {
			t.Errorf("Telegram command skill omitted %q: %s", want, skill)
		}
	}
	if strings.Contains(skill, "read_file") || strings.Contains(skill, "telegram-commands.md") {
		t.Fatalf("Telegram command skill still depends on a persistent reference: %s", skill)
	}

	reference, err := handler(context.Background(), `{}`)
	if err != nil || reference != telegramcommands.Reference() || !strings.Contains(reference, "/get-env") {
		t.Fatalf("agent runtime reference = %q, error = %v", reference, err)
	}

	// A legacy file may exist on an older installation. It is deliberately not
	// migrated or consulted; each tool call renders the current binary catalog.
	if err := os.WriteFile(legacyReferencePath, []byte("stale legacy reference"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := artefacts.InstallStarters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(legacyReferencePath); err != nil || string(got) != "stale legacy reference" {
		t.Fatalf("legacy reference was migrated: %q, error = %v", got, err)
	}
	upgradedReference, err := handler(context.Background(), `{}`)
	if err != nil || upgradedReference != telegramcommands.Reference() || strings.Contains(upgradedReference, "stale legacy reference") {
		t.Fatalf("upgraded runtime reference = %q, error = %v", upgradedReference, err)
	}
}

func findToolDefinition(definitions []codexTool, name string) (codexTool, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return codexTool{}, false
}
