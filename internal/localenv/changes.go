package localenv

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// ChangeSet contains only variable names; values are deliberately absent.
type ChangeSet struct {
	Added    []string
	Modified []string
}

// Changes compares incoming settings with existing settings without exposing
// either set of values.
func Changes(existing, incoming map[string]string) ChangeSet {
	added := make([]string, 0)
	modified := make([]string, 0)
	for name, value := range incoming {
		old, present := existing[name]
		if !present {
			added = append(added, name)
		} else if old != value {
			modified = append(modified, name)
		}
	}
	sort.Strings(added)
	sort.Strings(modified)
	return ChangeSet{Added: added, Modified: modified}
}

// Merge returns an upserted copy and leaves unrelated existing settings in
// place.
func Merge(existing, incoming map[string]string) map[string]string {
	merged := make(map[string]string, len(existing)+len(incoming))
	for name, value := range existing {
		merged[name] = value
	}
	for name, value := range incoming {
		merged[name] = value
	}
	return merged
}

// Digest returns a stable proposal identifier without returning any setting
// values to callers. The canonical encoding is the same sorted form used by
// the private settings file.
func Digest(values map[string]string) string {
	digest := sha256.Sum256(encode(values))
	return hex.EncodeToString(digest[:])
}
