package ovh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goaster/internal/localenv"
)

const runtimeToken = "123456789:abcdefghijklmnopqrstuvwxyzABCDE_12345"

func completeRuntime(t *testing.T) RuntimeBundle {
	t.Helper()
	values := testValues()
	values["TELEGRAM_BOT_TOKEN"] = runtimeToken
	values["UNRELATED_SECRET"] = "must-stay-local"
	b, err := NewRuntimeBundle([]byte(`{"access_token":"fake-access","refresh_token":"fake-refresh","account_id":"fake-account","expires_at":"2030-01-01T00:00:00Z"}`), values)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRuntimeBundleContentsAndRedaction(t *testing.T) {
	b := completeRuntime(t)
	if len(b.Values) != 5 {
		t.Fatal("bundle must contain only runtime settings")
	}
	for _, key := range []string{"OVH_ENDPOINT", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY", "TELEGRAM_BOT_TOKEN"} {
		if b.Values[key] == "" {
			t.Fatalf("missing %s", key)
		}
	}
	for _, s := range []string{fmt.Sprintf("%v", b), fmt.Sprintf("%+v", b), fmt.Sprintf("%#v", b)} {
		if strings.Contains(s, "fake-access") || strings.Contains(s, runtimeToken) {
			t.Fatal("bundle formatting leaked secrets")
		}
	}
}

func TestRuntimeSSHStdinAndFailureGates(t *testing.T) {
	b := completeRuntime(t)
	for _, failure := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			calls := 0
			ssh := OpenSSH{Run: func(_ context.Context, args []string, input io.Reader) (string, error) {
				calls++
				data, err := io.ReadAll(input)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"fake-access", "fake-refresh", "fake-app-secret", "fake-consumer-secret", runtimeToken} {
					if strings.Contains(strings.Join(args, " "), secret) || (calls != 2 && strings.Contains(string(data), secret)) {
						t.Fatal("secret outside JSON stdin")
					}
				}
				if calls == 1 && strings.Contains(string(data), "systemctl restart") {
					t.Fatal("service started before credentials")
				}
				if calls == 2 {
					if !strings.HasSuffix(strings.Join(args, " "), "sudo -n /usr/local/bin/goaster-agent setup-runtime") {
						t.Fatal("non-static receiver command")
					}
					var got RuntimeBundle
					if json.Unmarshal(data, &got) != nil || !reflect.DeepEqual(got, b) {
						t.Fatal("incomplete stdin bundle")
					}
					if strings.Contains(string(data), "must-stay-local") || strings.Contains(string(data), "/local/fake-key") {
						t.Fatal("local settings transferred")
					}
				}
				if calls == failure {
					return "fake-access", fmt.Errorf("fake-app-secret %s", runtimeToken)
				}
				if calls == 3 {
					return testRef + "\nactive\n", nil
				}
				return "", nil
			}}
			_, _, err := ssh.Install(context.Background(), Target{IP: "203.0.113.7", User: "goaster", PrivateKey: "/local/fake-key"}, testRef, b)
			wantCalls := 3
			if failure > 0 {
				wantCalls = failure
			}
			if calls != wantCalls || (err != nil) != (failure > 0) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if err != nil && (strings.Contains(err.Error(), "fake-app-secret") || strings.Contains(err.Error(), runtimeToken)) {
				t.Fatal("failure leaked secret")
			}
		})
	}
}

func TestRuntimeStorageAndPermissions(t *testing.T) {
	b := completeRuntime(t)
	data, _ := json.Marshal(b)
	root := t.TempDir()
	directory := filepath.Join(root, ".goaster")
	token := filepath.Join(root, "telegram-token")
	if err := StoreRuntime(bytes.NewReader(data), directory, token, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{directory, filepath.Join(directory, "auth.json"), filepath.Join(directory, "env"), token} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if p == directory {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("wrong permissions: %s", p)
		}
	}
	auth, err := os.ReadFile(filepath.Join(directory, "auth.json"))
	if err != nil || !bytes.Equal(auth, b.Auth) {
		t.Fatal("subscription state changed")
	}
	store := localenv.NewStoreAt(filepath.Join(directory, "env"))
	if err := store.Load(); err != nil || !reflect.DeepEqual(store.Values(), b.Values) {
		t.Fatal("remote CLI cannot consume runtime settings")
	}
	got, _ := os.ReadFile(token)
	if string(got) != runtimeToken {
		t.Fatal("adapter token missing")
	}
	if err := StoreRuntime(bytes.NewReader(data), directory, token, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("existing authentication replaced")
	}
}

func TestRuntimeStorageRejectsInvalidAndFailedInput(t *testing.T) {
	b := completeRuntime(t)
	valid, _ := json.Marshal(b)
	for name, data := range map[string][]byte{"truncated": valid[:len(valid)-1], "trailing": append(append([]byte{}, valid...), []byte(` {}`)...), "oversized": bytes.Repeat([]byte("x"), (2<<20)+1), "missing": []byte(`{"auth":{"access_token":"fake-access"},"values":{}}`), "unknown": []byte(`{"private_key":"fake-secret"}`)} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, ".goaster")
			if err := StoreRuntime(bytes.NewReader(data), directory, filepath.Join(root, "token"), os.Getuid(), os.Getgid()); err == nil {
				t.Fatal("invalid input accepted")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("invalid transfer left files")
			}
		})
	}
	t.Run("token write failure", func(t *testing.T) {
		root := t.TempDir()
		if err := StoreRuntime(bytes.NewReader(valid), filepath.Join(root, ".goaster"), filepath.Join(root, "missing", "token"), os.Getuid(), os.Getgid()); err == nil {
			t.Fatal("write failure ignored")
		}
		entries, _ := os.ReadDir(root)
		if len(entries) != 0 {
			t.Fatal("failed transfer left staged credentials")
		}
	})
}

