package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"goaster/internal/artefacts"
	"goaster/internal/filesync"
)

const (
	fileToolMaxReadBytes    int64 = 64 << 10
	fileToolMaxContentBytes       = 8 << 20
	fileToolMaxEntries            = 1000
)

var fileToolMutationLock sync.Mutex

var readFileCodexTool = codexTool{
	Type:        "function",
	Name:        "read_file",
	Description: "Read up to 64 KiB from a regular file under GoAster's private artefact workspace. Paths are relative to that workspace (or absolute paths inside it); symbolic links are rejected. Use byte offsets for UTF-8 text pagination; offsets must be character boundaries and binary or invalid text is rejected.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path relative to the GoAster artefact workspace.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"minimum":     int64(0),
				"description": "Byte offset to start reading from (default: 0).",
			},
			"length": map[string]any{
				"type":        "integer",
				"minimum":     int64(1),
				"maximum":     fileToolMaxReadBytes,
				"description": "Maximum bytes to return (default and maximum: 65536).",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	},
}

var writeFileCodexTool = codexTool{
	Type:        "function",
	Name:        "write_file",
	Description: "Create or replace a regular file under GoAster's private artefact workspace. New files use mode 0600; replacements preserve existing permission bits, missing parent directories are created with mode 0700, creation is exclusive by default, and replacement uses a rename. Set overwrite=true to create or replace the entire file; symbolic links are rejected.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path relative to the GoAster artefact workspace.",
			},
			"content": map[string]any{
				"type":        "string",
				"maxLength":   fileToolMaxContentBytes,
				"description": "Complete file content; maximum 8 MiB by byte length.",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "Create or replace the entire file when true.",
			},
		},
		"required":             []string{"path", "content"},
		"additionalProperties": false,
	},
}

var editFileCodexTool = codexTool{
	Type:        "function",
	Name:        "edit_file",
	Description: "Replace exactly one occurrence of old_text in a regular file under GoAster's private artefact workspace. The file is left unchanged unless old_text occurs exactly once and the resulting file is at most 8 MiB; symbolic links are rejected and a successful edit uses a replacement. Concurrent file-tool mutations in this process are serialized; outside changes may conflict.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path relative to the GoAster artefact workspace.",
			},
			"old_text": map[string]any{
				"type":        "string",
				"maxLength":   fileToolMaxContentBytes,
				"description": "Exact non-empty text to replace; it must occur once.",
			},
			"new_text": map[string]any{
				"type":        "string",
				"maxLength":   fileToolMaxContentBytes,
				"description": "Replacement text; the resulting file must be at most 8 MiB by byte length.",
			},
		},
		"required":             []string{"path", "old_text", "new_text"},
		"additionalProperties": false,
	},
}

var appendFileCodexTool = codexTool{
	Type:        "function",
	Name:        "append_file",
	Description: "Append content to a regular file under GoAster's private artefact workspace, creating it when absent. New files use exclusive creation and mode 0600; existing-file updates use a replacement and cooperating calls in this process are serialized; the result is limited to 8 MiB, and symbolic links are rejected.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path relative to the GoAster artefact workspace.",
			},
			"content": map[string]any{
				"type":        "string",
				"maxLength":   fileToolMaxContentBytes,
				"description": "Content to append; the resulting file must be at most 8 MiB by byte length.",
			},
		},
		"required":             []string{"path", "content"},
		"additionalProperties": false,
	},
}

var listDirCodexTool = codexTool{
	Type:        "function",
	Name:        "list_dir",
	Description: "List one directory level under GoAster's private artefact workspace. Paths are relative to that workspace; symbolic-link directories are rejected and symbolic-link entries are shown as LINK without following them. At most 1000 entries are returned; an incomplete listing is marked truncated.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Directory path relative to the GoAster artefact workspace (default: .).",
			},
		},
		"additionalProperties": false,
	},
}

var fileToolArgumentNames = map[string]map[string]bool{
	readFileCodexTool.Name:   {"path": true, "offset": true, "length": true},
	writeFileCodexTool.Name:  {"path": true, "content": true, "overwrite": true},
	editFileCodexTool.Name:   {"path": true, "old_text": true, "new_text": true},
	appendFileCodexTool.Name: {"path": true, "content": true},
	listDirCodexTool.Name:    {"path": true},
}

