package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"goaster/internal/artefacts"
)

const maxSessionSkills = 10

const (
	skillActionNotice   = "Some actions may already have completed. Check their results before continuing or repeating any action."
	skillResetMessage   = skillActionNotice + " Skill guidance could not be loaded or restored. Check the installed skill files; use /clear if the selection cannot be restored."
	skillChangedMessage = skillActionNotice + " Skill guidance changed. Restore the original files or use /clear to discard the selection."
	skillBudgetMessage  = skillActionNotice + " Skill guidance does not fit in the conversation. Review installed skills in ~/.goaster/artefacts/skills and retain their safety and authorization requirements; use /clear if selected skills or conversation history still do not fit."
	skillSaveMessage    = skillActionNotice + " Skill selection could not be saved. Restart to reload the conversation, then use /clear if needed."
)

type skillRecoveryError struct {
	err     error
	message string
}

func (err skillRecoveryError) Error() string { return err.err.Error() }
func (err skillRecoveryError) Unwrap() error { return err.err }

func skillRecovery(err error, message string) error {
	for _, current := range requestFailureErrors(err) {
		if existing, ok := current.(skillRecoveryError); ok {
			message = existing.message
			break
		}
	}
	return skillRecoveryError{err: err, message: message}
}

// SkillRecoveryMessage returns a fixed, safe explanation for a skill failure.
// It never includes filesystem errors, skill contents, or tool arguments.
func SkillRecoveryMessage(err error) string {
	for _, current := range requestFailureErrors(err) {
		if recovery, ok := current.(skillRecoveryError); ok {
			return recovery.message
		}
	}
	return ""
}

// IsSkillRecoveryMessage validates messages received over the local agent
// protocol so arbitrary remote error text is never shown as recovery guidance.
func IsSkillRecoveryMessage(message string) bool {
	switch message {
	case skillResetMessage, skillChangedMessage, skillBudgetMessage, skillSaveMessage:
		return true
	default:
		return false
	}
}

// A selection is context, never user authorization. An empty digest means
// loading was interrupted or failed, so a later turn must stop safely.
type selectedSkill struct {
	Name   string `json:"name"`
	Digest string `json:"digest,omitempty"`
}

type skillSessionKey struct{}

type skillSessionContext struct {
	selected func() []selectedSkill
	remember func(selectedSkill) error
}

func skillDigest(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func validSelectedSkills(selected []selectedSkill) bool {
	// One extra pending entry records that the active-skill limit was hit.
	if len(selected) > maxSessionSkills+1 {
		return false
	}
	seen := make(map[string]bool, len(selected))
	for i, skill := range selected {
		// An unknown selection is a fail-closed marker for malformed or
		// excessive load requests. It can only appear at the end.
		if skill.Name == "" && skill.Digest == "" && i == len(selected)-1 {
			continue
		}
		if !artefacts.ValidName(skill.Name) || seen[skill.Name] {
			return false
		}
		seen[skill.Name] = true
		if skill.Digest != "" {
			if len(skill.Digest) != sha256.Size*2 {
				return false
			}
			for _, c := range skill.Digest {
				if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
					return false
				}
			}
		}
	}
	return true
}

var loadSkillCodexTool = codexTool{
	Type:        "function",
	Name:        "load_skill",
	Description: "Load an available skill's workflow and authorization requirements before using it.",
	Parameters: map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"name": map[string]any{"type": "string"}},
		"required":             []string{"name"},
		"additionalProperties": false,
	},
}

func parseLoadSkillArguments(arguments string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return "", fmt.Errorf("invalid arguments")
	}
	key, err := decoder.Token()
	if err != nil || key != "name" {
		return "", fmt.Errorf("expected a skill name")
	}
	value, err := decoder.Token()
	if err != nil {
		return "", fmt.Errorf("invalid skill name")
	}
	name, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid skill name")
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return "", fmt.Errorf("invalid arguments")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", fmt.Errorf("invalid arguments")
	}
	return name, nil
}

func executeLoadSkill(_ context.Context, arguments string) (string, error) {
	name, err := parseLoadSkillArguments(arguments)
	if err != nil {
		return "", fmt.Errorf("skill load failed: %w; stopped the workflow", err)
	}
	content, err := artefacts.Load(name)
	if err != nil {
		return "", skillRecovery(fmt.Errorf("skill load failed for %q: %w; stopped the workflow", name, err), skillResetMessage)
	}
	return content, nil
}