func TestRuntimeStorageRejectsExistingTelegramState(t *testing.T) {
	for _, state := range []string{"file", "directory", "symlink", "dangling symlink"} {
		for _, withToken := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/token=%t", state, withToken), func(t *testing.T) {
				root := t.TempDir()
				directory := filepath.Join(root, ".goaster")
				token := filepath.Join(root, "telegram-token")
				target := filepath.Join(root, "target")
				const original = "existing Telegram state"
				switch state {
				case "file":
					if err := os.WriteFile(token, []byte(original), 0600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(token, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(token, "state"), []byte(original), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink", "dangling symlink":
					if state == "symlink" {
						if err := os.WriteFile(target, []byte(original), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(target, token); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.Lstat(token)
				if err != nil {
					t.Fatal(err)
				}
				entriesBefore, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				b := completeRuntime(t)
				if !withToken {
					delete(b.Values, "TELEGRAM_BOT_TOKEN")
				}
				data, _ := json.Marshal(b)
				if err := StoreRuntime(bytes.NewReader(data), directory, token, os.Getuid(), os.Getgid()); err == nil {
					t.Fatal("existing Telegram state accepted")
				}
				after, err := os.Lstat(token)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatal("existing Telegram state replaced or permissions changed")
				}
				path := token
				if state == "directory" {
					path = filepath.Join(token, "state")
				}
				if state == "dangling symlink" {
					if _, err := os.Lstat(target); !os.IsNotExist(err) {
						t.Fatal("dangling symlink target created")
					}
				} else if got, err := os.ReadFile(path); err != nil || string(got) != original {
					t.Fatal("existing Telegram contents changed")
				}
				if before.Mode()&os.ModeSymlink != 0 {
					if got, err := os.Readlink(token); err != nil || got != target {
						t.Fatal("Telegram symlink changed")
					}
				}
				entriesAfter, err := os.ReadDir(root)
				if err != nil || !reflect.DeepEqual(entriesBefore, entriesAfter) {
					t.Fatal("rejected installation left files")
				}
			})
		}
	}
}

func TestRuntimeStorageRollsBackTokenOnPublicationFailure(t *testing.T) {
	b := completeRuntime(t)
	data, _ := json.Marshal(b)
	root := t.TempDir()
	directory := filepath.Join(root, "runtime")
	// Publishing a token at the runtime destination makes the final directory
	// rename fail after the token has been successfully published.
	if err := StoreRuntime(bytes.NewReader(data), directory, directory, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("runtime publication failure ignored")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed publication left token or staged credentials")
	}
}

func TestRuntimeStorageWithoutTelegramToken(t *testing.T) {
	b := completeRuntime(t)
	delete(b.Values, "TELEGRAM_BOT_TOKEN")
	data, _ := json.Marshal(b)
	root := t.TempDir()
	directory := filepath.Join(root, "runtime")
	token := filepath.Join(root, "telegram-token")
	if err := StoreRuntime(bytes.NewReader(data), directory, token, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(token); !os.IsNotExist(err) {
		t.Fatal("installation without Telegram created token state")
	}
	if got, err := os.ReadFile(filepath.Join(directory, "auth.json")); err != nil || !bytes.Equal(got, b.Auth) {
		t.Fatal("subscription state missing or changed")
	}
}

func TestRuntimeServiceFileWiring(t *testing.T) {
	installer := fmt.Sprintf(installScript, testRef, Repository)
	for _, want := range []string{"User=goaster", "Environment=HOME=/var/lib/goaster", "ExecStart=/usr/local/bin/goaster-agent", "ReadWritePaths=/var/lib/goaster", "ExecStart=/usr/local/bin/goaster-telegram", "-o root -g root -m 0700 /etc/goaster"} {
		if !strings.Contains(installer, want) {
			t.Fatalf("missing service wiring %s", want)
		}
	}
	if strings.Contains(installer, "systemctl enable") || strings.Contains(installer, "systemctl restart") {
		t.Fatal("installer starts services before credentials")
	}
	if strings.Contains(agentStartScript, "goaster-telegram") {
		t.Fatal("agent startup bypasses Telegram confirmation")
	}
}

func TestInvalidRuntimeBundleStopsBeforeOVHCalls(t *testing.T) {
	config, _ := ConfigFromValues(testValues())
	fake := &fakeHTTP{t: t}
	client, _ := NewClient(config, fake)
	w := Workflow{Client: client, SSH: &fakeSSH{}, Remote: &fakeRemote{}, Confirm: func(context.Context, Plan) (bool, error) {
		t.Fatal("invalid bundle reached confirmation")
		return false, nil
	}}
	if _, err := w.Run(context.Background()); err == nil || len(fake.requests) != 0 {
		t.Fatal("invalid runtime bundle reached OVH")
	}
}
