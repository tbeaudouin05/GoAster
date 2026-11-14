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

	"goaster/internal/artefacts"
	"goaster/internal/filesync"
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

	storeVersion     = 1
	maxJobsFileBytes = 4 << 20
	maxJobErrorBytes = 4 << 10
	maxEverySeconds  = int64((1<<63 - 1) / int64(time.Second))
	cronSubdirectory = "cron"
	cronFilename     = "jobs.json"
)

var (
	ErrJobNotFound    = errors.New("cron job not found")
	ErrAlreadyStarted = errors.New("cron scheduler is already running")
	// ErrExecutionClaimUnavailable means an action must not start because the
	// job is disabled, under review, or already owned by another execution.
	ErrExecutionClaimUnavailable = errors.New("cron job execution claim unavailable")
	// ErrExecutionInProgress means a stop cannot report success while an
	// execution claim is active.
	ErrExecutionInProgress = errors.New("cron job execution is in progress")
	// ErrRetry means no actions started and the job can be retried safely.
	ErrRetry = errors.New("cron job should retry")
	// ErrReviewRequired means execution may have performed actions and must be reviewed.
	ErrReviewRequired = errors.New("cron job requires manual review")
	jobIDCounter      atomic.Uint64
)

// ReviewRequiredError carries safe guidance for a run that must not be replayed automatically.
type ReviewRequiredError struct {
	Message string
	Cause   error
}

func (err ReviewRequiredError) Error() string    { return err.Message }
func (err ReviewRequiredError) Unwrap() error    { return err.Cause }
func (ReviewRequiredError) Is(target error) bool { return target == ErrReviewRequired }

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
	ReviewRequired bool       `json:"review_required,omitempty"`
	ExecutionClaim bool       `json:"execution_claim,omitempty"`
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

// Options configures a scheduler. An empty Path selects
// ~/.goaster/artefacts/state/cron/jobs.json.
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
		root, err := artefacts.Root()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(root, "state", cronSubdirectory, cronFilename)
	}
	return &Store{Path: path}, nil
}

func (s *Store) Load() ([]Job, error) {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return nil, fmt.Errorf("cron store path is empty")
	}
	if err := validateCronDirectory(filepath.Dir(s.Path), false); err != nil {
		return nil, err
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
	if err := validateCronDirectory(directory, true); err != nil {
		return err
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

// Validate directory components before looking for or writing jobs.json.
func validateCronDirectory(directory string, create bool) error {
	if directory == "." {
		return nil
	}
	root, rootErr := artefacts.Root()
	if rootErr == nil && filepath.Clean(directory) == filepath.Join(root, "state", cronSubdirectory) {
		if create {
			_, err := artefacts.EnsureRuntimeDirectory("state", cronSubdirectory)
			return err
		}
		for _, dir := range []string{filepath.Dir(root), root, filepath.Join(root, "state"), directory} {
			info, err := os.Lstat(dir)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("cron directory must be a real directory: %s", dir)
			}
		}
		return nil
	}
	checkParents := func() error {
		for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect cron directory %s: %w", current, err)
			}
			if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return fmt.Errorf("cron directory must be a real directory: %s", current)
			}
			if parent := filepath.Dir(current); parent == current {
				break
			}
		}
		return nil
	}
	if err := checkParents(); err != nil {
		return err
	}
	if create {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create cron directory: %w", err)
		}
		if err := checkParents(); err != nil {
			return err
		}
		return os.Chmod(directory, 0700)
	}
	return nil
}

