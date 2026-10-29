package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestStorePersistsPrivateJobsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "jobs.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, time.January, 2, 10, 0, 0, 0, time.UTC)
	want := []Job{{
		ID:           "job-1",
		Kind:         ScheduleEvery,
		EverySeconds: 60,
		Message:      "remember this",
		Enabled:      true,
		NextRun:      &next,
		Status:       StatusScheduled,
		RunCount:     2,
		MaxRuns:      4,
	}}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("jobs file permissions = %04o, want 0600", got)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("jobs directory permissions = %04o, want 0700", got)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".jobs-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary jobs files left behind: %v", temporary)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != want[0].ID || loaded[0].Message != want[0].Message || loaded[0].NextRun == nil || !loaded[0].NextRun.Equal(next) {
		t.Fatalf("loaded jobs = %#v, want persisted job", loaded)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk diskState
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Version != storeVersion || len(disk.Jobs) != 1 {
		t.Fatalf("disk state = %#v, want version %d with one job", disk, storeVersion)
	}
}

func TestNewStoreUsesGoAsterCronPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".goaster", "cron", "jobs.json")
	if store.Path != want {
		t.Fatalf("default path = %q, want %q", store.Path, want)
	}
}

func TestNextRunComputesAtEveryAndCronSchedules(t *testing.T) {
	after := time.Date(2026, time.January, 6, 10, 7, 30, 0, time.UTC)
	at, err := NextRun(Schedule{Kind: ScheduleAt, AtSeconds: time.Date(2026, time.January, 6, 10, 8, 0, 0, time.UTC).Unix()}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(time.Date(2026, time.January, 6, 10, 8, 0, 0, time.UTC)) {
		t.Fatalf("at next run = %v", at)
	}
	past, err := NextRun(Schedule{Kind: ScheduleAt, AtSeconds: after.Add(-time.Minute).Unix()}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !past.IsZero() {
		t.Fatalf("past at next run = %v, want zero", past)
	}

	every, err := NextRun(Schedule{Kind: ScheduleEvery, EverySeconds: 15}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !every.Equal(after.Add(15 * time.Second)) {
		t.Fatalf("every next run = %v", every)
	}

	cron, err := NextRun(Schedule{Kind: ScheduleCron, CronExpr: "*/5 * * * *"}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !cron.Equal(time.Date(2026, time.January, 6, 10, 10, 0, 0, time.UTC)) {
		t.Fatalf("cron next run = %v", cron)
	}
	seconds, err := NextRun(Schedule{Kind: ScheduleCron, CronExpr: "*/10 * * * * *"}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !seconds.Equal(time.Date(2026, time.January, 6, 10, 7, 40, 0, time.UTC)) {
		t.Fatalf("seconds cron next run = %v", seconds)
	}
	weekday, err := NextRun(Schedule{Kind: ScheduleCron, CronExpr: "0 9 * * 1"}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !weekday.Equal(time.Date(2026, time.January, 12, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekday cron next run = %v", weekday)
	}
	sunday, err := NextRun(Schedule{Kind: ScheduleCron, CronExpr: "0 0 * * 7"}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !sunday.Equal(time.Date(2026, time.January, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("numeric Sunday cron next run = %v", sunday)
	}
}

func TestSchedulerManagementAndPartialUpdate(t *testing.T) {
	scheduler, err := New(Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time {
		return time.Unix(1000, 0)
	}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := scheduler.Add(Job{ID: "managed", Kind: ScheduleEvery, EverySeconds: 60, Message: "first", MaxRuns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !job.Enabled || job.Status != StatusScheduled || job.NextRun == nil {
		t.Fatalf("added job = %+v, want enabled scheduled job", job)
	}
	updatedMessage := "second"
	updated, err := scheduler.Update(job.ID, Patch{Message: &updatedMessage})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Message != updatedMessage || updated.Kind != ScheduleEvery || updated.EverySeconds != 60 || updated.MaxRuns != 3 || updated.NextRun == nil {
		t.Fatalf("partial update changed omitted fields: %+v", updated)
	}
	if _, err := scheduler.SetEnabled(job.ID, false); err != nil {
		t.Fatal(err)
	}
	disabled, ok := scheduler.Get(job.ID)
	if !ok || disabled.Enabled || disabled.Status != StatusDisabled {
		t.Fatalf("disabled job = %+v, found=%v", disabled, ok)
	}
	if _, err := scheduler.SetEnabled(job.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Remove(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := scheduler.Get(job.ID); ok {
		t.Fatal("removed job is still present")
	}
	if err := scheduler.Remove(job.ID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("second remove error = %v, want ErrJobNotFound", err)
	}
}

func TestSchedulerDeletesOneTimeJobAfterExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	var gotMessage atomic.Value
	scheduler, err := New(Options{
		Path: path,
		Now:  func() time.Time { return time.Unix(1000, 0) },
		Executor: func(_ context.Context, message string) error {
			gotMessage.Store(message)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := scheduler.Add(Job{Kind: ScheduleAt, AtSeconds: 999, Message: "one time"})
	if err != nil {
		t.Fatal(err)
	}
	if runs := scheduler.RunDue(context.Background()); runs != 1 {
		t.Fatalf("due runs = %d, want 1", runs)
	}
	if got := gotMessage.Load(); got != "one time" {
		t.Fatalf("executed message = %v", got)
	}
	if _, ok := scheduler.Get(job.ID); ok {
		t.Fatal("one-time job still exists after execution")
	}
	loaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := loaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("persisted jobs after one-time execution = %#v", jobs)
	}
}

func TestSchedulerHonorsMaxRunsAndPersistsFailure(t *testing.T) {
	clock := time.Unix(1000, 0)
	var calls atomic.Int32
	scheduler, err := New(Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return clock },
		Executor: func(context.Context, string) error {
			calls.Add(1)
			return errors.New("provider unavailable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := scheduler.Add(Job{Kind: ScheduleEvery, EverySeconds: 10, Message: "repeat", MaxRuns: 2})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(10 * time.Second)
	if scheduler.RunDue(context.Background()) != 1 {
		t.Fatal("first due run did not execute")
	}
	clock = clock.Add(10 * time.Second)
	if scheduler.RunDue(context.Background()) != 1 {
		t.Fatal("second due run did not execute")
	}
	if calls.Load() != 2 {
		t.Fatalf("executor calls = %d, want 2", calls.Load())
	}
	updated, ok := scheduler.Get(job.ID)
	if !ok {
		t.Fatal("max-runs job was removed instead of retained")
	}
	if updated.RunCount != 2 || updated.Enabled || updated.NextRun != nil || updated.Status != StatusFailed || updated.Error != "provider unavailable" {
		t.Fatalf("max-runs job after failures = %+v", updated)
	}
	if scheduler.RunDue(context.Background()) != 0 {
		t.Fatal("max-runs job executed after reaching its limit")
	}
}

func TestSchedulerLoopWakesAndExecutesDueJob(t *testing.T) {
	message := make(chan string, 1)
	scheduler, err := New(Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Executor: func(_ context.Context, value string) error {
			message <- value
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Add(Job{Kind: ScheduleAt, AtSeconds: time.Now().Add(-time.Second).Unix(), Message: "wake up"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-message:
		if got != "wake up" {
			t.Fatalf("loop message = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler loop did not execute due job")
	}
	scheduler.Stop()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	scheduler.Stop()
}
