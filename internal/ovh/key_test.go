package ovh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureKeyPairCreatesReusesAndRepairsPublicHalf(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "nested", "ovh_ed25519")
	first, err := EnsureKeyPairAt(privatePath)
	if err != nil {
		if strings.Contains(err.Error(), "ssh-keygen is required") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if first.PrivateKeyPath != privatePath || !strings.HasPrefix(first.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("unexpected key pair: %+v", first)
	}
	privateBefore, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm()&0077 != 0 || privateInfo.Mode().Perm()&0400 == 0 {
		t.Fatalf("private key permissions are not owner-only: %o", privateInfo.Mode().Perm())
	}

	second, err := EnsureKeyPairAt(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("key pair was not reused: first=%+v second=%+v", first, second)
	}
	privateAfter, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(privateBefore) != string(privateAfter) {
		t.Fatal("reusing the key pair replaced the private key")
	}

	if err := os.Remove(privatePath + ".pub"); err != nil {
		t.Fatal(err)
	}
	repaired, err := EnsureKeyPairAt(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != first {
		t.Fatalf("repair changed the key identity: first=%+v repaired=%+v", first, repaired)
	}
}

func TestEnsureKeyPairRejectsMismatchedPublicHalf(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "keys", "ovh_ed25519")
	pair, err := EnsureKeyPairAt(privatePath)
	if err != nil {
		if strings.Contains(err.Error(), "ssh-keygen is required") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if err := os.WriteFile(privatePath+".pub", []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureKeyPairAt(privatePath); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched public key was accepted: %v (original=%+v)", err, pair)
	}
}
