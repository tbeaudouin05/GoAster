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
}
