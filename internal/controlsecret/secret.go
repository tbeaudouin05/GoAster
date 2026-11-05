// Package controlsecret loads the shared agent and adapter authorization secret.
package controlsecret

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const (
	ValueEnv = "GOASTER_GITHUB_AUTH_CONTROL_SECRET"
	FileEnv  = "GOASTER_GITHUB_AUTH_CONTROL_SECRET_FILE"
)

// Load retains the original environment setting for existing deployments. A
// configured file must instead contain the provisioned 32-byte hex secret;
// it never falls back to an environment value if the file is unusable.
func Load() (string, error) {
	path, fileConfigured := os.LookupEnv(FileEnv)
	value := os.Getenv(ValueEnv)
	if !fileConfigured {
		value = strings.TrimSpace(value)
		if strings.ContainsAny(value, "\r\n") {
			value = ""
		}
		return value, nil
	}
	invalid := errors.New("control secret configuration is invalid")
	if value != "" || !secureParents(path) {
		return "", invalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !trustedOwner(info) || info.Mode().Perm()&0077 != 0 || info.Size() < 64 || info.Size() > 65 {
		return "", invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || !trustedOwner(opened) || opened.Mode().Perm()&0077 != 0 {
		return "", invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil {
		return "", invalid
	}
	value = strings.TrimSuffix(string(data), "\n")
	if len(value) != 64 || strings.ToLower(value) != value {
		return "", invalid
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", invalid
	}
	return value, nil
}

// Check ancestors from the root down, so no untrusted directory can replace
// a later component after it is inspected. Sticky directories protect entries
// owned by the service user or root, which we check at the next component.
func secureParents(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	var dirs []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		info, err := os.Lstat(dirs[i])
		if err != nil || !info.IsDir() || !trustedOwner(info) {
			return false
		}
		if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return false
		}
	}
	return true
}

// FileInfo exposes Unix ownership through its platform metadata. Fail closed
// on systems where that ownership cannot be checked.
func trustedOwner(info os.FileInfo) bool {
	if os.Geteuid() < 0 {
		return false
	}
	metadata := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !metadata.IsValid() || metadata.Kind() != reflect.Struct {
		return false
	}
	owner := metadata.FieldByName("Uid")
	return owner.IsValid() && owner.CanUint() && (owner.Uint() == 0 || owner.Uint() == uint64(os.Geteuid()))
}
