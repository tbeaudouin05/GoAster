// Package telegram implements the private, single-user Telegram adapter.
package telegram

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var tokenFormat = regexp.MustCompile(`^[1-9][0-9]{0,19}:[A-Za-z0-9_-]{20,128}$`)

// ValidateToken rejects malformed tokens without including them in errors.
func ValidateToken(token string) error {
	if !tokenFormat.MatchString(token) {
		return fmt.Errorf("invalid TELEGRAM_BOT_TOKEN; use the token supplied by @BotFather")
	}
	return nil
}

// PairingCode creates an ephemeral 192-bit code and its stored verifier.
func PairingCode() (string, string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", fmt.Errorf("could not generate a Telegram pairing code")
	}
	code := base64.RawURLEncoding.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(code))
	return code, hex.EncodeToString(digest[:]), nil
}

// Paths contains root-only server state locations. No secrets are in the unit.
type Paths struct {
	Token, PairingHash, UserID string
}

var DefaultPaths = Paths{
	Token:       "/etc/goaster/telegram-token",
	PairingHash: "/var/lib/goaster-telegram/pairing-hash",
	UserID:      "/var/lib/goaster-telegram/user-id",
}

// SetupInput is the separately confirmed Telegram activation payload. Runtime
// provisioning also transfers credentials. The hash cannot reveal the random code.
type SetupInput struct {
	Token       string `json:"token"`
	PairingHash string `json:"pairing_hash"`
}

// Setup reads its bounded input from stdin and writes root-owned 0600 files.
// An existing pairing is never replaced by a subsequent setup attempt.
func Setup(input io.Reader, paths Paths) error {
	var payload SetupInput
	data, err := io.ReadAll(io.LimitReader(input, 2049))
	if err != nil || len(data) > 2048 {
		return fmt.Errorf("invalid Telegram setup input")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("invalid Telegram setup input")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid Telegram setup input")
	}
	if err := ValidateToken(payload.Token); err != nil {
		return err
	}
	if !validHash(payload.PairingHash) {
		return fmt.Errorf("invalid Telegram pairing verifier")
	}
	for _, dir := range []string{filepath.Dir(paths.Token), filepath.Dir(paths.PairingHash), filepath.Dir(paths.UserID)} {
		if err := privateDirectory(dir); err != nil {
			return fmt.Errorf("could not protect Telegram state directory")
		}
	}
	if _, err := os.Lstat(paths.UserID); err == nil || !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Telegram is already paired or its pairing state is unavailable")
	}
	if err := writePrivate(paths.Token, []byte(payload.Token)); err != nil {
		return fmt.Errorf("could not save Telegram token")
	}
	if err := writePrivate(paths.PairingHash, []byte(payload.PairingHash)); err != nil {
		return fmt.Errorf("could not save Telegram pairing verifier")
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func privateDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid directory")
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 0, 0); err != nil {
			return err
		}
	}
	return os.Chmod(dir, 0700)
}

func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".telegram-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1024 {
		return nil, fmt.Errorf("invalid private file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("private file changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(data) > 1024 {
		return nil, fmt.Errorf("could not read private file")
	}
	return data, nil
}

type pairing struct {
	paths  Paths
	hash   [sha256.Size]byte
	userID int64
}

func loadPairing(paths Paths) (*pairing, error) {
	p := &pairing{paths: paths}
	data, err := readPrivate(paths.UserID)
	if err == nil {
		p.userID, err = strconv.ParseInt(string(data), 10, 64)
		if err != nil || p.userID <= 0 {
			return nil, fmt.Errorf("invalid Telegram paired user")
		}
		// A crash after pairing may have left the verifier behind.
		if err := os.Remove(paths.PairingHash); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not remove Telegram pairing verifier")
		}
		return p, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		// readPrivate deliberately hides file errors; check absence separately.
		if _, statErr := os.Lstat(paths.UserID); !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("invalid Telegram paired user file")
		}
	}
	data, err = readPrivate(paths.PairingHash)
	if err != nil || !validHash(string(data)) {
		return nil, fmt.Errorf("invalid Telegram pairing verifier file")
	}
	decoded, _ := hex.DecodeString(string(data))
	copy(p.hash[:], decoded)
	return p, nil
}

func (p *pairing) tryPair(userID int64, code string) bool {
	if p.userID != 0 || userID <= 0 {
		return false
	}
	digest := sha256.Sum256([]byte(code))
	if subtle.ConstantTimeCompare(digest[:], p.hash[:]) != 1 {
		return false
	}
	// O_EXCL prevents another process or later request from replacing a pair.
	f, err := os.OpenFile(p.paths.UserID, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(p.paths.UserID)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return false
	}
	if _, err := f.WriteString(strconv.FormatInt(userID, 10)); err != nil {
		return false
	}
	if err := f.Sync(); err != nil {
		return false
	}
	if err := f.Close(); err != nil {
		return false
	}
	ok = true
	p.userID = userID
	_ = os.Remove(p.paths.PairingHash)
	p.hash = [sha256.Size]byte{}
	return true
}
