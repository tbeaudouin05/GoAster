package skills

import (
	"embed"
	"strings"
)

// Bootstrap is the small, always-available skill discovery and safety policy.
//
//go:embed bootstrap.md
var Bootstrap string

// Starters are the files shipped with this build and installed on first use.
//
//go:embed */META.md */AUTH.md */SKILL.md
var Starters embed.FS

// MandatoryGuidance keeps the shipped safety and workflow rules in every
// tool-enabled request, including when the model does not select a skill.
var MandatoryGuidance = func() string {
	var guidance strings.Builder
	entries, err := Starters.ReadDir(".")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		guidance.WriteString("\n\nShipped " + name + " rules (apply when relevant):\n")
		for _, file := range []string{"AUTH.md", "SKILL.md"} {
			data, err := Starters.ReadFile(name + "/" + file)
			if err != nil {
				panic(err) // Every named file is a build-time embed requirement.
			}
			guidance.Write(data)
			guidance.WriteString("\n")
		}
	}
	return guidance.String()
}()