func parseFileToolArguments(toolName, arguments string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	var args map[string]json.RawMessage
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("%s arguments are not valid JSON: %w", toolName, err)
	}
	if args == nil {
		return nil, fmt.Errorf("%s arguments must be a JSON object", toolName)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%s arguments contain more than one JSON value", toolName)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s arguments are not valid JSON: %w", toolName, err)
	}
	for name := range args {
		if !fileToolArgumentNames[toolName][name] {
			return nil, fmt.Errorf("%s arguments contain unknown field %q", toolName, name)
		}
	}
	return args, nil
}

func fileToolStringArgument(args map[string]json.RawMessage, toolName, name string, required bool) (string, error) {
	raw, ok := args[name]
	if !ok {
		if required {
			return "", fmt.Errorf("%s %s is required", toolName, name)
		}
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("%s %s must be a string", toolName, name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s %s must be a string", toolName, name)
	}
	return value, nil
}

func fileToolBoolArgument(args map[string]json.RawMessage, toolName, name string, defaultValue bool) (bool, error) {
	raw, ok := args[name]
	if !ok {
		return defaultValue, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, fmt.Errorf("%s %s must be a boolean", toolName, name)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%s %s must be a boolean", toolName, name)
	}
	return value, nil
}

func fileToolInt64Argument(args map[string]json.RawMessage, toolName, name string, defaultValue int64) (int64, error) {
	raw, ok := args[name]
	if !ok {
		return defaultValue, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("%s %s must be an integer", toolName, name)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s %s must be an integer", toolName, name)
	}
	return value, nil
}

type fileToolCommittedError struct{ err error }

func (e fileToolCommittedError) Error() string {
	return fmt.Sprintf("operation committed, but durability is uncertain: %v; inspect the file before retrying", e.err)
}

func fileToolFailure(ctx context.Context, operation string, err error) (string, error) {
	var committed fileToolCommittedError
	if errors.As(err, &committed) {
		return fmt.Sprintf("%s: %v", operation, err), nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
	}
	return fmt.Sprintf("%s error: %v", operation, err), nil
}

func executeReadFile(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	args, err := parseFileToolArguments(readFileCodexTool.Name, arguments)
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, err)
	}
	path, err := fileToolStringArgument(args, readFileCodexTool.Name, "path", true)
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, err)
	}
	if path == "" {
		return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("path must not be empty"))
	}
	offset, err := fileToolInt64Argument(args, readFileCodexTool.Name, "offset", 0)
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, err)
	}
	if offset < 0 {
		return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("offset must be >= 0"))
	}
	length, err := fileToolInt64Argument(args, readFileCodexTool.Name, "length", fileToolMaxReadBytes)
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, err)
	}
	if length <= 0 {
		return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("length must be > 0"))
	}
	if length > fileToolMaxReadBytes {
		length = fileToolMaxReadBytes
	}

	resolved, err := resolveFileToolPath(path, false, false)
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, err)
	}
	file, err := openFileToolRegular(resolved)
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, fmt.Errorf("inspect file: %w", err))
	}
	seeker, ok := file.(io.Seeker)
	if !ok {
		return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("file does not support byte offsets"))
	}
	if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, fmt.Errorf("seek to byte %d: %w", offset, err))
	}
	data, err := io.ReadAll(io.LimitReader(file, length+utf8.UTFMax))
	if err != nil {
		return fileToolFailure(ctx, readFileCodexTool.Name, fmt.Errorf("read content: %w", err))
	}
	if len(data) > 0 && !utf8.RuneStart(data[0]) {
		return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("offset is not a UTF-8 character boundary"))
	}
	hasMore := int64(len(data)) > length
	if hasMore {
		cut := int(length)
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		if cut == 0 {
			return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("length is too small for the next UTF-8 character"))
		}
		data = data[:cut]
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return fileToolFailure(ctx, readFileCodexTool.Name, errors.New("file contains binary or invalid UTF-8 text"))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	displayName := filepath.Base(resolved)
	if len(data) == 0 {
		return fmt.Sprintf("[file: %s | total: %d bytes | offset: %d]\n[END OF FILE - no content at this offset]", displayName, info.Size(), offset), nil
	}
	readEnd := offset + int64(len(data)) - 1
	result := fmt.Sprintf("[file: %s | total: %d bytes | read: bytes %d-%d]\n\n%s", displayName, info.Size(), offset, readEnd, string(data))
	if hasMore {
		result += fmt.Sprintf("\n\n[TRUNCATED - call read_file again with offset=%d to continue.]", offset+int64(len(data)))
	} else {
		result += "\n\n[END OF FILE - no further content.]"
	}
	return result, nil
}

