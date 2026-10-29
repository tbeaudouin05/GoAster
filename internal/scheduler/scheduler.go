package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ScheduleAt    = "at"
	ScheduleEvery = "every"
	ScheduleCron  = "cron"

	StatusScheduled = "scheduled"
	StatusRunning   = "running"
	StatusSuccess   = "success"
	StatusFailed    = "failed"
	StatusCompleted = "completed"
	StatusDisabled  = "disabled"

	storeVersion      = 1
	maxJobsFileBytes  = 4 << 20
	maxJobErrorBytes  = 4 << 10
	maxEverySeconds   = int64((1<<63 - 1) / int64(time.Second))
	cronDirectoryName = ".goaster"
	cronSubdirectory  = "cron"
	cronFilename      = "jobs.json"
)

var (
	ErrJobNotFound    = errors.New("cron job not found")
	ErrAlreadyStarted = errors.New("cron scheduler is already running")
	jobIDCounter      atomic.Uint64
)

// Executor is called with the saved message when a job becomes due. The
// scheduler never interprets the message as a shell command.
type Executor func(context.Context, string) error

// Schedule describes one supported schedule kind. AtSeconds is a positive
// delay from now; EverySeconds is a recurring interval; CronExpr is a
// five-field cron expression (or a six-field expression that includes
// seconds).
type Schedule struct {
	Kind         string
	AtSeconds    int64
	EverySeconds int64
	CronExpr     string
}

// Job is the persisted representation of a scheduled chat turn.
type Job struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	AtSeconds      int64      `json:"at_seconds,omitempty"`
	EverySeconds   int64      `json:"every_seconds,omitempty"`
	CronExpr       string     `json:"cron_expr,omitempty"`
	Message        string     `json:"message"`
	Enabled        bool       `json:"enabled"`
	NextRun        *time.Time `json:"next_run,omitempty"`
	LastRun        *time.Time `json:"last_run,omitempty"`
	Status         string     `json:"status"`
	Error          string     `json:"error,omitempty"`
	RunCount       int        `json:"run_count"`
	MaxRuns        int        `json:"max_runs,omitempty"`
	DeleteAfterRun bool       `json:"delete_after_run,omitempty"`
}

func (job Job) schedule() Schedule {
	return Schedule{
		Kind:         job.Kind,
		AtSeconds:    job.AtSeconds,
		EverySeconds: job.EverySeconds,
		CronExpr:     job.CronExpr,
	}
}

func (job Job) Schedule() Schedule {
	return job.schedule()
}

// Patch contains only fields that should change. Nil fields are deliberately
// omitted so model updates preserve the rest of the job.
type Patch struct {
	AtSeconds      *int64
	EverySeconds   *int64
	CronExpr       *string
	Message        *string
	MaxRuns        *int
	DeleteAfterRun *bool
}

// Options configures a scheduler. An empty Path selects ~/.goaster/cron/jobs.json.
type Options struct {
	Path     string
	Executor Executor
	Now      func() time.Time
}

// Store persists jobs in one private JSON file.
type Store struct {
	Path string
}

type diskState struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

// NewStore creates a store. An empty path selects the GoAster default path.
func NewStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory: %w", err)
		}
		path = filepath.Join(home, cronDirectoryName, cronSubdirectory, cronFilename)
	}
	return &Store{Path: path}, nil
}

