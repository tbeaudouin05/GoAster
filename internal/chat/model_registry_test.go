package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appconfig "goaster/config"
)

func TestShippedModelConfigIsInstalledAndUsed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	registry, err := NewModelRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".goaster", "artefacts", "config", "openai-models.json"); registry.Path != want {
		t.Fatalf("runtime path = %q, want %q", registry.Path, want)
	}
	state, err := registry.Current(42)
	if err != nil || state.Selection.ID != DefaultModel {
		t.Fatalf("installed selection = %+v, %v", state, err)
	}
	embedded, err := ParseModelConfig(appconfig.OpenAIModels)
	if err != nil {
		t.Fatal(err)
	}
	want := []ConfiguredModel{
		{ID: "gpt-6.1-terra", Alias: "terra"},
		{ID: "gpt-6.1-sol", Alias: "sol"},
		{ID: "gpt-5.6-luna", Alias: "luna"},
	}
	if !reflect.DeepEqual(embedded.Models, want) {
		t.Fatalf("embedded models = %+v, want %+v", embedded.Models, want)
	}
	data, err := os.ReadFile(registry.Path)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := ParseModelConfig(data)
	if err != nil || !reflect.DeepEqual(installed, embedded) {
		t.Fatalf("installed configuration = %+v, %v; want %+v", installed, err, embedded)
	}
}

func TestModelCommandRecognizesOnlyM(t *testing.T) {
	for _, item := range []struct {
		message string
		arg     string
		valid   bool
	}{
		{message: "/m", valid: true},
		{message: " /m terra ", arg: "terra", valid: true},
		{message: "/m gpt-5.6-luna", arg: "gpt-5.6-luna", valid: true},
		{message: "/model terra"},
		{message: "/more"},
	} {
		arg, ok := ModelCommandArgument(item.message)
		if ok != item.valid || arg != item.arg || IsModelCommand(item.message) != item.valid {
			t.Errorf("model command %q = (%q, %v)", item.message, arg, ok)
		}
	}
}

func TestInvalidEmbeddedConfigQueuesRecoveryNoticeOnce(t *testing.T) {
	original := appconfig.OpenAIModels
	appconfig.OpenAIModels = []byte("broken")
	defer func() { appconfig.OpenAIModels = original }()
	registry := NewModelRegistryAt(filepath.Join(t.TempDir(), "openai-models.json"))
	state, err := registry.Current(42)
	if err != nil || state.Selection.ID != EmergencyModel || !strings.Contains(state.RecoveryNotice, "embedded model configuration") {
		t.Fatalf("invalid embedded config state = %+v, %v", state, err)
	}
	if notice := registry.takeRecoveryNotice(); !strings.Contains(notice, "embedded model configuration") {
		t.Fatalf("pending notice = %q", notice)
	}
	if _, err := registry.Current(42); err != nil || registry.takeRecoveryNotice() != "" {
		t.Fatalf("recovery notice repeated: %v", err)
	}
}