func executeWriteFile(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	args, err := parseFileToolArguments(writeFileCodexTool.Name, arguments)
	if err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	path, err := fileToolStringArgument(args, writeFileCodexTool.Name, "path", true)
	if err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	if path == "" {
		return fileToolFailure(ctx, writeFileCodexTool.Name, errors.New("path must not be empty"))
	}
	content, err := fileToolStringArgument(args, writeFileCodexTool.Name, "content", true)
	if err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	if int64(len(content)) > fileToolMaxContentBytes {
		return fileToolFailure(ctx, writeFileCodexTool.Name, fmt.Errorf("content exceeds %d bytes", fileToolMaxContentBytes))
	}
	overwrite, err := fileToolBoolArgument(args, writeFileCodexTool.Name, "overwrite", false)
	if err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	fileToolMutationLock.Lock()
	defer fileToolMutationLock.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resolved, err := resolveFileToolPath(path, true, true)
	if err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	if err := validateFileToolTarget(resolved, true); err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	if overwrite {
		err = atomicReplaceFileTool(ctx, resolved, []byte(content), false, nil)
	} else {
		err = createFileToolExclusive(ctx, resolved, []byte(content))
	}
	if err != nil {
		return fileToolFailure(ctx, writeFileCodexTool.Name, err)
	}
	return fmt.Sprintf("File written: %s", path), nil
}

func executeEditFile(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	args, err := parseFileToolArguments(editFileCodexTool.Name, arguments)
	if err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	path, err := fileToolStringArgument(args, editFileCodexTool.Name, "path", true)
	if err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	if path == "" {
		return fileToolFailure(ctx, editFileCodexTool.Name, errors.New("path must not be empty"))
	}
	oldText, err := fileToolStringArgument(args, editFileCodexTool.Name, "old_text", true)
	if err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	newText, err := fileToolStringArgument(args, editFileCodexTool.Name, "new_text", true)
	if err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	if oldText == "" {
		return fileToolFailure(ctx, editFileCodexTool.Name, errors.New("old_text must not be empty"))
	}
	if int64(len(oldText)) > fileToolMaxContentBytes || int64(len(newText)) > fileToolMaxContentBytes {
		return fileToolFailure(ctx, editFileCodexTool.Name, fmt.Errorf("old_text and new_text must each be at most %d bytes", fileToolMaxContentBytes))
	}
	fileToolMutationLock.Lock()
	defer fileToolMutationLock.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resolved, err := resolveFileToolPath(path, false, false)
	if err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	content, err := readFileToolContent(resolved)
	if err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	first := bytes.Index(content, []byte(oldText))
	if first < 0 {
		return fileToolFailure(ctx, editFileCodexTool.Name, errors.New("old_text was not found; the file was not changed"))
	}
	if bytes.Index(content[first+1:], []byte(oldText)) >= 0 {
		return fileToolFailure(ctx, editFileCodexTool.Name, errors.New("old_text appears 2 times or more; the file was not changed"))
	}
	next := []byte(strings.Replace(string(content), oldText, newText, 1))
	if int64(len(next)) > fileToolMaxContentBytes {
		return fileToolFailure(ctx, editFileCodexTool.Name, fmt.Errorf("result exceeds %d bytes; the file was not changed", fileToolMaxContentBytes))
	}
	if err := atomicReplaceFileTool(ctx, resolved, next, true, content); err != nil {
		return fileToolFailure(ctx, editFileCodexTool.Name, err)
	}
	return fmt.Sprintf("Edited one occurrence in %s", path), nil
}