func (s *Store) Load() ([]Job, error) {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return nil, fmt.Errorf("cron store path is empty")
	}
	info, err := os.Lstat(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return []Job{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect cron jobs file: %w", err)
	}
	if err := validateJobsFileInfo(info); err != nil {
		return nil, err
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read cron jobs file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened cron jobs file: %w", err)
	}
	pathInfo, err := os.Lstat(s.Path)
	if err != nil {
		return nil, fmt.Errorf("inspect cron jobs file: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return nil, fmt.Errorf("cron jobs file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJobsFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read cron jobs file: %w", err)
	}
	if len(data) > maxJobsFileBytes {
		return nil, fmt.Errorf("cron jobs file exceeds the %d-byte limit", maxJobsFileBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var disk diskState
	if err := decoder.Decode(&disk); err != nil {
		return nil, fmt.Errorf("parse cron jobs file: %w", err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parse cron jobs file: trailing data")
	}
	if disk.Version != storeVersion {
		return nil, fmt.Errorf("unsupported cron jobs file version %d", disk.Version)
	}
	seen := make(map[string]struct{}, len(disk.Jobs))
	for _, job := range disk.Jobs {
		if _, ok := seen[job.ID]; ok {
			return nil, fmt.Errorf("cron jobs file contains duplicate job ID %q", job.ID)
		}
		seen[job.ID] = struct{}{}
		if err := validateJob(job); err != nil {
			return nil, fmt.Errorf("invalid cron job %q: %w", job.ID, err)
		}
	}
	return disk.Jobs, nil
}

func (s *Store) Save(jobs []Job) error {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("cron store path is empty")
	}
	for _, job := range jobs {
		if err := validateJob(job); err != nil {
			return fmt.Errorf("cannot save cron job %q: %w", job.ID, err)
		}
	}
	directory := filepath.Dir(s.Path)
	if directory != "." {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create cron directory: %w", err)
		}
		if err := os.Chmod(directory, 0700); err != nil {
			return fmt.Errorf("restrict cron directory: %w", err)
		}
	}
	data, err := json.MarshalIndent(diskState{Version: storeVersion, Jobs: jobs}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cron jobs: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".jobs-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary cron jobs file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary cron jobs file: %w", err)
	}
	n, err := temporary.Write(data)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write cron jobs: %w", err)
	}
	if n != len(data) {
		_ = temporary.Close()
		return fmt.Errorf("write cron jobs: incomplete write")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync cron jobs: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary cron jobs file: %w", err)
	}
	if info, err := os.Lstat(s.Path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cron jobs file must not be a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect cron jobs file before replacement: %w", err)
	}
	if err := os.Rename(temporaryName, s.Path); err != nil {
		return fmt.Errorf("replace cron jobs: %w", err)
	}
	if err := os.Chmod(s.Path, 0600); err != nil {
		return fmt.Errorf("restrict cron jobs file: %w", err)
	}
	syncDirectory(directory)
	return nil
}

func validateJobsFileInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cron jobs file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cron jobs path is not a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("cron jobs file permissions are too broad: %04o (want 0600)", info.Mode().Perm())
	}
	return nil
}

func validateJob(job Job) error {
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("job ID is required")
	}
	if strings.TrimSpace(job.Message) == "" {
		return fmt.Errorf("job message is required")
	}
	if job.RunCount < 0 {
		return fmt.Errorf("run count must not be negative")
	}
	if job.MaxRuns < 0 {
		return fmt.Errorf("max runs must not be negative")
	}
	if job.MaxRuns > 0 && job.RunCount > job.MaxRuns {
		return fmt.Errorf("run count exceeds max runs")
	}
	return validateSchedule(job.schedule())
}

func validateSchedule(schedule Schedule) error {
	switch schedule.Kind {
	case ScheduleAt:
		if schedule.AtSeconds <= 0 {
			return fmt.Errorf("at_seconds must be positive")
		}
		if schedule.AtSeconds > maxEverySeconds {
			return fmt.Errorf("at_seconds is too large")
		}
	case ScheduleEvery:
		if schedule.EverySeconds <= 0 {
			return fmt.Errorf("every_seconds must be positive")
		}
		if schedule.EverySeconds > maxEverySeconds {
			return fmt.Errorf("every_seconds is too large")
		}
	case ScheduleCron:
		if _, err := parseCronExpression(schedule.CronExpr); err != nil {
			return err
		}
	default:
		return fmt.Errorf("schedule kind must be %q, %q, or %q", ScheduleAt, ScheduleEvery, ScheduleCron)
	}
	return nil
}

// NextRun computes the next run after after. At schedules run after the
// requested number of seconds; recurring schedules run after their interval
// or at the next cron expression match.
func NextRun(schedule Schedule, after time.Time) (time.Time, error) {
	if err := validateSchedule(schedule); err != nil {
		return time.Time{}, err
	}
	switch schedule.Kind {
	case ScheduleAt:
		return after.Add(time.Duration(schedule.AtSeconds) * time.Second), nil
	case ScheduleEvery:
		return after.Add(time.Duration(schedule.EverySeconds) * time.Second), nil
	case ScheduleCron:
		return nextCron(schedule.CronExpr, after)
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule kind %q", schedule.Kind)
	}
}

func initialNextRun(schedule Schedule, now time.Time) (*time.Time, error) {
	next, err := NextRun(schedule, now)
	if err != nil {
		return nil, err
	}
	return &next, nil
}

