package ovh

import (
	"bytes"
	"crypto/md5" // OVH/OpenStack may expose the legacy MD5 key fingerprint.
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// KeyPair identifies the local GoAster SSH identity. PublicKey is safe to
// send to OVH; PrivateKeyPath is used only by the local OpenSSH client.
type KeyPair struct {
	PrivateKeyPath string
	PublicKey      string
}

// EnsureLocalKeyPair returns the persistent GoAster key under ~/.goaster. It
// never replaces an existing file and never returns private-key contents.
func EnsureLocalKeyPair() (KeyPair, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return KeyPair{}, fmt.Errorf("find local home directory")
	}
	return EnsureKeyPairAt(filepath.Join(home, ".goaster", "ovh_ed25519"))
}

// EnsureKeyPairAt is injectable for tests and keeps the same safety rules as
// EnsureLocalKeyPair: owner-only private storage and an Ed25519 identity whose
// public and private halves agree.
func EnsureKeyPairAt(privatePath string) (KeyPair, error) {
	if !filepath.IsAbs(privatePath) {
		return KeyPair{}, fmt.Errorf("GoAster SSH key path must be absolute")
	}
	privatePath = filepath.Clean(privatePath)
	publicPath := privatePath + ".pub"
	if err := ensurePrivateDirectory(filepath.Dir(privatePath)); err != nil {
		return KeyPair{}, err
	}

	privateExists, privateErr := pathExists(privatePath)
	publicExists, publicErr := pathExists(publicPath)
	if privateErr != nil {
		return KeyPair{}, privateErr
	}
	if publicErr != nil {
		return KeyPair{}, publicErr
	}
	if privateExists && publicExists {
		return readUsableKeyPair(privatePath, publicPath)
	}
	if privateExists && !publicExists {
		publicKey, err := derivePublicKey(privatePath)
		if err != nil {
			return KeyPair{}, fmt.Errorf("read existing GoAster SSH key")
		}
		if err := writeExclusive(publicPath, []byte(publicKey+"\n"), 0600); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return KeyPair{}, fmt.Errorf("persist GoAster SSH public key")
			}
		}
		return readUsableKeyPair(privatePath, publicPath)
	}
	if publicExists {
		return KeyPair{}, fmt.Errorf("GoAster SSH key pair is incomplete; private key is missing")
	}

	return generateKeyPair(privatePath, publicPath)
}

func ensurePrivateDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create GoAster key directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("GoAster key directory is not a directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("GoAster key directory must be private")
	}
	return nil
}

func pathExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect GoAster SSH key")
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("GoAster SSH key must be a regular file")
	}
	return true, nil
}

func generateKeyPair(privatePath, publicPath string) (KeyPair, error) {
	temporaryDirectory, err := os.MkdirTemp(filepath.Dir(privatePath), ".goaster-key-")
	if err != nil {
		return KeyPair{}, fmt.Errorf("create temporary GoAster key directory")
	}
	defer os.RemoveAll(temporaryDirectory)
	if err := os.Chmod(temporaryDirectory, 0700); err != nil {
		return KeyPair{}, fmt.Errorf("protect temporary GoAster key directory")
	}
	temporaryPrivate := filepath.Join(temporaryDirectory, "ovh_ed25519")
	command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "goaster", "-f", temporaryPrivate)
	command.Stdin = strings.NewReader("")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return KeyPair{}, fmt.Errorf("generate local Ed25519 SSH key; OpenSSH ssh-keygen is required")
	}
	generated, err := readUsableKeyPair(temporaryPrivate, temporaryPrivate+".pub")
	if err != nil {
		return KeyPair{}, fmt.Errorf("validate generated GoAster SSH key")
	}
	privateData, err := os.ReadFile(temporaryPrivate)
	if err != nil {
		return KeyPair{}, fmt.Errorf("read generated GoAster private key")
	}
	if err := writeExclusive(privatePath, privateData, 0600); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readUsableKeyPair(privatePath, publicPath)
		}
		return KeyPair{}, fmt.Errorf("persist local GoAster private key")
	}
	if err := writeExclusive(publicPath, []byte(generated.PublicKey+"\n"), 0600); err != nil {
		if !errors.Is(err, os.ErrExist) {
			_ = os.Remove(privatePath)
			return KeyPair{}, fmt.Errorf("persist local GoAster public key")
		}
		return readUsableKeyPair(privatePath, publicPath)
	}
	return readUsableKeyPair(privatePath, publicPath)
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func readUsableKeyPair(privatePath, publicPath string) (KeyPair, error) {
	privateInfo, err := os.Stat(privatePath)
	if err != nil || !privateInfo.Mode().IsRegular() || privateInfo.Mode().Perm()&0400 == 0 || privateInfo.Mode().Perm()&0077 != 0 {
		return KeyPair{}, fmt.Errorf("GoAster private SSH key must be owner-readable with owner-only permissions")
	}
	publicInfo, err := os.Stat(publicPath)
	if err != nil || !publicInfo.Mode().IsRegular() {
		return KeyPair{}, fmt.Errorf("GoAster public SSH key is not a regular file")
	}
	privatePublic, err := derivePublicKey(privatePath)
	if err != nil {
		return KeyPair{}, fmt.Errorf("GoAster private SSH key is not usable")
	}
	publicData, err := os.ReadFile(publicPath)
	if err != nil {
		return KeyPair{}, fmt.Errorf("read GoAster public SSH key")
	}
	publicKey, err := normalizePublicKey(string(publicData))
	if err != nil {
		return KeyPair{}, fmt.Errorf("GoAster public SSH key is invalid")
	}
	if publicKeyFingerprint(privatePublic) != publicKeyFingerprint(publicKey) {
		return KeyPair{}, fmt.Errorf("GoAster SSH key pair does not match")
	}
	return KeyPair{PrivateKeyPath: privatePath, PublicKey: publicKey}, nil
}

func (p KeyPair) validate() error {
	if !filepath.IsAbs(p.PrivateKeyPath) {
		return fmt.Errorf("private key path must be absolute")
	}
	publicKey, err := normalizePublicKey(p.PublicKey)
	if err != nil {
		return err
	}
	if filepath.Clean(p.PrivateKeyPath) == "." || publicKey == "" {
		return fmt.Errorf("key pair is incomplete")
	}
	return nil
}

func derivePublicKey(privatePath string) (string, error) {
	command := exec.Command("ssh-keygen", "-y", "-f", privatePath)
	command.Stdin = strings.NewReader("")
	command.Stderr = io.Discard
	var output bytes.Buffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return "", err
	}
	return normalizePublicKey(output.String())
}

func normalizePublicKey(value string) (string, error) {
	fields := strings.Fields(value)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", fmt.Errorf("public key must be an Ed25519 OpenSSH key")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) != 51 || !bytes.Equal(blob[:4], []byte{0, 0, 0, 11}) || string(blob[4:15]) != "ssh-ed25519" || !bytes.Equal(blob[15:19], []byte{0, 0, 0, 32}) {
		return "", fmt.Errorf("public key must contain a valid Ed25519 key")
	}
	return fields[0] + " " + fields[1] + " goaster", nil
}

func publicKeyFingerprint(publicKey string) string {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
}

func publicKeyMD5Fingerprint(publicKey string) string {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return ""
	}
	digest := md5.Sum(blob)
	parts := make([]string, len(digest))
	for i, value := range digest {
		parts[i] = fmt.Sprintf("%02x", value)
	}
	return strings.Join(parts, ":")
}

func publicKeyHash(publicKey string) string {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(blob)
	return fmt.Sprintf("%x", digest[:])
}