func executeAppendFile(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	args, err := parseFileToolArguments(appendFileCodexTool.Name, arguments)
	if err != nil {
		return fileToolFailure(ctx, appendFileCodexTool.Name, err)
	}
	path, err := fileToolStringArgument(args, appendFileCodexTool.Name, "path", true)
	if err != nil {
		return fileToolFailure(ctx, appendFileCodexTool.Name, err)
	}
	if path == "" {
		return fileToolFailure(ctx, appendFileCodexTool.Name, errors.New("path must not be empty"))
	}
	content, err := fileToolStringArgument(args, appendFileCodexTool.Name, "content", true)
	if err != nil {
		return fileToolFailure(ctx, appendFileCodexTool.Name, err)
	}
	if int64(len(content)) > fileToolMaxContentBytes {
		return fileToolFailure(ctx, appendFileCodexTool.Name, fmt.Errorf("content exceeds %d bytes", fileToolMaxContentBytes))
	}
	fileToolMutationLock.Lock()
	defer fileToolMutationLock.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resolved, err := resolveFileToolPath(path, true, true)
	if err != nil {
		return fileToolFailure(ctx, appendFileCodexTool.Name, err)
	}
	previous, err := readFileToolContent(resolved)
	if errors.Is(err, fs.ErrNotExist) {
		err = createFileToolExclusive(ctx, resolved, []byte(content))
	} else if err == nil {
		if int64(len(previous))+int64(len(content)) > fileToolMaxContentBytes {
			err = fmt.Errorf("result exceeds %d bytes; the file was not changed", fileToolMaxContentBytes)
		} else {
			next := make([]byte, 0, len(previous)+len(content))
			next = append(next, previous...)
			next = append(next, content...)
			err = atomicReplaceFileTool(ctx, resolved, next, true, previous)
		}
	}
	if err != nil {
		return fileToolFailure(ctx, appendFileCodexTool.Name, err)
	}
	return fmt.Sprintf("Appended to %s", path), nil
}

func executeListDir(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	args, err := parseFileToolArguments(listDirCodexTool.Name, arguments)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	path, err := fileToolStringArgument(args, listDirCodexTool.Name, "path", false)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	if path == "" {
		path = "."
	}
	resolved, err := resolveFileToolPath(path, false, false)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	base, rel, err := fileToolRoot(resolved)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	home := filepath.Dir(filepath.Dir(base))
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	defer homeRoot.Close()
	parentName := filepath.Base(filepath.Dir(base))
	parentInfo, err := homeRoot.Lstat(parentName)
	if errors.Is(err, fs.ErrNotExist) {
		if err = homeRoot.Mkdir(parentName, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			parentInfo, err = homeRoot.Lstat(parentName)
		}
	}
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact parent must be a real directory")
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	parentRoot, err := homeRoot.OpenRoot(parentName)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	defer parentRoot.Close()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentOpened) {
		err = errors.New("artefact parent changed while opening")
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	workspaceRel := filepath.Base(base)
	if err = checkFileToolComponents(parentRoot, workspaceRel, true, true); err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	before, err := parentRoot.Lstat(workspaceRel)
	if errors.Is(err, fs.ErrNotExist) {
		if err = parentRoot.Mkdir(workspaceRel, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			before, err = parentRoot.Lstat(workspaceRel)
		}
	}
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact workspace must be a real directory")
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	root, err := parentRoot.OpenRoot(workspaceRel)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		err = errors.New("artefact workspace changed while opening")
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}

	defer root.Close()
	if err := checkFileToolComponents(root, rel, false, false); err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	before, err = root.Lstat(rel)
	if err != nil || !before.IsDir() {
		return fileToolFailure(ctx, listDirCodexTool.Name, errors.New("path is not a directory"))
	}
	dir, err := root.OpenFile(rel, os.O_RDONLY|fileToolDirectoryOpenFlag, 0)
	if err != nil {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() || !os.SameFile(before, info) {
		return fileToolFailure(ctx, listDirCodexTool.Name, errors.New("path is not a directory or changed while opening"))
	}
	entries, err := dir.ReadDir(fileToolMaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fileToolFailure(ctx, listDirCodexTool.Name, err)
	}
	truncated := len(entries) > fileToolMaxEntries
	if truncated {
		entries = entries[:fileToolMaxEntries]
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "[empty directory]", nil
	}
	var result strings.Builder
	for _, entry := range entries {
		kind := "FILE"
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			kind = "LINK"
		case entry.IsDir():
			kind = "DIR"
		case entry.Type()&os.ModeType != 0:
			kind = "OTHER"
		}
		name := fileToolEntryName(entry.Name())
		if kind == "DIR" {
			result.WriteString("DIR:  ")
		} else {
			result.WriteString(kind + ": ")
		}
		result.WriteString(name + "\n")
	}
	if truncated {
		result.WriteString("[TRUNCATED - directory has more than 1000 entries]\n")
	}
	return strings.TrimSuffix(result.String(), "\n"), nil
}

