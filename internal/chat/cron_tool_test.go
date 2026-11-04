package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

type cronToolTestRegistrar struct {
	tool Tool
}

func (r *cronToolTestRegistrar) RegisterTool(tool Tool) error {
	r.tool = tool
	return nil
}

func TestCronToolManagesJobsAndPreservesOmittedFields(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != cronToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered cron tool = %+v", registrar.tool)
	}

	result, err := registrar.tool.Handler(context.Background(), `{"action":"add","every_seconds":60,"message":"first message","max_runs":3}`)
	if err != nil {
		t.Fatal(err)
	}
	var added scheduler.Job
	if err := json.Unmarshal([]byte(result), &added); err != nil {
		t.Fatalf("decode add result %q: %v", result, err)
	}
	if added.Kind != scheduler.ScheduleEvery || added.EverySeconds != 60 || added.MaxRuns != 3 || added.Message != "first message" {
		t.Fatalf("added job = %+v", added)
	}

	result, err = registrar.tool.Handler(context.Background(), `{"action":"update","id":"`+added.ID+`","message":"updated message"}`)
	if err != nil {
		t.Fatal(err)
	}
	var updated scheduler.Job
	if err := json.Unmarshal([]byte(result), &updated); err != nil {
		t.Fatalf("decode update result %q: %v", result, err)
	}
	if updated.Message != "updated message" || updated.Kind != scheduler.ScheduleEvery || updated.EverySeconds != 60 || updated.MaxRuns != 3 || updated.NextRun == nil {
		t.Fatalf("partial tool update did not preserve fields: %+v", updated)
	}

	if _, err := registrar.tool.Handler(context.Background(), `{"action":"disable","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"enable","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	listResult, err := registrar.tool.Handler(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Jobs []scheduler.Job `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(listResult), &listed); err != nil {
		t.Fatalf("decode list result %q: %v", listResult, err)
	}
	if len(listed.Jobs) != 1 || listed.Jobs[0].ID != added.ID {
		t.Fatalf("listed jobs = %+v", listed.Jobs)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"remove","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := jobs.Get(added.ID); ok {
		t.Fatal("cron tool remove left job behind")
	}
}

