package ovh

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
)

// ResolveGoasterCommit returns the exact commit represented by the running
// GoAster build or, when available, by the local checkout. It never asks the
// user for a ref.
func ResolveGoasterCommit() (string, error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && commitID.MatchString(setting.Value) {
				return setting.Value, nil
			}
		}
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine GoAster checkout")
	}
	command := exec.Command("git", "rev-parse", "--verify", "HEAD^{commit}")
	command.Dir = workingDirectory
	output, err := command.Output()
	if err == nil {
		ref := strings.TrimSpace(string(output))
		if commitID.MatchString(ref) {
			return ref, nil
		}
	}
	return "", fmt.Errorf("could not determine the exact GoAster commit from build metadata or the local checkout")
}