func fileToolEntryName(name string) string {
	for _, character := range name {
		if character < ' ' || character == 0x7f {
			return strconv.Quote(name)
		}
	}
	return name
}

// Root operations confine traversal even if a directory is replaced between checks.
// JS and Plan 9 do not provide the required rename-resistant Root implementation.
func fileToolRoot(path string) (string, string, error) {
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		return "", "", errors.New("file tools are unavailable on this platform")
	}
	base, err := artefacts.Root()
	if err != nil {
		return "", "", err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", errors.New("path escapes the GoAster artefact workspace")
	}
	return base, rel, nil
}

func checkFileToolComponents(root interface {
	Lstat(string) (fs.FileInfo, error)
	Mkdir(string, fs.FileMode) error
}, rel string, create, missing bool) error {
	parts := strings.Split(rel, string(filepath.Separator))
	current := "."
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) && i < len(parts)-1 && create {
			if e := root.Mkdir(current, 0700); e != nil && !errors.Is(e, fs.ErrExist) {
				return e
			}
			info, err = root.Lstat(current)
		}
		if errors.Is(err, fs.ErrNotExist) && i == len(parts)-1 && missing {
			return nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("file not found: %w", err)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path must not contain a symbolic link")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return errors.New("parent path component is not a directory")
		}
	}
	return nil
}

func resolveFileToolPath(rawPath string, createParents, allowMissingTarget bool) (string, error) {
	if rawPath == "" || strings.IndexByte(rawPath, 0) >= 0 {
		return "", errors.New("invalid path")
	}
	base, err := artefacts.Root()
	if err != nil {
		return "", err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", err
	}
	candidate := rawPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(base, candidate)
	}
	candidate = filepath.Clean(candidate)
	base, rel, err := fileToolRoot(candidate)
	if err != nil {
		return "", err
	}
	home := filepath.Dir(filepath.Dir(base))
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return "", err
	}
	defer homeRoot.Close()
	parentName := filepath.Base(filepath.Dir(base))
	parentInfo, err := homeRoot.Lstat(parentName)
	if errors.Is(err, fs.ErrNotExist) {
		if err = homeRoot.Mkdir(parentName, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			parentInfo, err = homeRoot.Lstat(parentName)
		}
	}
	if err != nil {
		return "", err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact parent must be a real directory")
		return "", err
	}
	parentRoot, err := homeRoot.OpenRoot(parentName)
	if err != nil {
		return "", err
	}
	defer parentRoot.Close()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentOpened) {
		err = errors.New("artefact parent changed while opening")
		return "", err
	}
	workspaceRel := filepath.Base(base)
	if err = checkFileToolComponents(parentRoot, workspaceRel, true, true); err != nil {
		return "", err
	}
	before, err := parentRoot.Lstat(workspaceRel)
	if errors.Is(err, fs.ErrNotExist) {
		if err = parentRoot.Mkdir(workspaceRel, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			before, err = parentRoot.Lstat(workspaceRel)
		}
	}
	if err != nil {
		return "", err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact workspace must be a real directory")
		return "", err
	}
	root, err := parentRoot.OpenRoot(workspaceRel)
	if err != nil {
		return "", err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		err = errors.New("artefact workspace changed while opening")
		return "", err
	}

	defer root.Close()
	if err = checkFileToolComponents(root, rel, createParents, allowMissingTarget); err != nil {
		return "", err
	}
	return candidate, nil
}

