// Package telegramcommands contains the canonical GoAster Telegram command
// catalog shared by the adapter, local help, and the runtime agent reference.
package telegramcommands

import (
	"fmt"
	"strings"
)

const (
	HelpCommand               = "/help"
	StopCommand               = "/stop"
	StartCommand              = "/start"
	PairCommand               = "/pair"
	ClearCommand              = "/clear"
	ModelCommand              = "/m"
	EnvironmentCommand        = "/env"
	GetEnvironmentCommand     = "/get_env"
	GetEnvironmentManualAlias = "/get-env"
	CommandReferenceToolName  = "telegram_command_reference"
)

// Command describes one supported Telegram slash command. Name is the
// command used by Telegram's native menu, while Aliases contains manually
// accepted command spellings that are not menu entries.
type Command struct {
	Name            string
	Aliases         []string
	Usage           string
	HelpDescription string
	MenuDescription string
	Access          string
}

// MenuCommand is the setMyCommands representation accepted by Telegram.
type MenuCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

var catalog = []Command{
	{
		Name:            "help",
		Usage:           "/help",
		HelpDescription: "Show this list.",
		MenuDescription: "Show available commands",
		Access:          "Before pairing, private chats. After pairing, the paired account in private chats, groups, and supergroups. Use bare /help; addressed command mentions are ignored.",
	},
	{
		Name:            "stop",
		Usage:           "/stop",
		HelpDescription: "Cancel the current agent turn in this chat/thread (paired account).",
		MenuDescription: "Cancel the current agent turn",
		Access:          "Paired account only, in the same chat and Telegram topic as the active turn. Use the bare command in a private chat, group, or supergroup; addressed command mentions are ignored.",
	},
	{
		Name:            "start",
		Usage:           "/start",
		HelpDescription: "Show pairing or readiness guidance; /start CODE never pairs.",
		MenuDescription: "Show pairing or readiness guidance",
		Access:          "Before pairing, bare /start gives guidance in private chats; /start CODE is ignored. After pairing, the paired account can use /start or /start CODE in a private chat, group, or supergroup for readiness guidance. /start CODE never pairs or forwards the code.",
	},
	{
		Name:            "pair",
		Usage:           "/pair CODE",
		HelpDescription: "Pair the first account with the one-time code (pairing requires a private chat).",
		MenuDescription: "Pair this account with GoAster",
		Access:          "Pairing requires a private chat. Before pairing, /pair CODE consumes the one-time code for the first valid account; bare /pair gives instructions. After pairing, the paired account can use /pair in a private chat, group, or supergroup for readiness guidance; it does not expose or forward a code.",
	},
	{
		Name:            "clear",
		Usage:           "/clear",
		HelpDescription: "Clear this chat's history (paired account).",
		MenuDescription: "Clear this chat's history",
		Access:          "Paired account only, in a private chat, group, or supergroup. It takes no attachment or argument and clears the chat's shared history across Telegram topics.",
	},
	{
		Name:            "m",
		Usage:           "/m [MODEL_OR_ALIAS]",
		HelpDescription: "Show or select this chat's model (paired account).",
		MenuDescription: "Show or select this chat's model",
		Access:          "Paired account only, in a private chat, group, or supergroup. Use /m to view the current model or /m MODEL_OR_ALIAS to select one; attachments are not accepted.",
	},
	{
		Name:            "env",
		Usage:           "/env (attach goaster.env)",
		HelpDescription: "Preview settings changes; reply ACCEPT to apply (paired account, private chat only).",
		MenuDescription: "Preview an environment update",
		Access:          "Paired account only, private chat only. Attach a document named goaster.env to /env in the same message, review the preview, then reply exactly ACCEPT. /env accepts a valid @bot_username suffix.",
	},
	{
		Name:            "get_env",
		Aliases:         []string{"get-env"},
		Usage:           "/get_env, /get-env",
		HelpDescription: "Send the saved goaster.env file (paired account, private chat only).",
		MenuDescription: "Download the saved environment file",
		Access:          "Paired account only, private chat only. The native menu uses /get_env; the manually typed alias /get-env is also accepted. Either spelling accepts a valid @bot_username suffix.",
	},
}

const helpFooter = "Before pairing, /help and bare /start provide guidance in private chats; pairing requires a private chat. After pairing, only the paired account can use the bot, and bare /start or /pair provide readiness guidance in private chats, groups, and supergroups. " +
	"Telegram topics in the same chat share conversation history and model settings. /env, ACCEPT, and environment file retrieval require a private chat. /env, /get_env, and /get-env accept @bot_username suffixes; use bare /help and /stop."

// Catalog returns the supported commands in their stable display order.
func Catalog() []Command {
	commands := make([]Command, len(catalog))
	for index, command := range catalog {
		commands[index] = cloneCommand(command)
	}
	return commands
}

// Menu returns the canonical command list for Telegram's native menu.
func Menu() []MenuCommand {
	commands := make([]MenuCommand, 0, len(catalog))
	for _, command := range catalog {
		commands = append(commands, MenuCommand{Command: command.Name, Description: command.MenuDescription})
	}
	return commands
}

// Dispatch identifies the command at the start of text. It returns the
// canonical command for a primary name or a manually accepted alias and does
// not treat Telegram @bot_username mentions as aliases.
func Dispatch(text string) (Command, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return Command{}, false
	}
	token := fields[0]
	for _, command := range catalog {
		if token == "/"+command.Name {
			return cloneCommand(command), true
		}
		for _, alias := range command.Aliases {
			if token == "/"+alias {
				return cloneCommand(command), true
			}
		}
	}
	return Command{}, false
}

// HelpText renders the user-facing help text from the catalog.
func HelpText() string {
	var help strings.Builder
	help.WriteString("Available Telegram commands:\n")
	for _, command := range catalog {
		fmt.Fprintf(&help, "%s — %s\n", command.Usage, command.HelpDescription)
	}
	help.WriteString(helpFooter)
	return help.String()
}

// Reference renders the command reference from the catalog in the running
// GoAster binary. Callers should render it when requested instead of caching
// it in the user's artefact workspace.
func Reference() string {
	var reference strings.Builder
	reference.WriteString("# GoAster Telegram command reference\n\n")
	reference.WriteString("This reference is generated from the Telegram command catalog in the running GoAster binary for this request. It is authoritative for that binary; do not consult a public repository or assume another installation has the same commands.\n\n")
	for _, command := range catalog {
		fmt.Fprintf(&reference, "## %s\n\n", command.Usage)
		fmt.Fprintf(&reference, "- Primary command: `/%s`\n", command.Name)
		fmt.Fprintf(&reference, "- Aliases: %s\n", formatAliases(command.Aliases))
		fmt.Fprintf(&reference, "- Description: %s\n", command.HelpDescription)
		fmt.Fprintf(&reference, "- Access requirements: %s\n", command.Access)
		if command.MenuDescription != "" {
			reference.WriteString("- Native Telegram menu: yes\n")
		} else {
			reference.WriteString("- Native Telegram menu: no\n")
		}
		reference.WriteString("\n")
	}
	reference.WriteString("## Shared behavior\n\n")
	reference.WriteString(helpFooter)
	reference.WriteByte('\n')
	return reference.String()
}

func cloneCommand(command Command) Command {
	command.Aliases = append([]string(nil), command.Aliases...)
	return command
}

func formatAliases(aliases []string) string {
	if len(aliases) == 0 {
		return "none"
	}
	formatted := make([]string, len(aliases))
	for index, alias := range aliases {
		formatted[index] = "`/" + alias + "`"
	}
	return strings.Join(formatted, ", ")
}
