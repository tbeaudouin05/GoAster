package telegram

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "123456789:abcdefghijklmnopqrstuvwxyzABCDE_12345"
const testCode = "abcdefghijklmnopqrstuvwxABCDEFGH"

func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{filepath.Join(dir, "etc", "token"), filepath.Join(dir, "state", "hash"), filepath.Join(dir, "state", "user")}
}

func testHash(code string) string {
	digest := sha256.Sum256([]byte(code))
	return hex.EncodeToString(digest[:])
}

func setupTest(t *testing.T, paths Paths) {
	t.Helper()
	data, err := json.Marshal(SetupInput{Token: testToken, PairingHash: testHash(testCode)})
	if err != nil {
		t.Fatal(err)
	}
	if err := Setup(bytes.NewReader(data), paths); err != nil {
		t.Fatal(err)
	}
}

func TestTokenValidation(t *testing.T) {
	for _, tc := range []struct {
		token string
		valid bool
	}{
		{testToken, true},
		{"", false},
		{"0:abcdefghijklmnopqrstuvwxyz", false},
		{"123:short", false},
		{" " + testToken, false},
		{testToken + "\n", false},
		{testToken + "'$(touch /tmp/not-a-command)'", false},
		{testToken + "/method", false},
	} {
		err := ValidateToken(tc.token)
		if (err == nil) != tc.valid {
			t.Errorf("valid = %t for test token, error = %v", tc.valid, err)
		}
		if err != nil && tc.token != "" && strings.Contains(err.Error(), tc.token) {
			t.Error("validation error exposed the token")
		}
	}
}

func TestSetupProtectsFilesAndNeverReplacesPair(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	for _, path := range []string{paths.Token, paths.PairingHash} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("private file %s: mode %v, error %v", path, info, err)
		}
	}
	for _, path := range []string{filepath.Dir(paths.Token), filepath.Dir(paths.UserID)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("private directory %s: mode %v, error %v", path, info, err)
		}
	}
	data, err := os.ReadFile(paths.PairingHash)
	if err != nil || string(data) != testHash(testCode) || strings.Contains(string(data), testCode) {
		t.Fatal("pairing verifier is invalid")
	}
	p, err := loadPairing(paths)
	if err != nil || p.tryPair(42, "wrong") || !p.tryPair(42, testCode) || p.tryPair(99, testCode) {
		t.Fatal("pairing did not require the one-time code")
	}
	info, err := os.Stat(paths.UserID)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("paired user file is not private")
	}
	if data, err := os.ReadFile(paths.UserID); err != nil || string(data) != "42" {
		t.Fatal("paired numeric user ID was not saved")
	}
	if _, err := os.Stat(paths.PairingHash); !os.IsNotExist(err) {
		t.Fatal("pairing verifier was not removed")
	}
	reloaded, err := loadPairing(paths)
	if err != nil || reloaded.userID != 42 || reloaded.tryPair(99, testCode) {
		t.Fatal("restarting did not preserve the paired user")
	}
	payload, _ := json.Marshal(SetupInput{Token: "999999:" + strings.Repeat("b", 32), PairingHash: testHash("another-code")})
	if err := Setup(bytes.NewReader(payload), paths); err == nil {
		t.Fatal("setup replaced an existing pairing")
	}
	if token, err := os.ReadFile(paths.Token); err != nil || string(token) != testToken {
		t.Fatal("refused setup changed the token")
	}
}

func TestSetupRejectsMalformedInputAndUnsafeFiles(t *testing.T) {
	paths := testPaths(t)
	for _, input := range []string{
		`{"token":"` + testToken + `'; touch BAD; echo '","pairing_hash":"` + testHash(testCode) + `"}`,
		`{"token":"` + testToken + `","pairing_hash":"bad"}`,
		`{"token":"` + testToken + `","pairing_hash":"` + testHash(testCode) + `","extra":1}`,
		`{"token":"` + testToken + `","pairing_hash":"` + testHash(testCode) + `"} {}`,
		`{"token":"` + testToken + `","pairing_hash":"` + testHash(testCode) + `"}` + strings.Repeat(" ", 2049),
	} {
		err := Setup(strings.NewReader(input), paths)
		if err == nil || strings.Contains(err.Error(), testToken) {
			t.Fatal("malformed input was accepted or exposed a token")
		}
	}
	setupTest(t, paths)
	if err := os.Chmod(paths.PairingHash, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPairing(paths); err == nil {
		t.Fatal("accepted world-readable pairing state")
	}
	if err := os.Remove(paths.PairingHash); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths.Token, paths.PairingHash); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := loadPairing(paths); err == nil {
		t.Fatal("followed a symlink for pairing state")
	}
}
