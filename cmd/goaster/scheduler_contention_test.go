package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/chat"
	"goaster/internal/scheduler"
)

type scheduledTestProvider struct {
	entered chan struct{}
	release chan struct{}
	history []chat.ChatMessage
}

type scheduledReviewProvider func(context.Context, string, string) (string, error)

func (provider scheduledReviewProvider) Reply(ctx context.Context, model, message string) (string, error) {
	return provider(ctx, model, message)
}

type scheduledReviewError struct{ cause error }

func (scheduledReviewError) Error() string {
	return "OpenAI authentication failed after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state, then resolve OpenAI authentication before retrying."
}

func (err scheduledReviewError) Is(target error) bool { return target.Error() == err.Error() }
func (err scheduledReviewError) Unwrap() error        { return err.cause }

type scheduledInterruptedError struct{ cause error }

func (scheduledInterruptedError) Error() string {
	return "Scheduled run was interrupted after a tool started. Actions may already have completed and tool results may be unavailable. Check and reconcile external state before retrying."
}

func (err scheduledInterruptedError) Is(target error) bool { return target.Error() == err.Error() }
func (err scheduledInterruptedError) Unwrap() error        { return err.cause }

func checkScheduledInterruptedActionNeedsReview(t interface {
	TempDir() string
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	for _, spec := range []scheduler.Job{
		{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once"},
		{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat"},
	} {
		clock := time.Unix(1000, 0)
		session := chat.NewSession(chat.Config{Model: "test"}, scheduledReviewProvider(func(context.Context, string, string) (string, error) {
			return "", scheduledInterruptedError{cause: context.Canceled}
		}))
		executor := scheduledExecutor(session, nil)
		var executionErr error
		options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(ctx context.Context, message string) error {
			executionErr = executor(ctx, message)
			return executionErr
		}}
		jobs, err := scheduler.New(options)
		if err != nil {
			t.Fatal(err)
		}
		job, err := jobs.Add(spec)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Second)
		if runs := jobs.RunDue(context.Background()); runs != 1 || !errors.Is(executionErr, context.Canceled) || !errors.Is(executionErr, scheduler.ErrReviewRequired) {
			t.Fatalf("%s: runs=%d interrupted action error=%v", spec.Kind, runs, executionErr)
		}
		jobs, err = scheduler.New(options)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Minute)
		if saved, ok := jobs.Get(job.ID); !ok || saved.Enabled || !saved.ReviewRequired || saved.Error != (scheduledInterruptedError{}).Error() || saved.RunCount != 1 || saved.NextRun != nil || jobs.RunDue(context.Background()) != 0 {
			t.Fatalf("%s: interrupted action was not retained for review: %+v", spec.Kind, saved)
		}
	}

	// Providers without action tracking cannot prove that an interruption was
	// pre-action, so their scheduled runs must be retained for review.
	for _, spec := range []scheduler.Job{
		{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once"},
		{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat"},
	} {
		for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
			for _, cancelParent := range []bool{false, true} {
				parent, cancel := context.WithCancel(context.Background())
				cause := errors.New("provider child failure")
				provider := scheduledReviewProvider(func(context.Context, string, string) (string, error) {
					if cancelParent {
						cancel()
					}
					return "", errors.Join(cause, failure)
				})
				session := chat.NewSession(chat.Config{Model: "test"}, provider)
				if !cancelParent {
					_, err := session.Reply(parent, "ordinary turn")
					if !errors.Is(err, failure) || !errors.Is(err, cause) || chat.ScheduledActionReviewMessage(err) != "" || errors.Is(err, chat.ErrSessionBusy) {
						t.Fatalf("%s %v unscheduled error=%v", spec.Kind, failure, err)
					}
				}
				clock := time.Unix(1000, 0)
				executor := scheduledExecutor(session, nil)
				var executionErr error
				options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(ctx context.Context, message string) error {
					executionErr = executor(ctx, message)
					return executionErr
				}}
				jobs, err := scheduler.New(options)
				if err != nil {
					t.Fatal(err)
				}
				job, err := jobs.Add(spec)
				if err != nil {
					t.Fatal(err)
				}
				clock = clock.Add(time.Second)
				if runs := jobs.RunDue(parent); runs != 1 || !errors.Is(executionErr, scheduler.ErrReviewRequired) || errors.Is(executionErr, scheduler.ErrRetry) || !errors.Is(executionErr, cause) || !errors.Is(executionErr, failure) || (cancelParent && !errors.Is(executionErr, context.Canceled)) || (!cancelParent && parent.Err() != nil) {
					t.Fatalf("%s %v parent canceled=%v: runs=%d error=%v", spec.Kind, failure, cancelParent, runs, executionErr)
				}
				cancel()
				jobs, err = scheduler.New(options)
				if err != nil {
					t.Fatal(err)
				}
				clock = clock.Add(time.Minute)
				want := "Scheduled run was interrupted and its action status is unknown. Actions may already have completed and tool results may be unavailable. Check and reconcile external state before retrying."
				if saved, ok := jobs.Get(job.ID); !ok || saved.Enabled || !saved.ReviewRequired || saved.RunCount != 1 || saved.Status != scheduler.StatusFailed || saved.Error != want || saved.NextRun != nil || jobs.RunDue(context.Background()) != 0 {
					t.Fatalf("%s %v parent canceled=%v: saved=%+v", spec.Kind, failure, cancelParent, saved)
				}
			}
		}
	}
}