type Scheduler struct {
	mu       sync.Mutex
	store    *Store
	executor Executor
	now      func() time.Time
	jobs     map[string]Job
	wake     chan struct{}
	started  bool
	cancel   context.CancelFunc
	done     chan struct{}
}

// New loads the persisted scheduler state.
func New(options Options) (*Scheduler, error) {
	store, err := NewStore(options.Path)
	if err != nil {
		return nil, err
	}
	jobs, err := store.Load()
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	scheduler := &Scheduler{
		store:    store,
		executor: options.Executor,
		now:      now,
		jobs:     make(map[string]Job, len(jobs)),
		wake:     make(chan struct{}, 1),
	}
	changed := false
	for _, job := range jobs {
		if _, exists := scheduler.jobs[job.ID]; exists {
			return nil, fmt.Errorf("duplicate cron job ID %q", job.ID)
		}
		if job.Status == "" {
			if job.Enabled {
				job.Status = StatusScheduled
			} else {
				job.Status = StatusDisabled
			}
			changed = true
		}
		if job.Status == StatusRunning {
			job.Status = StatusFailed
			job.Error = "previous run was interrupted"
			changed = true
		}
		if job.Kind == ScheduleAt && !job.DeleteAfterRun {
			job.DeleteAfterRun = true
			changed = true
		}
		scheduler.jobs[job.ID] = job
	}
	if changed {
		if err := scheduler.saveLocked(); err != nil {
			return nil, fmt.Errorf("normalize cron jobs: %w", err)
		}
	}
	return scheduler, nil
}

func (s *Scheduler) Path() string {
	if s == nil || s.store == nil {
		return ""
	}
	return s.store.Path
}

func (s *Scheduler) Add(job Job) (Job, error) {
	if s == nil {
		return Job{}, fmt.Errorf("cron scheduler is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.Message = strings.TrimSpace(job.Message)
	if job.Message == "" {
		return Job{}, fmt.Errorf("job message is required")
	}
	if job.ID == "" {
		job.ID = newJobID(s.now())
		for {
			if _, exists := s.jobs[job.ID]; !exists {
				break
			}
			job.ID = newJobID(s.now())
		}
	}
	if _, exists := s.jobs[job.ID]; exists {
		return Job{}, fmt.Errorf("cron job %q already exists", job.ID)
	}
	job.Enabled = true
	job.Status = StatusScheduled
	job.Error = ""
	job.RunCount = 0
	if job.Kind == ScheduleAt {
		job.DeleteAfterRun = true
		job.NextRun = nil
	}
	if err := validateJob(job); err != nil {
		return Job{}, err
	}
	if job.NextRun == nil {
		next, err := initialNextRun(job.schedule(), s.now())
		if err != nil {
			return Job{}, err
		}
		job.NextRun = next
	}
	s.jobs[job.ID] = job
	if err := s.saveLocked(); err != nil {
		delete(s.jobs, job.ID)
		return Job{}, err
	}
	s.signalWake()
	return cloneJob(job), nil
}

func (s *Scheduler) List() []Job {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, cloneJob(job))
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].NextRun == nil && jobs[j].NextRun != nil {
			return false
		}
		if jobs[i].NextRun != nil && jobs[j].NextRun == nil {
			return true
		}
		if jobs[i].NextRun != nil && jobs[j].NextRun != nil && !jobs[i].NextRun.Equal(*jobs[j].NextRun) {
			return jobs[i].NextRun.Before(*jobs[j].NextRun)
		}
		return jobs[i].ID < jobs[j].ID
	})
	return jobs
}

