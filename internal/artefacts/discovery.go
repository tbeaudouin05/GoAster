package artefacts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxGuidanceBytes bounds the complete installed guidance included in every
// tool-enabled request, including its headings. Guidance is never truncated.
const MaxGuidanceBytes = 640 << 10

var ErrGuidanceTooLarge = errors.New("combined installed skill guidance exceeds the size limit")

// Discover reads only metadata. The workflow and authorization are loaded on
// demand, so changes to user-owned skills take effect without a rebuild.
func Discover() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return discoverAt(root)
}

// Authorizations returns every installed workflow and its authorization so
// their requirements reach the model even when it requests a tool directly.
func Authorizations() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	metadata, err := discoverAt(root)
	if err != nil || metadata == "" {
		return "", err
	}
	dir := filepath.Join(root, "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read skills directory: %w", err)
	}
	var rules strings.Builder
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		content, err := loadAt(root, entry.Name())
		if err != nil {
			return "", fmt.Errorf("invalid guidance for skill %s: %w", entry.Name(), err)
		}
		rule := "Runtime " + entry.Name() + " guidance (apply when relevant):\n" + content
		separator := ""
		if rules.Len() > 0 {
			separator = "\n\n"
		}
		if len(rule)+len(separator) > MaxGuidanceBytes-rules.Len() {
			return "", fmt.Errorf("%w (%d bytes)", ErrGuidanceTooLarge, MaxGuidanceBytes)
		}
		rules.WriteString(separator)
		rules.WriteString(rule)
	}
	return rules.String(), nil
}

func discoverAt(root string) (string, error) {
	dir := filepath.Join(root, "skills")
	rootInfo, rootErr := os.Lstat(root)
	if errors.Is(rootErr, os.ErrNotExist) {
		return "", nil
	}
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("artefacts directory is unavailable or not a real directory: %s", root)
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("skills directory is unavailable or not a real directory: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read skills directory: %w", err)
	}
	var descriptions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("skill entry must not be a symbolic link: %s", entry.Name())
			}
			continue
		}
		name := entry.Name()
		if !validName(name) {
			return "", fmt.Errorf("invalid skill directory name: %s", name)
		}
		if len(descriptions) >= maxSkills {
			return "", fmt.Errorf("too many runtime skills")
		}
		metadata, err := readSkillFile(filepath.Join(dir, name, "META.md"), metadataLimit)
		if err != nil {
			return "", fmt.Errorf("read %s metadata: %w", name, err)
		}
		if err := validateMetadata(name, metadata); err != nil {
			return "", err
		}
		// Check the other files exist and are safe without reading their text.
		for _, filename := range []string{"SKILL.md", "AUTH.md"} {
			limit := int64(skillLimit)
			if filename == "AUTH.md" {
				limit = metadataLimit
			}
			if err := checkSkillFile(filepath.Join(dir, name, filename), limit); err != nil {
				return "", fmt.Errorf("inspect %s %s: %w", name, filename, err)
			}
		}
		descriptions = append(descriptions, strings.TrimSpace(string(metadata)))
	}
	return strings.Join(descriptions, "\n\n"), nil
}

// Load returns both the workflow and its authorization requirements.
func Load(name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return loadAt(root, name)
}

func loadAt(root, name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("invalid skill name")
	}
	base := filepath.Join(root, "skills")
	for _, dir := range []string{root, base, filepath.Join(base, name)} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("skill directory is unavailable or not a real directory: %s", dir)
		}
	}
	metadata, err := readSkillFile(filepath.Join(base, name, "META.md"), metadataLimit)
	if err != nil {
		return "", err
	}
	if err := validateMetadata(name, metadata); err != nil {
		return "", err
	}
	auth, err := readSkillFile(filepath.Join(base, name, "AUTH.md"), metadataLimit)
	if err != nil {
		return "", err
	}
	skill, err := readSkillFile(filepath.Join(base, name, "SKILL.md"), skillLimit)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(auth)) == "" || strings.TrimSpace(string(skill)) == "" {
		return "", fmt.Errorf("skill %s has empty authorization or workflow", name)
	}
	if !utf8.Valid(auth) || !utf8.Valid(skill) {
		return "", fmt.Errorf("skill %s authorization and workflow must be valid UTF-8", name)
	}
	return "Authorization requirements (follow these before acting):\n" + string(auth) + "\n\nSkill workflow:\n" + string(skill), nil
}

func checkSkillFile(path string, limit int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || info.Size() == 0 {
		return fmt.Errorf("%s must be a nonempty regular file within the size limit", path)
	}
	return nil
}

func readSkillFile(path string, limit int64) ([]byte, error) {
	if err := checkSkillFile(path, limit); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds size limit", path)
	}
	return data, nil
}
