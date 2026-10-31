package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	remoteTargetRecordSuffix = ".goaster-target.json"
	remoteTargetRecordLimit  = 16 * 1024
	remoteTargetCheckTimeout = 30 * time.Second
)

// remoteTargetRecord contains only non-secret SSH destination details.
type remoteTargetRecord struct {
	Target     string `json:"target"`
	Port       int    `json:"port"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

type savedRemoteTarget struct {
	Record       remoteTargetRecord
	IdentityFile string
}

func remoteTargetRecordPath(home, identityFile string) (string, bool, error) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(identityFile) == "" {
		return "", false, errors.New("remote target path is empty")
	}
	if strings.IndexByte(home, 0) >= 0 || strings.IndexByte(identityFile, 0) >= 0 {
		return "", false, errors.New("remote target path contains an invalid NUL byte")
	}
	homePath, err := filepath.Abs(home)
	if err != nil {
		return "", false, fmt.Errorf("resolve local home directory")
	}
	identityPath, err := filepath.Abs(identityFile)
	if err != nil {
		return "", false, fmt.Errorf("resolve SSH identity path")
	}
	sshDirectory := filepath.Join(homePath, ".ssh")
	if filepath.Clean(filepath.Dir(identityPath)) != filepath.Clean(sshDirectory) {
		return "", false, nil
	}
	name := filepath.Base(identityPath)
	if name == "." || name == ".." || strings.HasSuffix(name, remoteTargetRecordSuffix) {
		return "", false, errors.New("SSH identity path cannot name a connection record")
	}
	return identityPath + remoteTargetRecordSuffix, true, nil
}

func saveRemoteTargetRecord(home, identityFile string, record remoteTargetRecord) error {
	path, eligible, err := remoteTargetRecordPath(home, identityFile)
	if err != nil {
		return err
	}
	if !eligible {
		return errors.New("SSH identity must be directly inside the local .ssh directory")
	}
	if err := validateRemoteTargetRecord(record, home); err != nil {
		return err
	}
	if err := validateRemoteIdentityFile(identityFile); err != nil {
		return err
	}
	if err := ensureRemoteTargetDirectory(home); err != nil {
		return err
	}
	if _, err := readRemoteTargetRecord(path, home); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing connection record is invalid: %w", err)
	}

	content, err := json.Marshal(record)
	if err != nil {
		return errors.New("encode remote connection record")
	}
	content = append(content, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".goaster-target-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary connection record: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary connection record: %w", err)
	}
	written, err := temporary.Write(content)
	if err != nil || written != len(content) {
		_ = temporary.Close()
		if err == nil {
			err = io.ErrShortWrite
		}
		return fmt.Errorf("write temporary connection record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary connection record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary connection record: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace connection record: %w", err)
	}
	return nil
}

func loadRemoteTargetRecord(home, identityFile string) (remoteTargetRecord, error) {
	path, eligible, err := remoteTargetRecordPath(home, identityFile)
	if err != nil {
		return remoteTargetRecord{}, err
	}
	if !eligible {
		return remoteTargetRecord{}, errors.New("SSH identity must be directly inside the local .ssh directory")
	}
	return readRemoteTargetRecord(path, home)
}

func readRemoteTargetRecord(path, home string) (remoteTargetRecord, error) {
	if err := inspectRemoteTargetDirectory(home); err != nil {
		return remoteTargetRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return remoteTargetRecord{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return remoteTargetRecord{}, errors.New("connection record must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return remoteTargetRecord{}, errors.New("connection record must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return remoteTargetRecord{}, errors.New("connection record permissions are too broad")
	}
	if info.Size() > remoteTargetRecordLimit {
		return remoteTargetRecord{}, errors.New("connection record exceeds the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return remoteTargetRecord{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return remoteTargetRecord{}, errors.New("inspect opened connection record")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return remoteTargetRecord{}, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return remoteTargetRecord{}, errors.New("connection record changed while it was opened")
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0077 != 0 || openedInfo.Size() > remoteTargetRecordLimit {
		return remoteTargetRecord{}, errors.New("opened connection record is not a private regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, remoteTargetRecordLimit+1))
	if err != nil || len(content) > remoteTargetRecordLimit {
		return remoteTargetRecord{}, errors.New("read connection record")
	}
	return decodeRemoteTargetRecord(content, home)
}

func decodeRemoteTargetRecord(content []byte, home string) (remoteTargetRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil {
		return remoteTargetRecord{}, errors.New("connection record is malformed JSON")
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return remoteTargetRecord{}, errors.New("connection record must be a JSON object")
	}

	var record remoteTargetRecord
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return remoteTargetRecord{}, errors.New("connection record is malformed JSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return remoteTargetRecord{}, errors.New("connection record field name is invalid")
		}
		if key != "target" && key != "port" && key != "known_hosts" {
			return remoteTargetRecord{}, errors.New("connection record contains an unknown field")
		}
		if seen[key] {
			return remoteTargetRecord{}, errors.New("connection record contains a duplicate field")
		}
		seen[key] = true
		value, err := decoder.Token()
		if err != nil {
			return remoteTargetRecord{}, errors.New("connection record is malformed JSON")
		}
		switch key {
		case "target":
			record.Target, ok = value.(string)
			if !ok {
				return remoteTargetRecord{}, errors.New("connection target must be a string")
			}
		case "port":
			number, isNumber := value.(json.Number)
			if !isNumber {
				return remoteTargetRecord{}, errors.New("connection port must be an integer")
			}
			port, parseErr := strconv.Atoi(string(number))
			if parseErr != nil {
				return remoteTargetRecord{}, errors.New("connection port must be an integer")
			}
			record.Port = port
		case "known_hosts":
			record.KnownHosts, ok = value.(string)
			if !ok {
				return remoteTargetRecord{}, errors.New("known_hosts must be a string")
			}
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return remoteTargetRecord{}, errors.New("connection record is malformed JSON")
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return remoteTargetRecord{}, errors.New("connection record is malformed JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return remoteTargetRecord{}, errors.New("connection record contains trailing data")
	}
	if !seen["target"] || !seen["port"] {
		return remoteTargetRecord{}, errors.New("connection record is missing required fields")
	}
	if err := validateRemoteTargetRecord(record, home); err != nil {
		return remoteTargetRecord{}, err
	}
	return record, nil
}

func validateRemoteTargetRecord(record remoteTargetRecord, home string) error {
	if strings.TrimSpace(record.Target) == "" {
		return errors.New("connection target is empty")
	}
	if hasWhitespaceOrControl(record.Target) || strings.HasPrefix(record.Target, "-") {
		return errors.New("connection target must be a single SSH destination")
	}
	if record.Port < 1 || record.Port > 65535 {
		return errors.New("connection port must be between 1 and 65535")
	}
	if record.KnownHosts != "" {
		if _, err := resolveLocalHomePath(record.KnownHosts, home, true); err != nil {
			return fmt.Errorf("known_hosts path is invalid: %w", err)
		}
	}
	return nil
}

func resolveLocalHomePath(path, home string, requireFile bool) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || hasControl(path) {
		return "", errors.New("path is empty or contains a control character")
	}
	homePath, err := filepath.Abs(home)
	if err != nil {
		return "", errors.New("resolve local home directory")
	}
	resolved := path
	if path == "~" {
		resolved = homePath
	} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		resolved = filepath.Join(homePath, filepath.FromSlash(strings.TrimLeft(path[2:], `/\\`)))
	} else if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute or home-relative")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("resolve local path")
	}
	relative, err := filepath.Rel(homePath, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("path must remain inside the local home directory")
	}
	if requireFile {
		info, statErr := os.Lstat(resolved)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", errors.New("known_hosts must be a regular non-symlink file")
			}
			resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(resolved))
			if parentErr != nil {
				return "", errors.New("known_hosts parent directory is invalid")
			}
			parentRelative, relErr := filepath.Rel(homePath, resolvedParent)
			if relErr != nil || parentRelative == ".." || strings.HasPrefix(parentRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(parentRelative) {
				return "", errors.New("known_hosts must remain inside the local home directory")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", errors.New("inspect known_hosts path")
		}
	}
	return resolved, nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func ensureRemoteTargetDirectory(home string) error {
	homePath, err := filepath.Abs(home)
	if err != nil {
		return errors.New("resolve local home directory")
	}
	sshDirectory := filepath.Join(homePath, ".ssh")
	info, err := os.Lstat(sshDirectory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(sshDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create local .ssh directory: %w", err)
		}
		info, err = os.Lstat(sshDirectory)
	}
	if err != nil {
		return fmt.Errorf("inspect local .ssh directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("local .ssh path must be a real directory")
	}
	if err := os.Chmod(sshDirectory, 0700); err != nil {
		return fmt.Errorf("restrict local .ssh directory: %w", err)
	}
	return inspectRemoteTargetDirectory(home)
}

func inspectRemoteTargetDirectory(home string) error {
	homePath, err := filepath.Abs(home)
	if err != nil {
		return errors.New("resolve local home directory")
	}
	sshDirectory := filepath.Join(homePath, ".ssh")
	info, err := os.Lstat(sshDirectory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("local .ssh path must be a real directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("local .ssh directory permissions are too broad")
	}
	return nil
}

func validateRemoteIdentityFile(identityFile string) error {
	info, err := os.Lstat(identityFile)
	if err != nil {
		return fmt.Errorf("inspect local SSH identity: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("local SSH identity must be a regular non-symlink file")
	}
	return nil
}

func remoteTargetRecordFromOptions(options remoteLoginOptions, home string) (remoteTargetRecord, error) {
	record := remoteTargetRecord{Target: options.Target, Port: options.Port}
	if options.KnownHostsFile != "" {
		knownHosts, err := resolveLocalHomePath(options.KnownHostsFile, home, true)
		if err != nil {
			return remoteTargetRecord{}, fmt.Errorf("known_hosts path is invalid: %w", err)
		}
		homePath, err := filepath.Abs(home)
		if err != nil {
			return remoteTargetRecord{}, errors.New("resolve local home directory")
		}
		relative, err := filepath.Rel(homePath, knownHosts)
		if err != nil {
			return remoteTargetRecord{}, errors.New("resolve known_hosts path")
		}
		record.KnownHosts = "~/" + filepath.ToSlash(relative)
		if relative == "." {
			record.KnownHosts = "~"
		}
	}
	if err := validateRemoteTargetRecord(record, home); err != nil {
		return remoteTargetRecord{}, err
	}
	return record, nil
}

func persistRemoteTargetAfterVerification(options remoteLoginOptions) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find local home directory: %w", err)
	}
	_, eligible, err := remoteTargetRecordPath(home, options.IdentityFile)
	if err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	record, err := remoteTargetRecordFromOptions(options, home)
	if err != nil {
		return err
	}
	return saveRemoteTargetRecord(home, options.IdentityFile, record)
}

func findSavedRemoteTarget(home string) (savedRemoteTarget, bool, error) {
	homePath, err := filepath.Abs(home)
	if err != nil {
		return savedRemoteTarget{}, false, errors.New("resolve local home directory")
	}
	sshDirectory := filepath.Join(homePath, ".ssh")
	directoryInfo, err := os.Lstat(sshDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return savedRemoteTarget{}, false, nil
	}
	if err != nil {
		return savedRemoteTarget{}, false, errors.New("inspect local .ssh directory")
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return savedRemoteTarget{}, false, errors.New("local .ssh path must be a real directory")
	}
	entries, err := os.ReadDir(sshDirectory)
	if err != nil {
		return savedRemoteTarget{}, false, fmt.Errorf("read local .ssh directory: %w", err)
	}
	var found savedRemoteTarget
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), remoteTargetRecordSuffix) {
			continue
		}
		recordPath := filepath.Join(sshDirectory, entry.Name())
		identityPath := strings.TrimSuffix(recordPath, remoteTargetRecordSuffix)
		identityInfo, identityErr := os.Lstat(identityPath)
		if errors.Is(identityErr, os.ErrNotExist) {
			continue
		}
		if identityErr != nil {
			return savedRemoteTarget{}, false, errors.New("inspect saved SSH identity")
		}
		if identityInfo.Mode()&os.ModeSymlink != 0 || !identityInfo.Mode().IsRegular() {
			return savedRemoteTarget{}, false, errors.New("saved SSH identity must be a regular non-symlink file")
		}
		record, err := readRemoteTargetRecord(recordPath, home)
		if err != nil {
			return savedRemoteTarget{}, false, err
		}
		count++
		if count > 1 {
			return savedRemoteTarget{}, false, errors.New("multiple saved GoAster server targets are ambiguous")
		}
		found = savedRemoteTarget{Record: record, IdentityFile: identityPath}
	}
	return found, count == 1, nil
}

func checkSavedRemoteTarget(ctx context.Context, home string, sshRun sshRunner) (savedRemoteTarget, bool, error) {
	if ctx == nil {
		return savedRemoteTarget{}, false, errors.New("saved target check context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return savedRemoteTarget{}, false, err
	}
	if sshRun == nil {
		return savedRemoteTarget{}, false, errors.New("saved target SSH runner is unavailable")
	}
	target, found, err := findSavedRemoteTarget(home)
	if err != nil || !found {
		return target, found, err
	}
	knownHosts := ""
	if target.Record.KnownHosts != "" {
		knownHosts, err = resolveLocalHomePath(target.Record.KnownHosts, home, true)
		if err != nil {
			return savedRemoteTarget{}, false, err
		}
	}
	options := remoteLoginOptions{
		Target:         target.Record.Target,
		IdentityFile:   target.IdentityFile,
		KnownHostsFile: knownHosts,
		Port:           target.Record.Port,
	}
	if err := validateRemoteLoginOptions(options); err != nil {
		return savedRemoteTarget{}, false, err
	}
	checkContext, cancel := context.WithTimeout(ctx, remoteTargetCheckTimeout)
	checkErr := sshRun(checkContext, readOnlyRemoteHealthArguments(options), strings.NewReader(""), io.Discard, io.Discard)
	contextErr := checkContext.Err()
	cancel()
	if checkErr != nil || contextErr != nil {
		return target, true, sshFailure("saved remote health check", contextErr, checkErr)
	}
	return target, true, nil
}

func readOnlyRemoteHealthArguments(options remoteLoginOptions) []string {
	arguments := []string{
		"-T",
		"-i", options.IdentityFile,
		"-p", strconv.Itoa(options.Port),
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PreferredAuthentications=publickey",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ConnectTimeout=" + strconv.Itoa(remoteConnectTimeout),
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if options.KnownHostsFile != "" {
		arguments = append(arguments,
			"-o", "GlobalKnownHostsFile=none",
			"-o", "UserKnownHostsFile="+options.KnownHostsFile,
		)
	}
	return append(arguments, "--", options.Target, remoteHealthCommand)
}

func reportSavedRemoteTarget(ctx context.Context, home string, sshRun sshRunner, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	target, found, err := checkSavedRemoteTarget(ctx, home, sshRun)
	if err != nil {
		if _, writeErr := fmt.Fprintln(output, "A saved GoAster server could not be safely confirmed. No server was changed or replaced."); writeErr != nil {
			return writeErr
		}
		return nil
	}
	if !found {
		return nil
	}
	_, err = fmt.Fprintf(output, "GoAster found the saved server %q and its read-only strict host-key check succeeded.\n", target.Record.Target)
	return err
}
