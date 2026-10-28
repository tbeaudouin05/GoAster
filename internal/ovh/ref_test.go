package ovh

import (
	"regexp"
	"testing"
)

func TestResolveGoasterCommitReturnsExactRevision(t *testing.T) {
	ref, err := ResolveGoasterCommit()
	if err != nil {
		t.Skip(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(ref) {
		t.Fatalf("invalid exact revision %q", ref)
	}
}
