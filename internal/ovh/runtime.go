package ovh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"goaster/internal/localenv"
	"goaster/internal/telegram"
)

// RuntimeBundle deliberately excludes local paths, SSH keys, and unrelated settings.
// Auth contains the complete subscription state, including refresh and account data.
type RuntimeBundle struct {
	Auth   json.RawMessage   `json:"auth"`
	Values map[string]string `json:"values"`
}

func (RuntimeBundle) String() string   { return "runtime credentials (redacted)" }
func (RuntimeBundle) GoString() string { return "runtime credentials (redacted)" }

func NewRuntimeBundle(auth []byte, values map[string]string) (RuntimeBundle, error) {
	b := RuntimeBundle{Auth: append(json.RawMessage(nil), auth...), Values: make(map[string]string)}
	for _, name := range []string{"OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY", "TELEGRAM_BOT_TOKEN"} {
		if value := values[name]; value != "" {
			b.Values[name] = value
		}
	}
	return b, b.validate()
}

func (b RuntimeBundle) validate() error {
	var auth struct {
		AccessToken string `json:"access_token"`
	}
	if len(b.Auth) > 1<<20 || json.Unmarshal(b.Auth, &auth) != nil || auth.AccessToken == "" {
		return fmt.Errorf("invalid runtime subscription credential")
	}
	if _, err := ConfigFromValues(b.Values); err != nil {
		return fmt.Errorf("invalid runtime OVH credentials")
	}
	for name := range b.Values {
		switch name {
		case "OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY", "TELEGRAM_BOT_TOKEN":
		default:
			return fmt.Errorf("unexpected runtime setting")
		}
	}
	return telegram.ValidateToken(b.Values["TELEGRAM_BOT_TOKEN"])
}

// StoreRuntime receives bounded JSON on stdin. The caller must be root in production.
// The private directory is staged beside its destination and published only when
// complete. Existing runtime or Telegram token state is rejected. If publishing
// the runtime directory fails, the newly published token is removed.
func StoreRuntime(input io.Reader, directory, tokenPath string, uid, gid int) error {
	fail := func() error { return fmt.Errorf("runtime credential installation failed") }
	data, err := io.ReadAll(io.LimitReader(input, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return fail()
	}
	var b RuntimeBundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || dec.Decode(&struct{}{}) != io.EOF || b.validate() != nil {
		return fail()
	}
	// Fresh provisioning only: never replace an existing authentication directory.
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		return fail()
	}
	if _, err := os.Lstat(tokenPath); !os.IsNotExist(err) {
		return fail()
	}
	stage, err := os.MkdirTemp(filepath.Dir(directory), ".runtime-*")
	if err != nil {
		return fail()
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0700); err != nil {
		return fail()
	}
	if err := os.WriteFile(filepath.Join(stage, "auth.json"), b.Auth, 0600); err != nil {
		return fail()
	}
	if err := localenv.NewStoreAt(filepath.Join(stage, "env")).Save(b.Values); err != nil {
		return fail()
	}
	for _, path := range []string{filepath.Join(stage, "auth.json"), filepath.Join(stage, "env"), stage} {
		if err := os.Chown(path, uid, gid); err != nil {
			return fail()
		}
	}
	tokenPublished := false
	installed := false
	defer func() {
		if tokenPublished && !installed {
			os.Remove(tokenPath)
		}
	}()
	token := b.Values["TELEGRAM_BOT_TOKEN"]
	// The parent is root-owned 0700, created by the installer. A hard link
	// publishes the complete token without replacing any destination,
	// including a symlink or a token created after the absence check.
	f, err := os.CreateTemp(filepath.Dir(tokenPath), ".token-*")
	if err != nil {
		return fail()
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return fail()
	}
	_, writeErr := f.WriteString(token)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return fail()
	}
	if err := os.Link(name, tokenPath); err != nil {
		return fail()
	}
	tokenPublished = true
	if err := os.Rename(stage, directory); err != nil {
		return fail()
	}
	installed = true
	return nil
}
