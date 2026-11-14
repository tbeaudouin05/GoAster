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
	t.Setenv("USERPROFILE", home)
	store, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".goaster", "artefacts", "state", "cron", "jobs.json")
	if store.Path != want {
		t.Fatalf("default path = %q, want %q", store.Path, want)
	}
	checkCronLoadRejectsSymlinkedDefaultParents(t)
	checkCronRejectsSymlinkedExplicitAncestor(t)
}

func TestNextRunComputesAtEveryAndCronSchedules(t *testing.T) {
	after := time.Date(2026, time.January, 6, 10, 7, 30, 0, time.UTC)
	at, err := NextRun(Schedule{Kind: ScheduleAt, AtSeconds: 30}, after)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(after.Add(30 * time.Second)) {
		t.Fatalf("at next run = %v", at)
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

func TestAtScheduleRequiresPositiveBoundedSeconds(t *testing.T) {
	for _, seconds := range []int64{0, -1, maxEverySeconds + 1} {
		if _, err := NextRun(Schedule{Kind: ScheduleAt, AtSeconds: seconds}, time.Unix(1000, 0)); err == nil {
			t.Errorf("NextRun accepted at_seconds=%d", seconds)
		}
	}
}

func TestSchedulerAtAddAndUpdateUseSecondsFromNow(t *testing.T) {
	clock := time.Unix(1000, 0)
	path := filepath.Join(t.TempDir(), "jobs.json")
	scheduler, err := New(Options{Path: path, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int64{0, -1, maxEverySeconds + 1} {
		if _, err := scheduler.Add(Job{Kind: ScheduleAt, AtSeconds: seconds, Message: "invalid"}); err == nil {
			t.Errorf("Add accepted at_seconds=%d", seconds)
		}
	}
	job, err := scheduler.Add(Job{Kind: ScheduleAt, AtSeconds: 30, Message: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if job.AtSeconds != 30 || job.NextRun == nil || !job.NextRun.Equal(clock.Add(30*time.Second)) {
		t.Fatalf("added at job = %+v, want 30 seconds from now", job)
	}

	updatedSeconds := int64(5)
	updated, err := scheduler.Update(job.ID, Patch{AtSeconds: &updatedSeconds})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AtSeconds != updatedSeconds || updated.NextRun == nil || !updated.NextRun.Equal(clock.Add(5*time.Second)) {
		t.Fatalf("updated at job = %+v, want 5 seconds from now", updated)
	}
	for _, seconds := range []int64{0, -1, maxEverySeconds + 1} {
		if _, err := scheduler.Update(job.ID, Patch{AtSeconds: &seconds}); err == nil {
			t.Errorf("Update accepted at_seconds=%d", seconds)
		}
	}
	persisted, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	storedJobs, err := persisted.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(storedJobs) != 1 || storedJobs[0].AtSeconds != updatedSeconds || storedJobs[0].NextRun == nil || !storedJobs[0].NextRun.Equal(clock.Add(5*time.Second)) {
		t.Fatalf("persisted at job = %+v, want relative seconds and absolute next_run", storedJobs)
	}
	checkRetrySaveFailureDoesNotSpinOrRepeat(t)
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
	checkInterruptedClaimNeedsExplicitReenable(t)
	checkStaleSchedulersPreserveConcurrentChanges(t)
	checkSchedulerCrossProcessRemoval(t)
}

func TestExecutionClaimAndStopAreAtomicAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := New(Options{Path: path, Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(Job{ID: "migration-claim", Kind: ScheduleEvery, EverySeconds: 60, Message: "migration"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, complete, err := jobs.ClaimExecution(context.Background(), job.ID)
	if err != nil || complete == nil || claimed.Status != StatusRunning || claimed.Enabled {
		t.Fatalf("execution claim = %+v, complete=%v, err=%v", claimed, complete != nil, err)
	}
	if _, err := jobs.StopIfNotRunning(job.ID); !errors.Is(err, ErrExecutionInProgress) {
		t.Fatalf("stop during execution claim = %v, want ErrExecutionInProgress", err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].Status != StatusRunning || persisted[0].Enabled {
		t.Fatalf("persisted execution claim = %#v", persisted)
	}
	completed, err := complete(nil)
	if err != nil || completed.Status != StatusDisabled || completed.Enabled || completed.RunCount != 1 {
		t.Fatalf("completed execution = %+v, err=%v", completed, err)
	}
	restarted, err := New(Options{Path: path, Now: func() time.Time { return time.Unix(1001, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := restarted.Get(job.ID)
	if !ok || saved.Status != StatusDisabled || saved.Enabled || saved.ReviewRequired || saved.RunCount != 1 {
		t.Fatalf("reloaded completed execution = %+v, found=%v", saved, ok)
	}
}

func TestSchedulerDeletesOneTimeJobAfterExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	var gotMessage atomic.Value
	clock := time.Unix(1000, 0)
	scheduler, err := New(Options{
		Path: path,
		Now:  func() time.Time { return clock },
		Executor: func(_ context.Context, message string) error {
			gotMessage.Store(message)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := scheduler.Add(Job{Kind: ScheduleAt, AtSeconds: 1, Message: "one time"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
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
	checkSchedulerCrossProcessRetry(t)
	checkReviewRequiredOverridesRetry(t)
	checkRunningJobCannotRemoveItself(t)
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
	if _, err := scheduler.Add(Job{Kind: ScheduleAt, AtSeconds: 1, Message: "wake up"}); err != nil {
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

func TestDirectExecutionHoldsRunLockUntilCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	now := time.Unix(1000, 0)
	jobs, err := New(Options{Path: path, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Add(Job{ID: "direct-lock", Kind: ScheduleEvery, EverySeconds: 1, Message: "action"}); err != nil {
		t.Fatal(err)
	}
	_, finish, err := jobs.ClaimExecution(context.Background(), "direct-lock")
	if err != nil {
		t.Fatal(err)
	}
	defer finish(errors.New("test abandoned"))
	other, err := New(Options{Path: path, Now: func() time.Time { return now.Add(time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if runs := other.RunDue(context.Background()); runs != 0 {
		t.Fatalf("runs during direct claim: %d", runs)
	}
	job, ok := other.Get("direct-lock")
	if !ok || job.Status != StatusRunning || job.ReviewRequired {
		t.Fatalf("live claim changed: %+v", job)
	}
	if _, err := other.StopIfNotRunning(job.ID); !errors.Is(err, ErrExecutionInProgress) {
		t.Fatalf("stop during claim: %v", err)
	}
	if _, _, err := other.ClaimExecution(context.Background(), job.ID); !errors.Is(err, ErrExecutionClaimUnavailable) {
		t.Fatalf("overlapping claim: %v", err)
	}
	if _, err := finish(nil); err != nil {
		t.Fatal(err)
	}
	job, err = other.StopIfNotRunning(job.ID)
	if err != nil || job.Status != StatusDisabled {
		t.Fatalf("stop after completion: %+v %v", job, err)
	}
}

func TestScheduledExecutionOwnershipAndOverlap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	now := time.Unix(1000, 0)
	started := make(chan context.Context, 1)
	proceed := make(chan struct{})
	result := make(chan error, 1)
	var claim func(context.Context, string) (Job, func(error) (Job, error), error)
	jobs, err := New(Options{Path: path, Now: func() time.Time { return now.Add(time.Hour) }, Executor: func(ctx context.Context, _ string) error {
		started <- ctx
		<-proceed
		_, finish, err := claim(ctx, "scheduled-owner")
		if err == nil && finish != nil {
			return errors.New("scheduled claim returned direct completion")
		}
		if err == nil {
			_, _, err = claim(ctx, "scheduled-owner")
			if !errors.Is(err, ErrExecutionClaimUnavailable) {
				return errors.New("scheduled claim was reusable")
			}
			return nil
		}
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	claim = jobs.ClaimExecution
	if _, err := jobs.Add(Job{ID: "scheduled-owner", Kind: ScheduleEvery, EverySeconds: 1, Message: "action"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	go func() {
		if runs := jobs.RunDue(context.Background()); runs != 1 {
			result <- errors.New("unexpected RunDue count")
			return
		}
		result <- nil
	}()
	var executionContext context.Context
	select {
	case executionContext = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
	other, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.ClaimExecution(context.Background(), "scheduled-owner"); !errors.Is(err, ErrExecutionClaimUnavailable) {
		t.Fatalf("immediate borrowed claim: %v", err)
	}
	if _, err := other.StopIfNotRunning("scheduled-owner"); !errors.Is(err, ErrExecutionInProgress) {
		t.Fatalf("stop during scheduled execution: %v", err)
	}
	if runs := other.RunDue(context.Background()); runs != 0 {
		t.Fatalf("overlapping RunDue: %d", runs)
	}
	close(proceed)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, _, err := jobs.ClaimExecution(executionContext, "scheduled-owner"); !errors.Is(err, ErrExecutionClaimUnavailable) {
		t.Fatalf("expired owner: %v", err)
	}
	job, ok := other.Get("scheduled-owner")
	if !ok || job.Enabled || job.Status == StatusRunning || job.ReviewRequired {
		t.Fatalf("finished job: %+v", job)
	}
	if _, err := other.StopIfNotRunning("scheduled-owner"); err != nil {
		t.Fatalf("stop after scheduled execution: %v", err)
	}
}

func TestRunningSchedulerRecoversInterruptedExecutionClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	first, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	job, err := first.Add(Job{ID: "interrupted-claim", Kind: ScheduleEvery, EverySeconds: 60, Message: "work"})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := first.store.lock(true)
	if err != nil {
		t.Fatal(err)
	}
	job.Status, job.Enabled, job.ExecutionClaim, job.NextRun = StatusRunning, false, true, nil
	if err := first.store.Save([]Job{job}); err != nil {
		unlock()
		t.Fatal(err)
	}
	second, err := New(Options{Path: path})
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock() // Simulate the first process exiting without finalizing its claim.
	if runs := second.RunDue(context.Background()); runs != 0 {
		t.Fatalf("runs = %d", runs)
	}
	recovered, ok := second.Get(job.ID)
	if !ok || recovered.ExecutionClaim || !recovered.ReviewRequired || recovered.Enabled || recovered.Status != StatusFailed {
		t.Fatalf("recovered = %+v", recovered)
	}
	if _, err := second.SetEnabled(job.ID, true); err != nil {
		t.Fatalf("explicit re-enable: %v", err)
	}
}