func openFileToolRegular(path string) (fs.File, error) {
	base, rel, err := fileToolRoot(path)
	if err != nil {
		return nil, err
	}
	home := filepath.Dir(filepath.Dir(base))
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer homeRoot.Close()
	parentName := filepath.Base(filepath.Dir(base))
	parentInfo, err := homeRoot.Lstat(parentName)
	if errors.Is(err, fs.ErrNotExist) {
		if err = homeRoot.Mkdir(parentName, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			parentInfo, err = homeRoot.Lstat(parentName)
		}
	}
	if err != nil {
		return nil, err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact parent must be a real directory")
		return nil, err
	}
	parentRoot, err := homeRoot.OpenRoot(parentName)
	if err != nil {
		return nil, err
	}
	defer parentRoot.Close()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentOpened) {
		err = errors.New("artefact parent changed while opening")
		return nil, err
	}
	workspaceRel := filepath.Base(base)
	if err = checkFileToolComponents(parentRoot, workspaceRel, true, true); err != nil {
		return nil, err
	}
	before, err := parentRoot.Lstat(workspaceRel)
	if errors.Is(err, fs.ErrNotExist) {
		if err = parentRoot.Mkdir(workspaceRel, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			before, err = parentRoot.Lstat(workspaceRel)
		}
	}
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact workspace must be a real directory")
		return nil, err
	}
	root, err := parentRoot.OpenRoot(workspaceRel)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		err = errors.New("artefact workspace changed while opening")
		return nil, err
	}

	defer root.Close()
	if err = checkFileToolComponents(root, rel, false, false); err != nil {
		return nil, err
	}
	before, err = root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	return openFileToolCheckedRegular(before, func(flags int) (fs.File, error) {
		return root.OpenFile(rel, flags, 0)
	})
}

func openFileToolCheckedRegular(before fs.FileInfo, open func(int) (fs.File, error)) (fs.File, error) {
	file, err := open(os.O_RDONLY | fileToolNonblockOpenFlag)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		file.Close()
		return nil, errors.New("file changed while it was being opened")
	}
	return file, nil
}

func readFileToolContent(path string) ([]byte, error) {
	file, err := openFileToolRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, fileToolMaxContentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > fileToolMaxContentBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", fileToolMaxContentBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("file contains binary or invalid UTF-8 text")
	}
	return data, nil
}

func validateFileToolTarget(path string, allowMissing bool) error {
	base, rel, err := fileToolRoot(path)
	if err != nil {
		return err
	}
	home := filepath.Dir(filepath.Dir(base))
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer homeRoot.Close()
	parentName := filepath.Base(filepath.Dir(base))
	parentInfo, err := homeRoot.Lstat(parentName)
	if errors.Is(err, fs.ErrNotExist) {
		if err = homeRoot.Mkdir(parentName, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			parentInfo, err = homeRoot.Lstat(parentName)
		}
	}
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact parent must be a real directory")
		return err
	}
	parentRoot, err := homeRoot.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentOpened) {
		err = errors.New("artefact parent changed while opening")
		return err
	}
	workspaceRel := filepath.Base(base)
	if err = checkFileToolComponents(parentRoot, workspaceRel, true, true); err != nil {
		return err
	}
	before, err := parentRoot.Lstat(workspaceRel)
	if errors.Is(err, fs.ErrNotExist) {
		if err = parentRoot.Mkdir(workspaceRel, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			before, err = parentRoot.Lstat(workspaceRel)
		}
	}
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact workspace must be a real directory")
		return err
	}
	root, err := parentRoot.OpenRoot(workspaceRel)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		err = errors.New("artefact workspace changed while opening")
		return err
	}

	defer root.Close()
	if err = checkFileToolComponents(root, rel, false, allowMissing); err != nil {
		return err
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	return nil
}