// The state lock serializes read-modify-write transactions. The run lock is
// separate so a job may manage cron jobs while its executor is running.
func (s *Store) lock(run bool) (func(), error) {
	if err := validateCronDirectory(filepath.Dir(s.Path), true); err != nil {
		return nil, err
	}
	path := s.Path
	if run {
		path += ".run"
	}
	path += ".lock"
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("cron lock must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		_ = file.Close()
		return nil, errors.New("cron lock changed while it was opened")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := filesync.LockFile(file)
		if err == nil {
			break
		}
		if run || !filesync.IsLockBusy(err) || time.Now().After(deadline) {
			_ = file.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() { _ = file.Close() }, nil
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
	saveJobs func([]Job) error // Optional seam for persistence failures.
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
	// A live executor owns the run lock. Only its absence makes a persisted
	// running status evidence of an interrupted run.
	unlockRun, runErr := store.lock(true)
	if runErr != nil && !filesync.IsLockBusy(runErr) {
		return nil, fmt.Errorf("lock cron execution: %w", runErr)
	}
	if runErr == nil {
		defer unlockRun()
	}
	unlockState, err := store.lock(false)
	if err != nil {
		return nil, fmt.Errorf("lock cron state: %w", err)
	}
	defer unlockState()
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
		if job.Status == StatusRunning && runErr == nil {
			job.Status = StatusFailed
			job.Error = "previous run was interrupted"
			job.Enabled = false
			job.NextRun = nil
			job.ReviewRequired = true
			job.ExecutionClaim = false
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
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
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
	job.ExecutionClaim = false
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
	if unlock, err := s.reloadLocked(); err == nil {
		unlock()
	}
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
	if unlock, err := s.reloadLocked(); err == nil {
		unlock()
	}
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
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	current, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if current.ExecutionClaim {
		return Job{}, ErrExecutionInProgress
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
	// A running claim has not yet been included in RunCount. Reserve room for
	// it so its eventual result can be saved without discarding this edit or
	// violating the persisted run-count limit.
	if current.Status == StatusRunning && patch.MaxRuns != nil && updated.MaxRuns > 0 && updated.RunCount >= updated.MaxRuns {
		return Job{}, fmt.Errorf("cron job %q is running; max_runs must be 0 or greater than run_count until the run finishes", id)
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
		if !updated.ReviewRequired {
			updated.Error = ""
		}
		if updated.Enabled && current.Status != StatusRunning {
			updated.Status = StatusScheduled
		}
	}
	if patch.MaxRuns != nil && updated.MaxRuns > 0 && updated.RunCount >= updated.MaxRuns {
		updated.Enabled = false
		updated.NextRun = nil
		if updated.Status != StatusFailed && !updated.ReviewRequired {
			updated.Status = StatusCompleted
		}
	}
	if patch.MaxRuns != nil && updated.MaxRuns > 0 && updated.RunCount < updated.MaxRuns && current.Status == StatusCompleted && !scheduleChanged && !current.ReviewRequired {
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
	unlock, err := s.reloadLocked()
	if err != nil {
		return err
	}
	defer unlock()
	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if job.Status == StatusRunning {
		return fmt.Errorf("cron job %q is running; wait for the run to finish before removing it", id)
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
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if job.ExecutionClaim {
		return Job{}, fmt.Errorf("%w: execution claim is active", ErrExecutionInProgress)
	}
	if enabled {
		if job.MaxRuns > 0 && job.RunCount >= job.MaxRuns {
			return Job{}, fmt.Errorf("cron job %q has reached max_runs; increase max_runs above run_count or set it to 0 before enabling", id)
		}
		job.Enabled = true
		if job.Status != StatusRunning {
			job.Status = StatusScheduled
		}
		job.Error = ""
		job.ReviewRequired = false
		if job.NextRun == nil {
			next, err := initialNextRun(job.schedule(), s.now())
			if err != nil {
				return Job{}, err
			}
			job.NextRun = next
		}
	} else {
		job.Enabled = false
		if job.Status != StatusRunning {
			job.Status = StatusDisabled
		}
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

// executionOwner is available only to the currently running scheduler executor.
type executionOwnerKey struct{}
type executionOwner struct {
	scheduler chan struct{}
	id        string
	state     chan bool
}

// ClaimExecution reserves a job and returns a completion function. A direct
// claim holds the run lock until completion; a scheduled claim must carry the
// context of the executor that owns this exact job.
func (s *Scheduler) ClaimExecution(ctx context.Context, id string) (Job, func(error) (Job, error), error) {
	return s.claimExecution(ctx, id, Job{}, false)
}

// ClaimExecutionMatching atomically verifies the expected message and schedule before claiming.
func (s *Scheduler) ClaimExecutionMatching(ctx context.Context, expected Job) (Job, func(error) (Job, error), error) {
	return s.claimExecution(ctx, expected.ID, expected, true)
}

func (s *Scheduler) claimExecution(ctx context.Context, id string, expected Job, match bool) (Job, func(error) (Job, error), error) {
	if s == nil {
		return Job{}, nil, fmt.Errorf("cron scheduler is nil")
	}
	id = strings.TrimSpace(id)
	var owner executionOwner
	var scheduled bool
	if ctx != nil {
		owner, scheduled = ctx.Value(executionOwnerKey{}).(executionOwner)
	}
	var unlockRun func()
	successOwner := false
	if scheduled {
		used := <-owner.state
		defer func() { owner.state <- used }()
		if used || owner.scheduler != s.wake || owner.id != id {
			return Job{}, nil, ErrExecutionClaimUnavailable
		}
		defer func() {
			if successOwner {
				used = true
			}
		}()
	} else {
		var err error
		unlockRun, err = s.store.lock(true)
		if err != nil {
			return Job{}, nil, fmt.Errorf("%w: %w", ErrExecutionClaimUnavailable, err)
		}
	}
	success := false
	defer func() {
		if !success && unlockRun != nil {
			unlockRun()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, nil, err
	}
	defer unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, nil, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if job.ReviewRequired || !job.Enabled || (match && (job.Message != expected.Message || !sameSchedule(job, expected))) || (!scheduled && job.Status != StatusScheduled) || (scheduled && job.Status != StatusRunning) {
		return Job{}, nil, ErrExecutionClaimUnavailable
	}
	previous := job
	job.Status = StatusRunning
	job.ExecutionClaim = true
	job.Enabled = false
	job.NextRun = nil
	job.Error = ""
	s.jobs[id] = job
	if err := s.saveLocked(); err != nil {
		s.jobs[id] = previous
		return Job{}, nil, err
	}
	s.signalWake()
	success = true
	if scheduled {
		successOwner = true
		return cloneJob(job), nil, nil
	}
	var once sync.Once
	complete := func(executionErr error) (Job, error) {
		var completed Job
		resultErr := ErrExecutionClaimUnavailable
		once.Do(func() { defer unlockRun(); completed, resultErr = s.completeExecution(id, executionErr) })
		return completed, resultErr
	}
	return cloneJob(job), complete, nil
}

// StopIfNotRunning atomically disables a job only when no action owns its
// durable execution claim. It is deliberately separate from SetEnabled,
// which permits changes to ordinary running jobs without such a claim.
func (s *Scheduler) StopIfNotRunning(id string) (Job, error) {
	if s == nil {
		return Job{}, fmt.Errorf("cron scheduler is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if job.Status == StatusRunning {
		return Job{}, fmt.Errorf("%w: stop cannot be confirmed", ErrExecutionInProgress)
	}
	previous := job
	job.Enabled = false
	job.NextRun = nil
	if !job.ReviewRequired {
		job.Status = StatusDisabled
	}
	s.jobs[id] = job
	if err := s.saveLocked(); err != nil {
		s.jobs[id] = previous
		return Job{}, err
	}
	s.signalWake()
	return cloneJob(job), nil
}

// completeExecution finalizes a direct claim while its run lock is held.
func (s *Scheduler) completeExecution(id string, executionErr error) (Job, error) {
	if s == nil {
		return Job{}, fmt.Errorf("cron scheduler is nil")
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}
	if job.Status != StatusRunning || job.Enabled || !job.ExecutionClaim {
		return Job{}, fmt.Errorf("%w: job is not a direct execution claim", ErrExecutionClaimUnavailable)
	}
	previous := job
	job.ExecutionClaim = false
	job.Enabled = false
	job.NextRun = nil
	if errors.Is(executionErr, ErrRetry) && !errors.Is(executionErr, ErrReviewRequired) {
		next, err := initialNextRun(job.schedule(), s.now())
		if err != nil {
			return Job{}, err
		}
		job.Enabled = true
		job.NextRun = next
		job.Status = StatusScheduled
		job.Error = ""
	} else if executionErr != nil {
		job.RunCount++
		job.Status = StatusFailed
		job.Error = formatJobError(executionErr)
		job.ReviewRequired = true
	} else {
		job.RunCount++
		job.Status = StatusDisabled
		job.Error = ""
		job.ReviewRequired = false
	}
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
	unlockRun, err := s.store.lock(true)
	if err != nil {
		return 0
	}
	defer unlockRun()
	// An earlier owner may have died after an action started. Do not repeat
	// that action automatically; require an explicit re-enable.
	s.mu.Lock()
	unlockState, err := s.reloadLocked()
	if err != nil {
		s.mu.Unlock()
		return 0
	}
	changed := false
	for id, job := range s.jobs {
		if job.Status == StatusRunning {
			job.Status = StatusFailed
			job.Error = "previous run was interrupted"
			job.Enabled = false
			job.NextRun = nil
			job.ReviewRequired = true
			job.ExecutionClaim = false
			s.jobs[id] = job
			changed = true
		}
	}
	if changed {
		err = s.saveLocked()
	}
	unlockState()
	s.mu.Unlock()
	if err != nil {
		return 0
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
			wait = 5 * time.Second
		}
		if wait <= 0 || wait > 5*time.Second {
			// A different process or a failed state write may prevent a claim.
			// Poll for its changes and back off if a claim cannot be saved.
			wait = 5 * time.Second
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
		if !job.Enabled || job.Status == StatusRunning || job.NextRun == nil {
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
	unlock, err := s.reloadLocked()
	if err != nil {
		return Job{}, false
	}
	defer unlock()
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
	s.jobs[selected.ID] = selected
	if err := s.saveLocked(); err != nil {
		s.jobs[selected.ID] = previous
		return Job{}, false
	}
	return cloneJob(selected), true
}

func (s *Scheduler) executeClaim(ctx context.Context, claimed Job) {
	started := s.now()
	var executionErr error
	owned := false
	if s.executor == nil {
		executionErr = errors.New("cron scheduler executor is nil")
	} else {
		owner := executionOwner{scheduler: s.wake, id: claimed.ID, state: make(chan bool, 1)}
		owner.state <- false
		executionErr = s.executor(context.WithValue(ctx, executionOwnerKey{}, owner), claimed.Message)
		owned = <-owner.state
		owner.state <- true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.reloadLocked()
	if err != nil {
		return
	}
	defer unlock()
	current, ok := s.jobs[claimed.ID]
	if !ok {
		return
	}
	previous := current
	current.ExecutionClaim = false
	if errors.Is(executionErr, ErrRetry) && !errors.Is(executionErr, ErrReviewRequired) {
		if owned {
			current.Enabled = true
		}
		if current.Enabled {
			current.Status = StatusScheduled
			if sameSchedule(current, claimed) {
				if owned {
					next, err := initialNextRun(current.schedule(), s.now())
					if err != nil {
						return
					}
					current.NextRun = next
				} else {
					current.NextRun = timePointer(s.now().Add(5 * time.Second))
				}
			}
		} else {
			current.Status = StatusDisabled
		}
		current.Error = ""
		s.jobs[current.ID] = current
		if err := s.saveLocked(); err != nil {
			s.jobs[current.ID] = previous
		}
		return
	}
	current.RunCount++
	current.LastRun = timePointer(started)
	if errors.Is(executionErr, ErrReviewRequired) {
		current.Status = StatusFailed
		current.Error = formatJobError(executionErr)
		current.Enabled = false
		current.NextRun = nil
		current.ReviewRequired = true
		s.jobs[current.ID] = current
		if err := s.saveLocked(); err != nil {
			s.jobs[current.ID] = previous
		}
		return
	}
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

// reloadLocked returns with the cross-process state lock held. Callers must
// release it after completing their transaction and while still holding mu.
func (s *Scheduler) reloadLocked() (func(), error) {
	unlock, err := s.store.lock(false)
	if err != nil {
		return nil, fmt.Errorf("lock cron state: %w", err)
	}
	jobs, err := s.store.Load()
	if err != nil {
		unlock()
		return nil, err
	}
	s.jobs = make(map[string]Job, len(jobs))
	for _, job := range jobs {
		s.jobs[job.ID] = job
	}
	return unlock, nil
}

func (s *Scheduler) saveLocked() error {
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	if s.saveJobs != nil {
		return s.saveJobs(jobs)
	}
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