func TestCronToolSupportsAtAndCronSchedulesAndRejectsAmbiguousAdd(t *testing.T) {
	clock := time.Unix(1000, 0)
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	atSeconds := int64(3600)
	result, err := registrar.tool.Handler(context.Background(), `{"action":"add","at_seconds":`+formatInt64(atSeconds)+`,"message":"later"}`)
	if err != nil {
		t.Fatal(err)
	}
	var atJob scheduler.Job
	if err := json.Unmarshal([]byte(result), &atJob); err != nil {
		t.Fatal(err)
	}
	if atJob.Kind != scheduler.ScheduleAt || atJob.AtSeconds != atSeconds || !atJob.DeleteAfterRun || atJob.NextRun == nil || !atJob.NextRun.Equal(clock.Add(time.Duration(atSeconds)*time.Second)) {
		t.Fatalf("at job = %+v, want delete-after-run", atJob)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"add","at_seconds":0,"message":"invalid"}`); err == nil {
		t.Fatal("at_seconds=0 unexpectedly succeeded")
	}
	updatedAtSeconds := int64(10)
	result, err = registrar.tool.Handler(context.Background(), `{"action":"update","id":"`+atJob.ID+`","at_seconds":`+formatInt64(updatedAtSeconds)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(result), &atJob); err != nil {
		t.Fatal(err)
	}
	if atJob.AtSeconds != updatedAtSeconds || atJob.NextRun == nil || !atJob.NextRun.Equal(clock.Add(time.Duration(updatedAtSeconds)*time.Second)) {
		t.Fatalf("updated at job = %+v, want 10 seconds from now", atJob)
	}
	result, err = registrar.tool.Handler(context.Background(), `{"action":"add","cron_expr":"0 9 * * 1","message":"weekly"}`)
	if err != nil {
		t.Fatal(err)
	}
	var cronJob scheduler.Job
	if err := json.Unmarshal([]byte(result), &cronJob); err != nil {
		t.Fatal(err)
	}
	if cronJob.Kind != scheduler.ScheduleCron || cronJob.CronExpr != "0 9 * * 1" {
		t.Fatalf("cron job = %+v", cronJob)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"add","every_seconds":10,"cron_expr":"* * * * *","message":"ambiguous"}`); err == nil {
		t.Fatal("ambiguous cron add unexpectedly succeeded")
	}
	if description, ok := registrar.tool.Parameters["properties"].(map[string]any)["at_seconds"].(map[string]any)["description"].(string); !ok || description == "" {
		t.Fatal("at_seconds tool description is missing")
	} else if description != "Positive number of seconds from now for a one-time chat turn" {
		t.Fatalf("at_seconds tool description = %q", description)
	}
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestCronConcurrentLimitEditPreservesReviewAndEdits(t *testing.T) {
	var seconds atomic.Int64
	seconds.Store(1000)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	options := scheduler.Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return time.Unix(seconds.Load(), 0) },
		Executor: func(context.Context, string) error {
			if calls.Add(1) == 2 {
				close(entered)
				<-release
				return scheduler.ReviewRequiredError{Message: "reconcile the completed actions"}
			}
			return nil
		},
	}
	jobs, err := scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "original", MaxRuns: 3})
	if err != nil {
		t.Fatal(err)
	}
	seconds.Add(1)
	if runs := jobs.RunDue(context.Background()); runs != 1 {
		t.Fatalf("first run = %d", runs)
	}
	seconds.Add(1)
	finished := make(chan int, 1)
	go func() { finished <- jobs.RunDue(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second run did not enter executor")
	}

	// Use another scheduler instance to exercise the persisted transaction and
	// the same locking used by separate processes.
	editor, err := scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	invalid := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":1,"message":"must not be saved"}`, job.ID)
	if _, err := executeCronTool(context.Background(), invalid, editor); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("limit without room for the claim = %v", err)
	}
	active, ok := editor.Get(job.ID)
	if !ok || active.Status != scheduler.StatusRunning || active.RunCount != 1 || active.MaxRuns != 3 || active.Message != "original" {
		t.Fatalf("rejected update changed the running job: %+v", active)
	}
	valid := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":2,"message":"edited","every_seconds":7}`, job.ID)
	if _, err := executeCronTool(context.Background(), valid, editor); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"disable", "enable"} {
		if _, err := executeCronTool(context.Background(), fmt.Sprintf(`{"action":%q,"id":%q}`, action, job.ID), editor); err != nil {
			t.Fatal(err)
		}
		active, ok = editor.Get(job.ID)
		if !ok || active.Status != scheduler.StatusRunning || active.Enabled != (action == "enable") {
			t.Fatalf("%s erased the running claim: %+v", action, active)
		}
	}
	if runs := editor.RunDue(context.Background()); runs != 0 {
		t.Fatalf("another scheduler claimed the active run: %d", runs)
	}
	unblock()
	select {
	case runs := <-finished:
		if runs != 1 {
			t.Fatalf("second run = %d", runs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second run did not finish")
	}
	jobs, err = scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := jobs.Get(job.ID)
	if !ok || saved.Enabled || !saved.ReviewRequired || saved.Status != scheduler.StatusFailed || saved.Error != "reconcile the completed actions" || saved.RunCount != 2 || saved.MaxRuns != 2 || saved.Message != "edited" || saved.EverySeconds != 7 || saved.NextRun != nil || saved.LastRun == nil {
		t.Fatalf("review or concurrent edits lost after restart: %+v", saved)
	}
	for _, limit := range []int{2, 3, 0} {
		args := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":%d}`, job.ID, limit)
		if _, err := executeCronTool(context.Background(), args, jobs); err != nil {
			t.Fatal(err)
		}
		saved, ok = jobs.Get(job.ID)
		if !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != "reconcile the completed actions" || saved.RunCount != 2 || saved.MaxRuns != limit {
			t.Fatalf("limit edit cleared review: %+v", saved)
		}
	}
	seconds.Add(100)
	if runs := jobs.RunDue(context.Background()); runs != 0 || calls.Load() != 2 {
		t.Fatalf("reviewed job replayed: runs=%d calls=%d", runs, calls.Load())
	}
}

func TestCronConcurrentLimitEditPreservesSuccessAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%v", retry), func(t *testing.T) {
			var seconds atomic.Int64
			seconds.Store(1000)
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			options := scheduler.Options{
				Path: filepath.Join(t.TempDir(), "jobs.json"),
				Now:  func() time.Time { return time.Unix(seconds.Load(), 0) },
				Executor: func(context.Context, string) error {
					if calls.Add(1) == 2 {
						close(entered)
						<-release
						if retry {
							return scheduler.ErrRetry
						}
					}
					return nil
				},
			}
			jobs, err := scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "original", MaxRuns: 3})
			if err != nil {
				t.Fatal(err)
			}
			seconds.Add(1)
			if runs := jobs.RunDue(context.Background()); runs != 1 {
				t.Fatalf("first run = %d", runs)
			}
			seconds.Add(1)
			finished := make(chan int, 1)
			go func() { finished <- jobs.RunDue(context.Background()) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("second run did not enter executor")
			}
			args := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":2,"message":"edited","every_seconds":7}`, job.ID)
			if _, err := executeCronTool(context.Background(), args, jobs); err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case runs := <-finished:
				if runs != 1 {
					t.Fatalf("second run = %d", runs)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("second run did not finish")
			}
			jobs, err = scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			saved, ok := jobs.Get(job.ID)
			if !ok || saved.ReviewRequired || saved.Message != "edited" || saved.EverySeconds != 7 || saved.MaxRuns != 2 {
				t.Fatalf("completion lost concurrent edits: %+v", saved)
			}
			if retry {
				if !saved.Enabled || saved.Status != scheduler.StatusScheduled || saved.RunCount != 1 || saved.NextRun == nil || !saved.NextRun.Equal(time.Unix(1009, 0)) {
					t.Fatalf("retry did not preserve the updated schedule: %+v", saved)
				}
				if runs := jobs.RunDue(context.Background()); runs != 0 {
					t.Fatalf("retry ran before the updated schedule: %d", runs)
				}
				seconds.Add(7)
				if runs := jobs.RunDue(context.Background()); runs != 1 || calls.Load() != 3 {
					t.Fatalf("updated retry: runs=%d calls=%d", runs, calls.Load())
				}
				saved, ok = jobs.Get(job.ID)
			}
			if !ok || saved.Enabled || saved.Status != scheduler.StatusCompleted || saved.RunCount != 2 || saved.NextRun != nil {
				t.Fatalf("run limit after completion: %+v", saved)
			}
		})
	}
}