func writeFileToolBytes(file fs.File, data []byte) error {
	writer, ok := file.(io.Writer)
	if !ok {
		return errors.New("file is not writable")
	}
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func createFileToolExclusive(ctx context.Context, path string, data []byte) error {
	base, rel, err := fileToolRoot(path)
	if err != nil {
		return err
	}
	home := filepath.Dir(filepath.Dir(base))
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer homeRoot.Close()
	parentName := filepath.Base(filepath.Dir(base))
	parentInfo, err := homeRoot.Lstat(parentName)
	if errors.Is(err, fs.ErrNotExist) {
		if err = homeRoot.Mkdir(parentName, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			parentInfo, err = homeRoot.Lstat(parentName)
		}
	}
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact parent must be a real directory")
		return err
	}
	parentRoot, err := homeRoot.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentOpened) {
		err = errors.New("artefact parent changed while opening")
		return err
	}
	workspaceRel := filepath.Base(base)
	if err = checkFileToolComponents(parentRoot, workspaceRel, true, true); err != nil {
		return err
	}
	before, err := parentRoot.Lstat(workspaceRel)
	if errors.Is(err, fs.ErrNotExist) {
		if err = parentRoot.Mkdir(workspaceRel, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			before, err = parentRoot.Lstat(workspaceRel)
		}
	}
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact workspace must be a real directory")
		return err
	}
	root, err := parentRoot.OpenRoot(workspaceRel)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		err = errors.New("artefact workspace changed while opening")
		return err
	}

	defer root.Close()
	if err = checkFileToolComponents(root, rel, false, true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, fs.ErrExist) {
		return errors.New("file already exists; set overwrite=true only to replace it")
	}
	if err != nil {
		return err
	}
	if err = writeFileToolBytes(file, data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = root.Remove(rel)
		return err
	}
	if err := syncFileToolParent(func() (filesync.Directory, error) {
		return root.OpenFile(filepath.Dir(rel), os.O_RDONLY|fileToolDirectoryOpenFlag, 0)
	}); err != nil {
		return fileToolCommittedError{err}
	}
	return nil
}

func atomicReplaceFileTool(ctx context.Context, path string, data []byte, requireExisting bool, expected []byte) error {
	base, rel, err := fileToolRoot(path)
	if err != nil {
		return err
	}
	home := filepath.Dir(filepath.Dir(base))
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer homeRoot.Close()
	parentName := filepath.Base(filepath.Dir(base))
	parentInfo, err := homeRoot.Lstat(parentName)
	if errors.Is(err, fs.ErrNotExist) {
		if err = homeRoot.Mkdir(parentName, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			parentInfo, err = homeRoot.Lstat(parentName)
		}
	}
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact parent must be a real directory")
		return err
	}
	parentRoot, err := homeRoot.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	parentOpened, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, parentOpened) {
		err = errors.New("artefact parent changed while opening")
		return err
	}
	workspaceRel := filepath.Base(base)
	if err = checkFileToolComponents(parentRoot, workspaceRel, true, true); err != nil {
		return err
	}
	before, err := parentRoot.Lstat(workspaceRel)
	if errors.Is(err, fs.ErrNotExist) {
		if err = parentRoot.Mkdir(workspaceRel, 0700); err == nil || errors.Is(err, fs.ErrExist) {
			before, err = parentRoot.Lstat(workspaceRel)
		}
	}
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		err = errors.New("artefact workspace must be a real directory")
		return err
	}
	root, err := parentRoot.OpenRoot(workspaceRel)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		err = errors.New("artefact workspace changed while opening")
		return err
	}

	defer root.Close()
	if err = checkFileToolComponents(root, rel, false, !requireExisting); err != nil {
		return err
	}
	info, err := root.Lstat(rel)
	mode := os.FileMode(0600)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("path is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) || requireExisting {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(rel), ".goaster-file-"+hex.EncodeToString(random[:])+".tmp")
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if err = file.Chmod(mode); err == nil {
		err = writeFileToolBytes(file, data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = checkFileToolComponents(root, rel, false, !requireExisting); err != nil {
		return err
	}
	current, currentErr := root.Lstat(rel)
	if info != nil {
		if currentErr != nil || !os.SameFile(info, current) || current.Size() != info.Size() || current.ModTime() != info.ModTime() || current.Mode() != info.Mode() {
			return errors.New("file changed during update; retry")
		}
	} else if !errors.Is(currentErr, fs.ErrNotExist) {
		return errors.New("file changed during update; retry")
	}
	if requireExisting {
		currentData, readErr := readFileToolContent(path)
		if readErr != nil || !bytes.Equal(currentData, expected) {
			return errors.New("file changed during update; retry")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = root.Rename(temp, rel); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	if err := syncFileToolParent(func() (filesync.Directory, error) {
		return root.OpenFile(filepath.Dir(rel), os.O_RDONLY|fileToolDirectoryOpenFlag, 0)
	}); err != nil {
		return fileToolCommittedError{err}
	}
	return nil
}

var syncFileToolParent = func(openDir func() (filesync.Directory, error)) error {
	dir, err := openDir()
	if err != nil {
		return err
	}
	return filesync.DirectoryFile(dir)
}
