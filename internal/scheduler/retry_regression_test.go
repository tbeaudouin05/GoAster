package scheduler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type schedulerTestReporter interface {
	TempDir() string
	Fatal(...any)
	Fatalf(string, ...any)
	Skipf(string, ...any)
	Setenv(string, string)
	Cleanup(func())
}

func checkSchedulerCrossProcessRetry(t schedulerTestReporter) {
	role := os.Getenv("GOASTER_SCHEDULER_TEST_ROLE")
	dir := os.Getenv("GOASTER_SCHEDULER_TEST_DIR")
	wait := func(name string) bool {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	mark := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if role != "" {
		s, err := New(Options{
			Path: filepath.Join(dir, "jobs.json"),
			Now:  func() time.Time { return time.Unix(1001, 0) },
			Executor: func(context.Context, string) error {
				mark("entered-" + role)
				if role == "a" {
					if !wait("finish-a") {
						t.Fatal("timed out waiting to finish the first claim")
					}
					return ErrRetry
				}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		mark("ready-" + role)
		if !wait("begin-" + role) {
			t.Fatal("timed out waiting to start")
		}
		runs := s.RunDue(context.Background())
		if (role == "a" && runs != 1) || (role == "b" && runs != 0) {
			t.Fatalf("first RunDue for %s = %d", role, runs)
		}
		mark("done-" + role)
		if role == "b" {
			if !wait("retry-b") {
				t.Fatal("timed out waiting for second claim")
			}
			if runs := s.RunDue(context.Background()); runs != 0 {
				t.Fatalf("stale scheduler retried early: %d", runs)
			}
		}
		return
	}

	dir = t.TempDir()
	clock := time.Unix(1000, 0)
	var executed atomic.Int32
	s, err := New(Options{Path: filepath.Join(dir, "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
		executed.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Add(Job{Kind: ScheduleAt, AtSeconds: 1, Message: "once"})
	if err != nil {
		t.Fatal(err)
	}
	commandA := exec.Command(os.Args[0], "-test.run=^TestSchedulerDeletesOneTimeJobAfterExecution$")
	commandB := exec.Command(os.Args[0], "-test.run=^TestSchedulerDeletesOneTimeJobAfterExecution$")
	commandA.Env = append(os.Environ(), "GOASTER_SCHEDULER_TEST_DIR="+dir, "GOASTER_SCHEDULER_TEST_ROLE=a")
	commandB.Env = append(os.Environ(), "GOASTER_SCHEDULER_TEST_DIR="+dir, "GOASTER_SCHEDULER_TEST_ROLE=b")
	commandA.Stdout, commandA.Stderr = os.Stdout, os.Stderr
	commandB.Stdout, commandB.Stderr = os.Stdout, os.Stderr
	if err := commandA.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commandA.Process.Kill() })
	if err := commandB.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commandB.Process.Kill() })
	if !wait("ready-a") || !wait("ready-b") {
		t.Fatal("both schedulers did not load the original job")
	}
	mark("begin-a")
	if !wait("entered-a") {
		t.Fatal("first claim did not start")
	}
	live, err := New(Options{Path: filepath.Join(dir, "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	if active, ok := live.Get(job.ID); !ok || !active.Enabled || active.Status != StatusRunning {
		t.Fatalf("a live claim was mistaken for an interrupted run: %+v", active)
	}
	mark("begin-b")
	if !wait("done-b") {
		t.Fatal("second scheduler did not finish its concurrent attempt")
	}
	mark("finish-a")
	if !wait("done-a") {
		t.Fatal("first scheduler did not save its retry")
	}
	mark("retry-b")
	if err := commandA.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := commandB.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "entered-b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale process executed the retry")
	}
	pending, ok := s.Get(job.ID)
	if !ok || pending.NextRun == nil || !pending.NextRun.Equal(time.Unix(1006, 0)) || pending.RunCount != 0 {
		t.Fatalf("persisted retry = %+v", pending)
	}
	clock = *pending.NextRun
	if runs := s.RunDue(context.Background()); runs != 1 || executed.Load() != 1 {
		t.Fatalf("due retry: runs=%d executions=%d", runs, executed.Load())
	}
	if _, ok := s.Get(job.ID); ok {
		t.Fatal("one-shot remained after the successful retry")
	}
}

func checkInterruptedClaimNeedsExplicitReenable(t schedulerTestReporter) {
	clock := time.Unix(1000, 0)
	executions := 0
	options := Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
		executions++
		return nil
	}}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Add(Job{Kind: ScheduleAt, AtSeconds: 1, Message: "uncertain"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if _, ok := s.claimDue(); !ok {
		t.Fatal("could not persist a running claim")
	}
	restarted, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	failed, ok := restarted.Get(job.ID)
	if !ok || failed.Enabled || failed.NextRun != nil || failed.Status != StatusFailed {
		t.Fatalf("interrupted claim was not disabled: %+v", failed)
	}
	if restarted.RunDue(context.Background()) != 0 || executions != 0 {
		t.Fatal("interrupted claim was repeated automatically")
	}
	if _, err := restarted.SetEnabled(job.ID, true); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if restarted.RunDue(context.Background()) != 1 || executions != 1 {
		t.Fatal("explicitly re-enabled job did not run")
	}
}

func checkStaleSchedulersPreserveConcurrentChanges(t schedulerTestReporter) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	first, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Add(Job{ID: "first", Kind: ScheduleAt, AtSeconds: 60, Message: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Add(Job{ID: "second", Kind: ScheduleAt, AtSeconds: 60, Message: "second"}); err != nil {
		t.Fatal(err)
	}
	if got := first.List(); len(got) != 2 {
		t.Fatalf("stale addition lost a job: %+v", got)
	}
	if err := first.Remove("first"); err != nil {
		t.Fatal(err)
	}
	if got := second.List(); len(got) != 1 || got[0].ID != "second" {
		t.Fatalf("stale view lost a removal: %+v", got)
	}
}

func checkRetrySaveFailureDoesNotSpinOrRepeat(t schedulerTestReporter) {
	clock := time.Unix(1000, 0)
	executions := 0
	s, err := New(Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
		executions++
		return ErrRetry
	}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Add(Job{Kind: ScheduleAt, AtSeconds: 1, Message: "deferred"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	s.saveJobs = func(jobs []Job) error {
		if len(jobs) == 1 && jobs[0].Status == StatusScheduled {
			return errors.New("retry could not be saved")
		}
		return s.store.Save(jobs)
	}
	if runs := s.RunDue(context.Background()); runs != 1 {
		t.Fatalf("claimed = %d", runs)
	}
	if wait, ok := s.nextWait(); ok {
		t.Fatalf("running job would wake immediately after save failure: %v", wait)
	}
	if runs := s.RunDue(context.Background()); runs != 0 || executions != 1 {
		t.Fatalf("failed retry was repeated: runs=%d executions=%d", runs, executions)
	}
	failed, ok := s.Get(job.ID)
	if !ok || failed.Enabled || failed.Status != StatusFailed || !strings.Contains(failed.Error, "interrupted") {
		t.Fatalf("uncertain claim was not held for review: %+v", failed)
	}
}

func checkCronLoadRejectsSymlinkedDefaultParents(t schedulerTestReporter) {
	for _, component := range []int{1, 2, 3, 4} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		parts := []string{home, ".goaster", "artefacts", "state", "cron"}
		link := filepath.Join(parts[:component+1]...)
		if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		store, err := NewStore("")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(store.Path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.Path, []byte(`{"version":1,"jobs":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "cron directory") {
			t.Fatalf("symlinked parent accepted: %v", err)
		}
	}
}

func checkCronRejectsSymlinkedExplicitAncestor(t schedulerTestReporter) {
	dir := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(target, "cron"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "cron", "jobs.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"jobs":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "cron directory") {
		t.Fatalf("load through symlinked ancestor = %v", err)
	}
	if err := store.Save(nil); err == nil || !strings.Contains(err.Error(), "cron directory") {
		t.Fatalf("save through symlinked ancestor = %v", err)
	}
}