func TestCronReviewRecoveryAtRunLimitRequiresExplicitEnable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		job   scheduler.Job
		limit int
	}{
		{"one-time raised limit", scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once", MaxRuns: 1}, 2},
		{"one-time unlimited", scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once", MaxRuns: 1}, 0},
		{"recurring raised limit", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat", MaxRuns: 1}, 2},
		{"recurring unlimited", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat", MaxRuns: 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Unix(1000, 0)
			calls := 0
			options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
				calls++
				if calls == 1 {
					return scheduler.ReviewRequiredError{Message: "reconcile first"}
				}
				return nil
			}}
			jobs, err := scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			job, err := jobs.Add(tc.job)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Second)
			if runs := jobs.RunDue(context.Background()); runs != 1 {
				t.Fatalf("review run = %d", runs)
			}
			enable := fmt.Sprintf(`{"action":"enable","id":%q}`, job.ID)
			if _, err := executeCronTool(context.Background(), enable, jobs); err == nil || !strings.Contains(err.Error(), "increase max_runs above run_count or set it to 0") {
				t.Fatalf("enable at limit = %v", err)
			}
			update := fmt.Sprintf(`{"action":"update","id":%q,"max_runs":%d}`, job.ID, tc.limit)
			if _, err := executeCronTool(context.Background(), update, jobs); err != nil {
				t.Fatal(err)
			}
			jobs, err = scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
			saved, ok := jobs.Get(job.ID)
			if !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != "reconcile first" || saved.RunCount != 1 || saved.MaxRuns != tc.limit || jobs.RunDue(context.Background()) != 0 || calls != 1 {
				t.Fatalf("limit edit or restart cleared review: %+v calls=%d", saved, calls)
			}
			if _, err := executeCronTool(context.Background(), enable, jobs); err != nil {
				t.Fatal(err)
			}
			saved, ok = jobs.Get(job.ID)
			if !ok || !saved.Enabled || saved.ReviewRequired || saved.Error != "" || saved.NextRun == nil {
				t.Fatalf("explicit enable did not clear review: %+v", saved)
			}
			clock = clock.Add(time.Second)
			if runs := jobs.RunDue(context.Background()); runs != 1 || calls != 2 {
				t.Fatalf("explicit recovery: runs=%d calls=%d", runs, calls)
			}
		})
	}
}

func TestCronInterruptedClaimRemainsInReviewThroughLimitEdits(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			clock := time.Unix(1000, 0)
			calls := 0
			options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(context.Context, string) error {
				calls++
				return nil
			}}
			jobs, err := scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			store, err := scheduler.NewStore(options.Path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save([]scheduler.Job{{ID: "interrupted", Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat", Enabled: true, Status: scheduler.StatusRunning, RunCount: 1, MaxRuns: 2}}); err != nil {
				t.Fatal(err)
			}
			if restart {
				jobs, err = scheduler.New(options)
				if err != nil {
					t.Fatal(err)
				}
			} else if runs := jobs.RunDue(context.Background()); runs != 0 {
				t.Fatalf("interrupted claim replayed: %d", runs)
			}
			for _, args := range []string{
				`{"action":"disable","id":"interrupted"}`,
				`{"action":"update","id":"interrupted","max_runs":1}`,
				`{"action":"update","id":"interrupted","max_runs":2}`,
				`{"action":"update","id":"interrupted","every_seconds":7}`,
			} {
				if _, err := executeCronTool(context.Background(), args, jobs); err != nil {
					t.Fatal(err)
				}
			}
			jobs, err = scheduler.New(options)
			if err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
			saved, ok := jobs.Get("interrupted")
			if !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != "previous run was interrupted" || saved.RunCount != 1 || saved.MaxRuns != 2 || saved.EverySeconds != 7 || jobs.RunDue(context.Background()) != 0 || calls != 0 {
				t.Fatalf("edits lost interrupted-run review: %+v calls=%d", saved, calls)
			}
			if _, err := executeCronTool(context.Background(), `{"action":"enable","id":"interrupted"}`, jobs); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(7 * time.Second)
			if runs := jobs.RunDue(context.Background()); runs != 1 || calls != 1 {
				t.Fatalf("explicitly enabled interrupted job: runs=%d calls=%d", runs, calls)
			}
		})
	}
}