func TestParseModelConfigAcceptsOpenAIModelsAndRejectsInvalidRegistry(t *testing.T) {
	config, err := ParseModelConfig([]byte(`{
        "provider":"openai",
        "models":[
            {"id":"gpt-6.1-terra","alias":"terra"},
            {"id":"gpt-6.1-sol","alias":"sol"},
            {"id":"gpt-5.6-luna","alias":"luna"}
        ],
        "chats":{"42":"gpt-6.1-terra"}
    }`))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Models) != 3 || config.Models[0].ID != DefaultModel || config.Chats["42"] != DefaultModel {
		t.Fatalf("parsed model config = %+v", config)
	}

	for name, data := range map[string]string{
		"provider":           `{"provider":"other","models":[{"id":"model"}]}`,
		"duplicate ID":       `{"provider":"openai","models":[{"id":"model"},{"id":"model"}]}`,
		"unknown chat model": `{"provider":"openai","models":[{"id":"model"}],"chats":{"1":"missing"}}`,
		"unknown field":      `{"provider":"openai","models":[{"id":"model"}],"extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseModelConfig([]byte(data)); err == nil {
				t.Fatal("invalid model configuration was accepted")
			}
		})
	}
}

func TestModelRegistryResolvesAliasesAndPersistsExactChatModel(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	registry := NewModelRegistryAt(pathName)

	state, err := registry.Current(42)
	if err != nil {
		t.Fatal(err)
	}
	if state.Selection.ID != DefaultModel || state.Selection.Alias != "terra" {
		t.Fatalf("default selection = %+v", state.Selection)
	}
	selected, err := registry.Select(42, "terra")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != DefaultModel || selected.Alias != "terra" {
		t.Fatalf("alias selection = %+v", selected)
	}
	state, err = registry.Current(42)
	if err != nil || state.Selection.ID != DefaultModel {
		t.Fatalf("selected chat state = %+v, %v", state, err)
	}
	other, err := registry.Current(99)
	if err != nil || other.Selection.ID != DefaultModel {
		t.Fatalf("other chat state = %+v, %v", other, err)
	}

	data, err := os.ReadFile(pathName)
	if err != nil {
		t.Fatal(err)
	}
	var disk ModelConfig
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Chats["42"] != DefaultModel {
		t.Fatalf("persisted chat model = %q, want exact ID %q", disk.Chats["42"], DefaultModel)
	}
	if _, err := registry.Select(42, "gpt-5.6-luna"); err != nil {
		t.Fatal(err)
	}
	state, err = registry.Current(42)
	if err != nil || state.Selection.ID != "gpt-5.6-luna" || state.Selection.Alias != "luna" {
		t.Fatalf("exact ID selection = %+v, %v", state, err)
	}
}

func TestModelRegistryUsesTerraDefaultAndPreservesExplicitLegacySelections(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	legacy := ModelConfig{
		Provider: "openai",
		Models: []ConfiguredModel{
			{ID: "gpt-6.1-sol", Alias: "sol"},
			{ID: "gpt-6.1-terra", Alias: "terra"},
		},
		Chats: map[string]string{"42": "gpt-6.1-sol"},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		t.Fatal(err)
	}
	registry := NewModelRegistryAt(pathName)

	existing, err := registry.Current(42)
	if err != nil || existing.Selection.ID != "gpt-6.1-sol" {
		t.Fatalf("existing explicit selection = %+v, %v", existing, err)
	}
	newChat, err := registry.Current(99)
	if err != nil || newChat.Selection.ID != DefaultModel {
		t.Fatalf("new chat selection = %+v, %v; want %s", newChat, err, DefaultModel)
	}

	selected, err := registry.Select(99, "sol")
	if err != nil || selected.ID != "gpt-6.1-sol" {
		t.Fatalf("select sol = %+v, %v", selected, err)
	}
	newChat, err = registry.Current(99)
	if err != nil || newChat.Selection.ID != "gpt-6.1-sol" {
		t.Fatalf("switched chat selection = %+v, %v", newChat, err)
	}
	if stillExisting, err := registry.Current(42); err != nil || stillExisting.Selection.ID != "gpt-6.1-sol" {
		t.Fatalf("other explicit selection changed = %+v, %v", stillExisting, err)
	}

	selected, err = registry.Select(99, "terra")
	if err != nil || selected.ID != DefaultModel {
		t.Fatalf("switch back to terra = %+v, %v", selected, err)
	}
}

func TestSelectionForChatWithoutTerra(t *testing.T) {
	config := ModelConfig{
		Models: []ConfiguredModel{
			{ID: "gpt-6.1-sol", Alias: "sol"},
			{ID: "gpt-5.6-luna", Alias: "luna"},
		},
		Chats: map[string]string{"42": "gpt-5.6-luna"},
	}
	if got := selectionForChat(config, 99); got.ID != "gpt-6.1-sol" || got.Alias != "sol" {
		t.Fatalf("default selection = %+v", got)
	}
	if got := selectionForChat(config, 42); got.ID != "gpt-5.6-luna" || got.Alias != "luna" {
		t.Fatalf("saved selection = %+v", got)
	}
}

func TestModelRegistryReloadsChangedRuntimeConfigWithoutRestart(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	registry := NewModelRegistryAt(pathName)
	var models []string
	provider := providerFunc(func(_ context.Context, model, _ string) (string, error) {
		models = append(models, model)
		return "ok", nil
	})
	session := NewSession(Config{Registry: registry, ChatID: 7}, provider)
	if _, err := session.Reply(context.Background(), "before update"); err != nil {
		t.Fatal(err)
	}
	updated := ModelConfig{
		Provider: "openai",
		Models: []ConfiguredModel{
			{ID: "gpt-test-orbit", Alias: "orbit"},
			{ID: DefaultModel, Alias: "terra"},
		},
		Chats: map[string]string{"7": "gpt-test-orbit"},
	}
	data, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(filepath.Dir(pathName), "updated.json")
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, pathName); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Reply(context.Background(), "after update"); err != nil {
		t.Fatal(err)
	}
	if want := []string{DefaultModel, "gpt-test-orbit"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("models used across update = %v, want %v", models, want)
	}
}

func TestModelRegistryQuarantinesBrokenConfigAndReportsRecoveryOnce(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	if err := os.WriteFile(pathName, []byte(`{"provider":"openai","models":[`), 0600); err != nil {
		t.Fatal(err)
	}
	registry := NewModelRegistryAt(pathName)

	state, err := registry.Current(42)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Recovered || state.Selection.ID != EmergencyModel || state.RecoveryNotice == "" {
		t.Fatalf("recovery state = %+v", state)
	}
	if len([]rune(state.RecoveryNotice)) > maxRecoveryNoticeBytes {
		t.Fatalf("recovery notice is not bounded: %d", len([]rune(state.RecoveryNotice)))
	}
	if _, err := os.Stat(pathName); !os.IsNotExist(err) {
		t.Fatalf("broken config was not quarantined: %v", err)
	}
	quarantined, err := filepath.Glob(pathName + ".corrupt-*")
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("quarantine files = %v, %v", quarantined, err)
	}
	content, err := os.ReadFile(quarantined[0])
	if err != nil || string(content) != `{"provider":"openai","models":[` {
		t.Fatalf("quarantined content = %q, %v", content, err)
	}

	state, err = registry.Current(42)
	if err != nil || state.Selection.ID != EmergencyModel || state.RecoveryNotice != "" {
		t.Fatalf("repeated recovery = %+v, %v", state, err)
	}
	valid := ModelConfig{
		Provider: "openai",
		Models:   []ConfiguredModel{{ID: "gpt-restored", Alias: "restored"}},
		Chats:    map[string]string{},
	}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathName, data, 0600); err != nil {
		t.Fatal(err)
	}
	state, err = registry.Current(42)
	if err != nil || state.Selection.ID != "gpt-restored" || state.RecoveryNotice != "" {
		t.Fatalf("restored state = %+v, %v", state, err)
	}
}

func TestSessionSuppliesModelRecoveryNoticeOnceAndUsesTerra(t *testing.T) {
	pathName := filepath.Join(t.TempDir(), "openai-models.json")
	if err := os.WriteFile(pathName, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := NewModelRegistryAt(pathName)
	provider := &historyTestProvider{}
	session := NewSession(Config{Registry: registry, ChatID: 42}, provider)
	if session.PendingModelRecoveryNotice() == "" {
		t.Fatal("session did not retain pending recovery notice")
	}
	if session.modelStatus() != EmergencyModel {
		t.Fatalf("recovery model = %q, want %q", session.modelStatus(), EmergencyModel)
	}
	if _, err := session.Reply(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 1 || len(provider.histories[0]) == 0 || provider.histories[0][0].Role != "system" || !strings.Contains(provider.histories[0][0].Content, "parse model configuration") {
		t.Fatalf("recovery history = %#v", provider.histories)
	}
	if session.PendingModelRecoveryNotice() != "" {
		t.Fatal("recovery notice remained pending after the first request")
	}
	if _, err := session.Reply(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	for _, message := range provider.histories[1] {
		if strings.Contains(message.Content, "model configuration recovery notice") {
			t.Fatalf("recovery notice was injected a second time: %#v", provider.histories[1])
		}
	}
}

func TestModelRegistryRejectsSymlinkedConfigParentBeforeReadOrRecovery(t *testing.T) {
	for _, contents := range []string{string(appconfig.OpenAIModels), "invalid configuration"} {
		t.Run(map[bool]string{true: "valid target", false: "invalid target"}[contents != "invalid configuration"], func(t *testing.T) {
			home, outside := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			runtimeRoot := filepath.Join(home, ".goaster", "artefacts")
			if err := os.MkdirAll(runtimeRoot, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(outside, modelConfigFilename)
			if err := os.WriteFile(target, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(runtimeRoot, "config")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			registry, err := NewModelRegistry("")
			if err != nil {
				t.Fatal(err)
			}
			state, err := registry.Current(42)
			if err != nil || !state.Recovered || state.Selection.ID != EmergencyModel {
				t.Fatalf("state = %+v, error=%v", state, err)
			}
			if _, err := registry.Select(42, "terra"); err == nil {
				t.Fatal("selection wrote through symlinked parent")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != contents {
				t.Fatalf("outside file changed: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("outside directory modified: %v, %v", entries, err)
			}
		})
	}
}

func TestModelRegistryDoesNotChangeExistingExplicitParentPermissions(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewModelRegistryAt(filepath.Join(directory, modelConfigFilename))
	if _, err := registry.Current(42); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(directory)
	if err != nil || after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("existing parent permissions changed: %v, %v", after, err)
	}
}
