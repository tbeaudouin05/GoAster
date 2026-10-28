package ovh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goaster/internal/telegram"
)

func TestInstallerScriptHasValidShellSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}
	script := fmt.Sprintf(installScript, testRef, Repository)
	cmd := exec.Command(sh, "-n") // parse only; do not execute installation
	cmd.Stdin = strings.NewReader(script)
	if err := cmd.Run(); err != nil {
		t.Fatal("installer shell syntax is invalid")
	}
	if !strings.Contains(script, "git checkout --quiet --detach \"$ref\"") || !strings.Contains(script, "test \"$commit\" = \"$ref\"") || !strings.Contains(script, "systemctl is-active goaster-agent.service") || !strings.Contains(script, "/healthz") {
		t.Fatal("installer is missing commit or service verification")
	}
}

func TestTelegramSSHUsesOnlyStdinForSecrets(t *testing.T) {
	const token = "123456789:abcdefghijklmnopqrstuvwxyzABCDE_12345"
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var calls int
	ssh := OpenSSH{Run: func(_ context.Context, args []string, input io.Reader) (string, error) {
		calls++
		if strings.Contains(strings.Join(args, " "), token) || strings.Contains(strings.Join(args, " "), hash) {
			t.Fatal("secret was placed in SSH arguments")
		}
		data, err := io.ReadAll(input)
		if err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			if !strings.HasSuffix(strings.Join(args, " "), "sudo -n /usr/local/bin/goaster-telegram setup") {
				t.Fatal("setup command is not fixed")
			}
			var payload telegram.SetupInput
			if err := json.Unmarshal(data, &payload); err != nil || payload.Token != token || payload.PairingHash != hash {
				t.Fatal("SSH stdin did not contain exactly the Telegram setup input")
			}
		case 2:
			if strings.Contains(string(data), token) || strings.Contains(string(data), hash) || !strings.Contains(string(data), "systemctl is-active goaster-telegram.service") {
				t.Fatal("service verification included a secret or was incomplete")
			}
			return "active\n", nil
		default:
			t.Fatal("unexpected SSH command")
		}
		return "", nil
	}}
	target := Target{IP: "203.0.113.7", User: "goaster", PrivateKey: "/local/private-key"}
	if err := ssh.SetupTelegram(context.Background(), target, token, hash); err != nil || calls != 2 {
		t.Fatalf("setup failed: %v", err)
	}
	if err := ssh.SetupTelegram(context.Background(), target, token+"'$(touch BAD)'", hash); err == nil || calls != 2 {
		t.Fatal("shell metacharacters in a token were accepted or sent")
	}
	ssh.Run = func(context.Context, []string, io.Reader) (string, error) {
		return "", fmt.Errorf("sensitive error %s https://example.invalid/bot%s", token, token)
	}
	if err := ssh.SetupTelegram(context.Background(), target, token, hash); err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "https://") {
		t.Fatal("SSH failure exposed a token or URL")
	}
}

func TestTelegramServiceScriptsHaveValidShellSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}
	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(telegramStartScript)
	if err := cmd.Run(); err != nil {
		t.Fatal("Telegram service startup shell syntax is invalid")
	}
	if !strings.Contains(telegramStartScript, "systemctl disable --now goaster-telegram.service") || strings.Index(telegramStartScript, "systemctl enable goaster-telegram.service") < strings.Index(telegramStartScript, "systemctl is-active goaster-telegram.service") {
		t.Fatal("startup must clean up failures and enable only after verification")
	}
	installer := fmt.Sprintf(installScript, testRef, Repository)
	for _, want := range []string{"./cmd/goaster-telegram", "Type=notify", "NotifyAccess=main", "goaster-telegram.service", "/etc/goaster /var/lib/goaster-telegram"} {
		if !strings.Contains(installer, want) {
			t.Errorf("Telegram installer missing %q", want)
		}
	}
	if strings.Contains(installer, "TELEGRAM_BOT_TOKEN") || strings.Contains(installer, "api.telegram.org") {
		t.Fatal("Telegram unit or installer contains secret configuration or API URLs")
	}
}

func TestTelegramServiceStartupAndFailureCleanup(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"sudo": "#!/bin/sh\nshift\nexec \"$@\"\n",
		"systemctl": `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_LOG"
case "$1" in
  restart) test "$FAKE_FAILURE" != restart ;;
  is-active) test "$FAKE_FAILURE" != status && printf 'active\n' ;;
  enable) test "$FAKE_FAILURE" != enable ;;
  *) exit 0 ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, failure := range []string{"", "restart", "status", "enable"} {
		t.Run("failure_"+failure, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "calls")
			cmd := exec.Command(sh)
			cmd.Env = append(os.Environ(), "PATH="+dir, "FAKE_LOG="+log, "FAKE_FAILURE="+failure)
			cmd.Stdin = strings.NewReader(telegramStartScript)
			output, err := cmd.Output()
			if (err == nil) != (failure == "") {
				t.Fatalf("startup result error = %v", err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "" {
				if string(output) != "active\n" || strings.Contains(string(calls), "disable --now") {
					t.Fatal("successful start was not preserved")
				}
			} else if !strings.Contains(string(calls), "disable --now goaster-telegram.service") {
				t.Fatal("failed start did not stop and disable the adapter")
			}
		})
	}
}