func (s *Scheduler) Get(id string) (Job, bool) {
	if s == nil {
		return Job{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[strings.TrimSpace(id)]
	if !ok {
		return Job{}, false
	}
	return cloneJob(job), true
}

func (s *Scheduler) Update(id string, patch Patch) (Job, error) {
	if s == nil {
		return Job{}, fmt.Errorf("cron scheduler is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	updated := current
	scheduleFields := 0
	if patch.AtSeconds != nil {
		scheduleFields++
	}
	if patch.EverySeconds != nil {
		scheduleFields++
	}
	if patch.CronExpr != nil {
		scheduleFields++
	}
	if scheduleFields > 1 {
		return Job{}, fmt.Errorf("update must contain at most one schedule field")
	}
	scheduleChanged := scheduleFields == 1
	if patch.Message != nil {
		updated.Message = strings.TrimSpace(*patch.Message)
	}
	if patch.MaxRuns != nil {
		updated.MaxRuns = *patch.MaxRuns
	}
	if patch.DeleteAfterRun != nil {
		updated.DeleteAfterRun = *patch.DeleteAfterRun
	}
	if scheduleChanged {
		updated.AtSeconds = 0
		updated.EverySeconds = 0
		updated.CronExpr = ""
		switch {
		case patch.AtSeconds != nil:
			updated.Kind = ScheduleAt
			updated.AtSeconds = *patch.AtSeconds
			if patch.DeleteAfterRun == nil {
				updated.DeleteAfterRun = true
			}
		case patch.EverySeconds != nil:
			updated.Kind = ScheduleEvery
			updated.EverySeconds = *patch.EverySeconds
		case patch.CronExpr != nil:
			updated.Kind = ScheduleCron
			updated.CronExpr = strings.TrimSpace(*patch.CronExpr)
		}
	}
	if err := validateJob(updated); err != nil {
		return Job{}, err
	}
	if scheduleChanged {
		updated.NextRun = nil
		if updated.Enabled {
			next, err := initialNextRun(updated.schedule(), s.now())
			if err != nil {
				return Job{}, err
			}
			updated.NextRun = next
		}
		updated.Error = ""
		if updated.Enabled {
			updated.Status = StatusScheduled
		}
	}
	if patch.MaxRuns != nil && updated.MaxRuns > 0 && updated.RunCount >= updated.MaxRuns {
		updated.Enabled = false
		updated.NextRun = nil
		if updated.Status != StatusFailed {
			updated.Status = StatusCompleted
		}
	}
	if patch.MaxRuns != nil && updated.MaxRuns > 0 && updated.RunCount < updated.MaxRuns && current.Status == StatusCompleted && !scheduleChanged {
		updated.Enabled = true
		updated.Status = StatusScheduled
		if updated.NextRun == nil {
			next, err := initialNextRun(updated.schedule(), s.now())
			if err != nil {
				return Job{}, err
			}
			updated.NextRun = next
		}
	}
	if updated.Message == "" {
		return Job{}, fmt.Errorf("job message is required")
	}
	s.jobs[id] = updated
	if err := s.saveLocked(); err != nil {
		s.jobs[id] = current
		return Job{}, err
	}
	s.signalWake()
	return cloneJob(updated), nil
}

func (s *Scheduler) Remove(id string) error {
	if s == nil {
		return fmt.Errorf("cron scheduler is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	delete(s.jobs, id)
	if err := s.saveLocked(); err != nil {
		s.jobs[id] = job
		return err
	}
	s.signalWake()
	return nil
}

func (s *Scheduler) SetEnabled(id string, enabled bool) (Job, error) {
	if s == nil {
		return Job{}, fmt.Errorf("cron scheduler is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if enabled {
		if job.MaxRuns > 0 && job.RunCount >= job.MaxRuns {
			return Job{}, fmt.Errorf("cron job %q has reached max_runs", id)
		}
		job.Enabled = true
		job.Status = StatusScheduled
		job.Error = ""
		if job.NextRun == nil {
			next, err := initialNextRun(job.schedule(), s.now())
			if err != nil {
				return Job{}, err
			}
			job.NextRun = next
		}
	} else {
		job.Enabled = false
		job.Status = StatusDisabled
	}
	previous := s.jobs[id]
	s.jobs[id] = job
	if err := s.saveLocked(); err != nil {
		s.jobs[id] = previous
		return Job{}, err
	}
	s.signalWake()
	return cloneJob(job), nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("cron scheduler is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return ErrAlreadyStarted
	}
	loopContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.started = true
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()
	go s.loop(loopContext, done)
	return nil
}

func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	cancel()
	s.signalWake()
	<-done
}

// RunDue executes all jobs that are due at the scheduler's current clock. It
// is useful for deterministic callers and is also the unit the timer loop uses.
func (s *Scheduler) RunDue(ctx context.Context) int {
	if s == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runs := 0
	for ctx.Err() == nil {
		job, ok := s.claimDue()
		if !ok {
			return runs
		}
		s.executeClaim(ctx, job)
		runs++
	}
	return runs
}

func (s *Scheduler) loop(ctx context.Context, done chan struct{}) {
	defer func() {
		close(done)
		s.mu.Lock()
		if s.done == done {
			s.started = false
			s.cancel = nil
			s.done = nil
		}
		s.mu.Unlock()
	}()
	for ctx.Err() == nil {
		if s.RunDue(ctx) > 0 {
			continue
		}
		wait, ok := s.nextWait()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		if wait <= 0 {
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-s.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (s *Scheduler) nextWait() (time.Duration, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var earliest *time.Time
	for _, job := range s.jobs {
		if !job.Enabled || job.NextRun == nil {
			continue
		}
		if earliest == nil || job.NextRun.Before(*earliest) {
			candidate := *job.NextRun
			earliest = &candidate
		}
	}
	if earliest == nil {
		return 0, false
	}
	return earliest.Sub(now), true
}

func (s *Scheduler) claimDue() (Job, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var selected Job
	found := false
	for _, job := range s.jobs {
		if !job.Enabled || job.Status == StatusRunning || job.NextRun == nil || job.NextRun.After(now) {
			continue
		}
		if !found || job.NextRun.Before(*selected.NextRun) || (job.NextRun.Equal(*selected.NextRun) && job.ID < selected.ID) {
			selected = job
			found = true
		}
	}
	if !found {
		return Job{}, false
	}
	previous := selected
	selected.Status = StatusRunning
	selected.Error = ""
	selected.LastRun = timePointer(now)
	s.jobs[selected.ID] = selected
	if err := s.saveLocked(); err != nil {
		s.jobs[selected.ID] = previous
		return Job{}, false
	}
	return cloneJob(selected), true
}

func (s *Scheduler) executeClaim(ctx context.Context, claimed Job) {
	var executionErr error
	if s.executor == nil {
		executionErr = errors.New("cron scheduler executor is nil")
	} else {
		executionErr = s.executor(ctx, claimed.Message)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[claimed.ID]
	if !ok {
		return
	}
	previous := current
	current.RunCount++
	current.LastRun = claimed.LastRun
	if current.DeleteAfterRun {
		delete(s.jobs, current.ID)
		if err := s.saveLocked(); err != nil {
			s.jobs[current.ID] = previous
		}
		return
	}
	if executionErr != nil {
		current.Status = StatusFailed
		current.Error = formatJobError(executionErr)
	} else {
		current.Status = StatusSuccess
		current.Error = ""
	}
	if current.MaxRuns > 0 && current.RunCount >= current.MaxRuns {
		current.Enabled = false
		current.NextRun = nil
		if executionErr == nil {
			current.Status = StatusCompleted
		}
	} else if current.Kind == ScheduleAt {
		current.Enabled = false
		current.NextRun = nil
		if executionErr == nil {
			current.Status = StatusCompleted
		}
	} else if !current.Enabled {
		current.Status = StatusDisabled
	} else if !sameSchedule(current, claimed) {
		// A model call may update this job while its message is executing. Keep
		// the new schedule and only apply this invocation's run metadata.
		if current.NextRun == nil {
			next, err := initialNextRun(current.schedule(), s.now())
			if err == nil {
				current.NextRun = next
			}
		}
	} else {
		next, err := NextRun(current.schedule(), s.now())
		if err != nil {
			current.Enabled = false
			current.NextRun = nil
			current.Status = StatusFailed
			current.Error = formatJobError(err)
		} else {
			current.NextRun = timePointer(next)
		}
	}
	s.jobs[current.ID] = current
	if err := s.saveLocked(); err != nil {
		s.jobs[current.ID] = previous
	}
}

func (s *Scheduler) saveLocked() error {
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return s.store.Save(jobs)
}

func (s *Scheduler) signalWake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func newJobID(now time.Time) string {
	return fmt.Sprintf("job-%d-%d", now.UnixNano(), jobIDCounter.Add(1))
}

func cloneJob(job Job) Job {
	if job.NextRun != nil {
		next := *job.NextRun
		job.NextRun = &next
	}
	if job.LastRun != nil {
		last := *job.LastRun
		job.LastRun = &last
	}
	return job
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func sameSchedule(a, b Job) bool {
	return a.Kind == b.Kind &&
		a.AtSeconds == b.AtSeconds &&
		a.EverySeconds == b.EverySeconds &&
		a.CronExpr == b.CronExpr
}

func formatJobError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > maxJobErrorBytes {
		message = message[:maxJobErrorBytes]
	}
	return message
}

func syncDirectory(directory string) {
	if directory == "." {
		return
	}
	file, err := os.Open(directory)
	if err != nil {
		return
	}
	_ = file.Sync()
	_ = file.Close()
}
