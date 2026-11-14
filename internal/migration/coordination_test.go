package migration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

type offlineDoer func(*http.Request) (*http.Response, error)

func (f offlineDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

type badVerificationScheduler struct {
	*scheduler.Scheduler
	failStop bool
}

func (s *badVerificationScheduler) Get(id string) (scheduler.Job, bool) {
	job, ok := s.Scheduler.Get(id)
	if ok {
		job.Message = "invalid"
	}
	return job, ok
}
func (s *badVerificationScheduler) StopIfNotRunning(id string) (scheduler.Job, error) {
	if s.failStop {
		return scheduler.Job{}, errors.New("synthetic stop failure")
	}
	return s.Scheduler.StopIfNotRunning(id)
}

func TestOfflineClaimCoordinatesImmediateCheck(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	options := Options{WebOrigin: "https://example.invalid", LoadHetznerToken: func() (string, error) { return "synthetic", nil }, DeliverHandoff: func(context.Context, []byte) error { return nil }, HTTPClient: offlineDoer(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return nil, errors.New("synthetic availability failure")
	})}
	request := Request{ServerType: "cx22", JobID: "migration-offline-race"}
	done := make(chan error, 1)
	go func() { _, err := StartMonitor(t.Context(), request, options, jobs); done <- err }()
	<-started
	if _, err := jobs.SetEnabled(request.JobID, true); !errors.Is(err, scheduler.ErrExecutionInProgress) {
		t.Fatalf("enable running job: %v", err)
	}
	if result, err := StartMonitor(t.Context(), request, options, jobs); !errors.Is(err, scheduler.ErrExecutionInProgress) || result.ManualReview {
		t.Fatalf("overlapping start: %+v, %v", result, err)
	}
	if _, err := StopMonitor(request.JobID, jobs); !errors.Is(err, scheduler.ErrExecutionInProgress) {
		t.Fatalf("stop running job: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get(request.JobID)
	if !ok || job.Enabled || !job.ReviewRequired {
		t.Fatalf("failure state: %+v", job)
	}
}

func TestOfflineVerificationFailureStopsMonitor(t *testing.T) {
	for _, failStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "stop_failure"}[failStop], func(t *testing.T) {
			base, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
			if err != nil {
				t.Fatal(err)
			}
			jobs := &badVerificationScheduler{Scheduler: base, failStop: failStop}
			options := Options{DeliverHandoff: func(context.Context, []byte) error { return nil }, HTTPClient: offlineDoer(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected request")
				return &http.Response{Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			result, err := StartMonitor(t.Context(), Request{ServerType: "cx22", JobID: "migration-verification"}, options, jobs)
			job, ok := base.Get("migration-verification")
			if !ok {
				t.Fatal("job missing")
			}
			if failStop {
				if err == nil || !job.Enabled || result.ManualReview {
					t.Fatalf("unconfirmed stop: %+v %v %+v", result, err, job)
				}
			} else if err != nil || job.Enabled || !result.ManualReview {
				t.Fatalf("confirmed stop: %+v %v %+v", result, err, job)
			}
		})
	}
}

func TestOfflineScheduledUnavailableReschedules(t *testing.T) {
	now := time.Unix(1800000000, 0)
	var jobs *scheduler.Scheduler
	options := Options{WebOrigin: "https://example.invalid", LoadHetznerToken: func() (string, error) { return "synthetic", nil }, DeliverHandoff: func(context.Context, []byte) error { return nil }, HTTPClient: offlineDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"version":"v1","server_type":"cx22","locations":[]}`))}, nil
	})}
	var err error
	jobs, err = scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return now }, Executor: func(ctx context.Context, msg string) error { return ScheduledExecutor(ctx, msg, jobs, options) }})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{ServerType: "cx22", JobID: "migration-scheduled-offline"}
	msg, err := schedulerMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.Add(scheduler.Job{ID: req.JobID, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds, Message: msg}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Duration(MonitorIntervalSeconds) * time.Second)
	if runs := jobs.RunDue(t.Context()); runs != 1 {
		t.Fatalf("runs = %d", runs)
	}
	job, ok := jobs.Get(req.JobID)
	if !ok || !job.Enabled || job.ExecutionClaim || job.NextRun == nil || !job.NextRun.Equal(now.Add(time.Duration(MonitorIntervalSeconds)*time.Second)) {
		t.Fatalf("job = %+v", job)
	}
}

type changingClaimScheduler struct {
	MonitorScheduler
	coordinator monitorExecutionCoordinator
	change      func()
}

func (s changingClaimScheduler) ClaimExecutionMatching(ctx context.Context, expected scheduler.Job) (scheduler.Job, func(error) (scheduler.Job, error), error) {
	s.change()
	return s.coordinator.ClaimExecutionMatching(ctx, expected)
}
func (s changingClaimScheduler) StopIfNotRunning(id string) (scheduler.Job, error) {
	return s.coordinator.StopIfNotRunning(id)
}

func TestClaimRejectsChangedMessageAndSchedule(t *testing.T) {
	for _, field := range []string{"message", "schedule"} {
		for _, scheduled := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "_immediate", true: "_scheduled"}[scheduled], func(t *testing.T) {
				now := time.Unix(1800000000, 0)
				path := filepath.Join(t.TempDir(), "jobs.json")
				var jobs *scheduler.Scheduler
				var wrapped changingClaimScheduler
				calls := 0
				opts := Options{LoadHetznerToken: func() (string, error) { calls++; return "synthetic", nil }, DeliverHandoff: func(context.Context, []byte) error { calls++; return nil }, HTTPClient: offlineDoer(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected request") })}
				var err error
				jobs, err = scheduler.New(scheduler.Options{Path: path, Now: func() time.Time { return now }, Executor: func(ctx context.Context, message string) error { return ScheduledExecutor(ctx, message, wrapped, opts) }})
				if err != nil {
					t.Fatal(err)
				}
				store, err := scheduler.NewStore(path)
				if err != nil {
					t.Fatal(err)
				}
				wrapped = changingClaimScheduler{MonitorScheduler: jobs, coordinator: jobs, change: func() {
					saved, err := store.Load()
					if err != nil {
						t.Fatal(err)
					}
					if field == "message" {
						saved[0].Message = "changed"
					} else {
						saved[0].EverySeconds++
					}
					if err := store.Save(saved); err != nil {
						t.Fatal(err)
					}
				}}
				req := Request{ServerType: "cx22", JobID: "migration-race"}
				if scheduled {
					msg, err := schedulerMessage(req)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := jobs.Add(scheduler.Job{ID: req.JobID, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds, Message: msg}); err != nil {
						t.Fatal(err)
					}
					now = now.Add(time.Duration(MonitorIntervalSeconds) * time.Second)
					if jobs.RunDue(t.Context()) != 1 {
						t.Fatal("job did not run")
					}
				} else {
					if result, err := StartMonitor(t.Context(), req, opts, wrapped); err != nil || !result.ManualReview {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				}
				job, ok := jobs.Get(req.JobID)
				if !ok || job.Enabled || (scheduled && !job.ReviewRequired) || job.ExecutionClaim || calls != 0 {
					t.Fatalf("job=%+v calls=%d", job, calls)
				}
			})
		}
	}
}

func TestRunDueAvailableCreateLostResponseAndStop(t *testing.T) {
	for _, scenario := range []string{"created", "lost_response", "stop_during_availability"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Unix(1800000000, 0).UTC()
			started, release := make(chan struct{}), make(chan struct{})
			availability, creates, deliveries := 0, 0, 0
			options := Options{WebOrigin: "https://example.invalid", Now: func() time.Time { return now }, TempDirectory: t.TempDir(), LoadHetznerToken: func() (string, error) { return "synthetic", nil }, DeliverHandoff: func(context.Context, []byte) error { deliveries++; return nil }, HTTPClient: offlineDoer(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/migration/v1/machines/availability":
					availability++
					if scenario == "stop_during_availability" {
						close(started)
						<-release
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"version":"v1","server_type":"cx22","locations":["fsn1"]}`))}, nil
				case "/migration/v1/machines/create":
					creates++
					if scenario == "lost_response" {
						return nil, errors.New("synthetic lost response")
					}
					body := `{"version":"v1","state":"created","attempt":"` + strings.Repeat("a", 64) + `","server_label":"goaster-migration-` + strings.Repeat("a", 16) + `","server_id":123,"ip":"198.51.100.10","owner_url":"https://example.invalid/migration#attempt=` + strings.Repeat("a", 64) + `","expires":1800003600}`
					return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(body))}, nil
				default:
					return nil, errors.New("unexpected request")
				}
			})}
			var jobs *scheduler.Scheduler
			var err error
			jobs, err = scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json"), Now: func() time.Time { return now }, Executor: func(ctx context.Context, msg string) error { return ScheduledExecutor(ctx, msg, jobs, options) }})
			if err != nil {
				t.Fatal(err)
			}
			req := Request{ServerType: "cx22", Locations: []string{"fsn1"}, JobID: "migration-due"}
			msg, err := schedulerMessage(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := jobs.Add(scheduler.Job{ID: req.JobID, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds, Message: msg}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Duration(MonitorIntervalSeconds) * time.Second)
			done := make(chan int, 1)
			go func() { done <- jobs.RunDue(t.Context()) }()
			if scenario == "stop_during_availability" {
				<-started
				if _, err := StopMonitor(req.JobID, jobs); !errors.Is(err, scheduler.ErrExecutionInProgress) {
					t.Fatalf("stop: %v", err)
				}
				close(release)
			}
			if runs := <-done; runs != 1 {
				t.Fatalf("runs=%d", runs)
			}
			job, ok := jobs.Get(req.JobID)
			if !ok || job.Enabled || job.ExecutionClaim || availability != 1 || creates != 1 {
				t.Fatalf("job=%+v availability=%d creates=%d", job, availability, creates)
			}
			if scenario == "lost_response" {
				if !job.ReviewRequired || deliveries != 0 {
					t.Fatalf("lost response job=%+v deliveries=%d", job, deliveries)
				}
			} else if job.ReviewRequired || deliveries != 1 {
				t.Fatalf("job=%+v deliveries=%d", job, deliveries)
			}
			now = now.Add(time.Hour)
			if jobs.RunDue(t.Context()) != 0 || creates != 1 {
				t.Fatal("job repeated")
			}
		})
	}
}
