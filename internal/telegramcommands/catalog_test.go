package telegramcommands

import (
	"strings"
	"testing"
)

func TestDispatchResolvesPrimaryCommandsAndAliases(t *testing.T) {
	for _, test := range []struct {
		text string
		name string
	}{
		{text: "/help", name: "help"},
		{text: "/m terra", name: "m"},
		{text: "/get_env", name: "get_env"},
		{text: "/get-env", name: "get_env"},
		{text: "/get_migration", name: "get_migration"},
	} {
		command, ok := Dispatch(test.text)
		if !ok || command.Name != test.name {
			t.Errorf("Dispatch(%q) = %+v, %v; want %q", test.text, command, ok, test.name)
		}
	}
	for _, text := range []string{"", "hello", "/more", "/help@other_bot"} {
		if _, ok := Dispatch(text); ok {
			t.Errorf("Dispatch(%q) unexpectedly matched", text)
		}
	}
}

func TestHelpAndMenuRenderFromTheSameCatalog(t *testing.T) {
	help := HelpText()
	menu := Menu()
	commands := Catalog()
	if len(menu) != len(commands) {
		t.Fatalf("menu count = %d, catalog count = %d", len(menu), len(commands))
	}
	for index, command := range commands {
		if menu[index].Command != command.Name || menu[index].Description != command.MenuDescription {
			t.Fatalf("menu[%d] = %+v, catalog[%d] = %+v", index, menu[index], index, command)
		}
		if !strings.Contains(help, command.Usage+" — "+command.HelpDescription) {
			t.Fatalf("help omitted %s", command.Usage)
		}
		if command.Access == "" {
			t.Fatalf("command %s omitted access requirements", command.Name)
		}
		if command.Name != "get_env" && len(command.Aliases) != 0 {
			t.Fatalf("unexpected aliases for %s: %v", command.Name, command.Aliases)
		}
	}
	if !strings.Contains(help, "/get_env, /get-env") || !strings.Contains(help, "private chat") {
		t.Fatalf("help omitted alias or access guidance: %q", help)
	}
}

func TestReferenceContainsAliasesAndAccessRequirements(t *testing.T) {
	reference := Reference()
	for _, want := range []string{
		"/get_env, /get-env",
		"Aliases: `/get-env`",
		"Access requirements:",
		"Pairing requires a private chat",
		"After pairing, the paired account can use /pair in a private chat, group, or supergroup for readiness guidance",
		"/start CODE is ignored",
		"/start CODE never pairs or forwards the code",
		"Native Telegram menu: yes",
		"running GoAster binary for this request",
	} {
		if !strings.Contains(reference, want) {
			t.Errorf("reference omitted %q", want)
		}
	}
	if strings.Contains(reference, "telegram-commands.md") {
		t.Fatal("runtime reference names a persistent reference file")
	}
	if strings.Contains(reference, "/start CODE is rejected") {
		t.Fatal("runtime reference incorrectly says paired /start CODE is rejected")
	}
	if strings.Contains(reference, "github.com") || strings.Contains(reference, "http://") || strings.Contains(reference, "https://") {
		t.Fatal("reference depends on a public URL")
	}
}
