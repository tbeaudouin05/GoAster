package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backgroundPlatformHelper(mode string, args []string) {}
func exerciseBackgroundPlatformLifecycle(t testing.TB)    { exerciseBackgroundProcessLifecycle(t) }

func TestBackgroundStopTerminatesOrdinaryChild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	process, err := StartBackground(BackgroundRequest{Executable: backgroundTestExecutable(t), Args: backgroundHelperArgs("hold-child", marker)})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(process.Snapshot().Stdout, "session-ready") {
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	process.Stop()
	select {
	case <-process.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not stop")
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived stop: stat error = %v", err)
	}
}
