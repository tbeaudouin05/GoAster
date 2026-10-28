package ovh

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
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
