package ovh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// OpenSSH uses the local ssh executable and the user's known_hosts file.
// It never forwards local environment settings or prints remote command output.
type OpenSSH struct{}

func (OpenSSH) command(ctx context.Context, target Target, script string) (string, error) {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10", "-o", "IdentitiesOnly=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-i", target.PrivateKey, "--", target.User + "@" + target.IP, "sh", "-s"}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(script)
	var output bytes.Buffer
	cmd.Stdout = &limitedWriter{writer: &output, remaining: 4096}
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("SSH command failed")
	}
	return output.String(), nil
}

func (s OpenSSH) Check(ctx context.Context, target Target) error {
	_, err := s.command(ctx, target, "exit 0\n")
	return err
}

func (s OpenSSH) Install(ctx context.Context, target Target, ref string) (string, string, error) {
	if !commitID.MatchString(ref) {
		return "", "", fmt.Errorf("invalid commit ID")
	}
	// ref is restricted to lowercase hexadecimal, so interpolation is safe.
	script := fmt.Sprintf(installScript, ref, Repository)
	output, err := s.command(ctx, target, script)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 || !commitID.MatchString(lines[0]) || lines[1] != "active" {
		return "", "", fmt.Errorf("invalid service verification response")
	}
	return lines[0], lines[1], nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	if len(p) > 0 {
		_, _ = w.writer.Write(p)
		w.remaining -= len(p)
	}
	return n, nil
}

// The installer is deliberately limited to a fresh Ubuntu 24.04 server with
// passwordless sudo. The selected ref must already be in the public repository.
const installScript = `set -eu
ref='%s'
repo='%s'
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sudo -n env DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null
sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git golang-go ca-certificates curl >/dev/null
GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 git clone --quiet "$repo" "$work/repo"
cd "$work/repo"
git checkout --quiet --detach "$ref"
commit=$(git rev-parse HEAD)
test "$commit" = "$ref"
go build -o "$work/goaster" ./cmd/goaster
go build -o "$work/goaster-agent" ./cmd/goaster-agent
sudo -n install -m 0755 "$work/goaster" /usr/local/bin/goaster
sudo -n install -m 0755 "$work/goaster-agent" /usr/local/bin/goaster-agent
if ! id goaster >/dev/null 2>&1; then
  sudo -n useradd --system --create-home --home-dir /var/lib/goaster --shell /usr/sbin/nologin goaster
fi
sudo -n install -d -o goaster -g goaster -m 0700 /var/lib/goaster
printf '[Unit]\nDescription=GoAster remote agent\nAfter=network-online.target\n[Service]\nUser=goaster\nEnvironment=HOME=/var/lib/goaster\nExecStart=/usr/local/bin/goaster-agent\nRestart=on-failure\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=true\nReadWritePaths=/var/lib/goaster\n[Install]\nWantedBy=multi-user.target\n' | sudo -n tee /etc/systemd/system/goaster-agent.service >/dev/null
sudo -n systemctl daemon-reload
sudo -n systemctl enable goaster-agent.service >/dev/null 2>&1
sudo -n systemctl restart goaster-agent.service >/dev/null 2>&1
status=$(systemctl is-active goaster-agent.service)
test "$status" = active
ready=0
for n in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS --max-time 2 http://127.0.0.1:8765/healthz >/dev/null; then ready=1; break; fi
  sleep 1
done
test "$ready" = 1
status=$(systemctl is-active goaster-agent.service)
test "$status" = active
printf '%%s\n%%s\n' "$commit" "$status"
`