func checkScheduledAuthenticationNeedsReview(t interface {
	TempDir() string
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	for _, tc := range []struct {
		name      string
		job       scheduler.Job
		cancelRun bool
		cause     error
	}{
		{"one-time", scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once"}, false, nil},
		{"recurring", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat"}, false, nil},
		{"canceled one-time", scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "once canceled"}, true, context.Canceled},
		{"canceled recurring", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat canceled"}, true, context.Canceled},
		{"body deadline", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat timeout"}, false, context.DeadlineExceeded},
		{"deadline then cancellation", scheduler.Job{Kind: scheduler.ScheduleEvery, EverySeconds: 1, Message: "repeat interrupted"}, true, context.DeadlineExceeded},
	} {
		clock := time.Unix(1000, 0)
		calls := 0
		fail := true
		ctx, cancel := context.WithCancel(context.Background())
		provider := scheduledReviewProvider(func(context.Context, string, string) (string, error) {
			calls++
			if !fail {
				return "done", nil
			}
			if tc.cancelRun {
				cancel()
			}
			return "", scheduledReviewError{cause: tc.cause}
		})
		session := chat.NewSession(chat.Config{Model: "test"}, provider)
		executor := scheduledExecutor(session, nil)
		var executionErr error
		options := scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return clock }, Executor: func(ctx context.Context, message string) error {
			executionErr = executor(ctx, message)
			return executionErr
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
		if runs := jobs.RunDue(ctx); runs != 1 {
			t.Fatalf("%s: runs=%d", tc.name, runs)
		}
		if !errors.Is(executionErr, scheduler.ErrReviewRequired) || (tc.cause != nil && !errors.Is(executionErr, tc.cause)) || (tc.cancelRun && !errors.Is(executionErr, context.Canceled)) {
			t.Fatalf("%s: execution error lost review or cancellation: %v", tc.name, executionErr)
		}
		cancel()
		jobs, err = scheduler.New(options)
		if err != nil {
			t.Fatal(err)
		}
		saved, ok := jobs.Get(job.ID)
		if !ok || saved.Enabled || !saved.ReviewRequired || saved.NextRun != nil || saved.RunCount != 1 || saved.Status != scheduler.StatusFailed || saved.Error != (scheduledReviewError{}).Error() {
			t.Fatalf("%s: review state after restart = %+v", tc.name, saved)
		}
		clock = clock.Add(2 * time.Minute)
		if runs := jobs.RunDue(context.Background()); runs != 0 || calls != 1 {
			t.Fatalf("%s: future tick replayed: runs=%d calls=%d", tc.name, runs, calls)
		}
		saved, ok = jobs.Get(job.ID)
		if !ok || !saved.ReviewRequired || saved.Error != (scheduledReviewError{}).Error() {
			t.Fatalf("%s: future tick lost warning: %+v", tc.name, saved)
		}
		if _, err := jobs.SetEnabled(job.ID, false); err != nil {
			t.Fatal(err)
		}
		saved, ok = jobs.Get(job.ID)
		if !ok || !saved.ReviewRequired || saved.Error != (scheduledReviewError{}).Error() {
			t.Fatalf("%s: disabling lost warning: %+v", tc.name, saved)
		}
		fail = false
		if _, err := jobs.SetEnabled(job.ID, true); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Second)
		if runs := jobs.RunDue(context.Background()); runs != 1 || calls != 2 {
			t.Fatalf("%s: explicitly re-enabled run: runs=%d calls=%d", tc.name, runs, calls)
		}
	}
}

func checkSkillPreflightFailureKeepsOneShot(t interface {
	TempDir() string
	Setenv(string, string)
	Fatal(...any)
	Fatalf(string, ...any)
}) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	skillDir := filepath.Join(home, ".goaster", "artefacts", "skills", "broken")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"META.md":  "name: broken\ndescription: invalid skill\nunexpected: data\n",
		"SKILL.md": "workflow", "AUTH.md": "invalid \xff",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session, err := chat.NewPersistentSessionAt(chat.Config{Model: "test"}, chat.NewCodexProvider(chat.CredentialStore{}), filepath.Join(home, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1000, 0)
	sched, err := scheduler.New(scheduler.Options{Path: filepath.Join(home, "jobs.json"), Now: func() time.Time { return clock }, Executor: scheduledExecutor(session, nil)})
	if err != nil {
		t.Fatal(err)
	}
	job, err := sched.Add(scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "run later"})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	if runs := sched.RunDue(context.Background()); runs != 1 {
		t.Fatalf("attempts = %d", runs)
	}
	pending, ok := sched.Get(job.ID)
	if !ok || pending.Status != scheduler.StatusScheduled || pending.RunCount != 0 || pending.NextRun == nil || !pending.NextRun.After(clock) {
		t.Fatalf("preflight failure lost one-shot: %+v", pending)
	}
	restarted, err := scheduler.New(scheduler.Options{Path: filepath.Join(home, "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	if saved, ok := restarted.Get(job.ID); !ok || saved.Status != scheduler.StatusScheduled || saved.RunCount != 0 {
		t.Fatalf("preflight retry was not durable: %+v", saved)
	}
}

func (p *scheduledTestProvider) Reply(ctx context.Context, model, message string) (string, error) {
	return p.ReplyWithHistory(ctx, model, []chat.ChatMessage{{Role: "user", Content: message}})
}
func (p *scheduledTestProvider) ReplyWithHistory(_ context.Context, _ string, history []chat.ChatMessage) (string, error) {
	p.history = append([]chat.ChatMessage(nil), history...)
	if p.entered != nil {
		close(p.entered)
		<-p.release
	}
	return "completed", nil
}

func TestOneShotRetriesSessionContentionAndReloadsConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	jobPath := filepath.Join(t.TempDir(), "jobs.json")
	ownerProvider := &scheduledTestProvider{}
	owner, err := chat.NewPersistentSessionAt(chat.Config{Model: "test"}, ownerProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduledExecutor(owner, nil)(canceled, "not started"); !errors.Is(err, scheduler.ErrRetry) {
		t.Fatalf("canceled before starting = %v", err)
	}
	externalProvider := &scheduledTestProvider{entered: make(chan struct{}), release: make(chan struct{})}
	external, err := chat.NewPersistentSessionAt(chat.Config{Model: "test"}, externalProvider, path)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1000, 0)
	options := scheduler.Options{Path: jobPath, Now: func() time.Time { return clock }, Executor: scheduledExecutor(owner, nil)}
	sched, err := scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	job, err := sched.Add(scheduler.Job{Kind: scheduler.ScheduleAt, AtSeconds: 1, Message: "scheduled task"})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := external.Reply(context.Background(), "external turn"); finished <- err }()
	select {
	case <-externalProvider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("external turn did not start")
	}
	clock = clock.Add(time.Second)
	if runs := sched.RunDue(context.Background()); runs != 1 {
		t.Fatalf("claimed = %d", runs)
	}
	pending, ok := sched.Get(job.ID)
	if !ok || pending.Status != scheduler.StatusScheduled || pending.RunCount != 0 || pending.NextRun == nil || !pending.NextRun.After(clock) || len(ownerProvider.history) != 0 {
		t.Fatalf("contended one-shot not retained: %+v", pending)
	}
	close(externalProvider.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	// The retry remains durable across a scheduler restart.
	sched, err = scheduler.New(options)
	if err != nil {
		t.Fatal(err)
	}
	clock = *pending.NextRun
	if runs := sched.RunDue(context.Background()); runs != 1 {
		t.Fatalf("retry = %d", runs)
	}
	if _, ok := sched.Get(job.ID); ok {
		t.Fatal("completed one-shot retained")
	}
	var text strings.Builder
	for _, message := range ownerProvider.history {
		text.WriteString(message.Content)
		text.WriteByte('\n')
	}
	if !strings.Contains(text.String(), "external turn") || !strings.Contains(text.String(), "scheduled task") {
		t.Fatalf("scheduled turn did not reload the changed conversation: %q", text.String())
	}
	checkSkillPreflightFailureKeepsOneShot(t)
	checkScheduledAuthenticationNeedsReview(t)
	checkScheduledInterruptedActionNeedsReview(t)
}
