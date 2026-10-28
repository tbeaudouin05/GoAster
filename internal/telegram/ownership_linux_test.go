package telegram

import (
	"os"
	"syscall"
	"testing"
)

func TestPrivateFilesAreOwnedByTheServiceUser(t *testing.T) {
	paths := testPaths(t)
	setupTest(t, paths)
	p, err := loadPairing(paths)
	if err != nil || !p.tryPair(42, testCode) {
		t.Fatal("could not create paired user state")
	}
	for _, path := range []string{paths.Token, paths.UserID} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0600 {
			t.Fatal("private file is not owned by the service user with mode 0600")
		}
	}
}
