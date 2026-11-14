package migration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"goaster/internal/artefacts"
	"goaster/internal/diagnostics"
	"goaster/internal/scheduler"
)

const schedulerMessagePrefix = "goaster_migration_v1:"

type scheduledMigration struct {
	Version string  `json:"version"`
	JobID   string  `json:"job_id"`
	Request Request `json:"request"`
}

// MonitorScheduler is the small existing-scheduler surface needed by the
// migration monitor. The concrete scheduler remains owned by the agent.
type MonitorScheduler interface {
	Add(scheduler.Job) (scheduler.Job, error)
	Get(string) (scheduler.Job, bool)
	SetEnabled(string, bool) (scheduler.Job, error)
}

// monitorExecutionCoordinator is implemented by the scheduler used by the
// agent. Keeping it separate from MonitorScheduler makes an older or test
// scheduler fail closed instead of silently falling back to a non-atomic
// disable-before-create sequence.
type monitorExecutionCoordinator interface {
	ClaimExecutionMatching(context.Context, scheduler.Job) (scheduler.Job, func(error) (scheduler.Job, error), error)
	StopIfNotRunning(string) (scheduler.Job, error)
}

// SchedulerPath returns the private scheduler file used only for migration
// monitor jobs. It is scheduler state, not a migration attempt ledger.
func SchedulerPath() (string, error) {
	root, err := artefacts.Root()
	if err != nil {
		return "", fmt.Errorf("find migration scheduler root: %w", err)
	}
	return filepath.Join(root, "state", "migration-monitor", "jobs.json"), nil
}

// StartMonitor adds one non-secret 30-minute monitor and performs its first
// availability check immediately. A ready result disables the exact job before
// one create request. Any post-add failure is returned as manual review and is
// never eligible for automatic retry.
func StartMonitor(ctx context.Context, request Request, options Options, jobs MonitorScheduler) (Result, error) {
	if jobs == nil {
		return Result{}, errors.New("migration scheduler is unavailable")
	}
	if err := validateMachineRequest(request.ServerType, request.Locations); err != nil {
		return Result{}, err
	}
	if request.ApprovedHourlyGross != "" || request.ApprovedCurrency != "" {
		return Result{}, errors.New("legacy migration price approval fields are unsupported")
	}
	options = completeOptions(options)
	if options.DeliverHandoff == nil {
		return Result{}, errors.New("migration handoff delivery is unavailable")
	}
	if request.JobID == "" {
		jobID, err := newJobID()
		if err != nil {
			return Result{}, err
		}
		request.JobID = jobID
	}
	if !safeJobID(request.JobID) {
		return Result{}, errors.New("migration monitor ID is invalid")
	}
	message, err := schedulerMessage(request)
	if err != nil {
		return Result{}, err
	}
	job, exists := jobs.Get(request.JobID)
	if exists {
		if job.Message != message || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
			return Result{}, errors.New("migration monitor ID is already in use")
		}
		if job.Status == scheduler.StatusRunning {
			return Result{Status: "in_progress", MonitorID: request.JobID, ServerType: request.ServerType}, scheduler.ErrExecutionInProgress
		}
		if job.Enabled {
			return Result{Status: "monitoring", ServerType: request.ServerType, MonitorID: request.JobID}, nil
		}
		return Result{Status: "manual_review", ServerType: request.ServerType, MonitorID: request.JobID, ManualReview: true}, nil
	}
	coordinator, err := monitorExecutionCoordinatorFor(jobs)
	if err != nil {
		return Result{}, err
	}
	if _, err := jobs.Add(scheduler.Job{
		ID:           request.JobID,
		Kind:         scheduler.ScheduleEvery,
		EverySeconds: MonitorIntervalSeconds,
		Message:      message,
	}); err != nil {
		return Result{}, fmt.Errorf("add migration monitor: %w", err)
	}
	if job, ok := jobs.Get(request.JobID); !ok || job.Message != message || !job.Enabled || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
		verifyErr := errors.New("migration monitor could not be verified")
		if stopErr := stopAndVerifyMonitor(coordinator, request.JobID); stopErr != nil {
			return Result{Status: "stop_unconfirmed", MonitorID: request.JobID, ServerType: request.ServerType}, errors.Join(verifyErr, fmt.Errorf("monitor stop could not be confirmed: %w", stopErr))
		}
		return Result{Status: "manual_review", MonitorID: request.JobID, ServerType: request.ServerType, ManualReview: true}, nil
	}

	runOptions := options
	result, runErr := executeMonitor(ctx, request, runOptions, coordinator, request.JobID)
	result.MonitorID = request.JobID
	if runErr == nil {
		return result, nil
	}
	disableErr := stopAndVerifyMonitor(coordinator, request.JobID)
	if disableErr != nil {
		result.Status = "stop_unconfirmed"
		result.ManualReview = false
		return result, errors.Join(runErr, fmt.Errorf("monitor stop could not be confirmed: %w", disableErr))
	}
	diagnostics.Error("migration.monitor.start", runErr, diagnostics.Field{Key: "reason", Value: "manual review required"})
	result.Status = "manual_review"
	result.ManualReview = true
	return result, nil
}

func executeMonitor(ctx context.Context, request Request, options Options, coordinator monitorExecutionCoordinator, jobID string) (result Result, runErr error) {
	if coordinator == nil {
		return Result{}, errors.New("migration monitor execution claim capability is unavailable")
	}
	message, err := schedulerMessage(request)
	if err != nil {
		return Result{}, err
	}
	_, complete, err := coordinator.ClaimExecutionMatching(ctx, scheduler.Job{ID: jobID, Message: message, Kind: scheduler.ScheduleEvery, EverySeconds: MonitorIntervalSeconds})
	if err != nil {
		return Result{}, fmt.Errorf("claim migration monitor execution: %w", err)
	}
	if complete != nil {
		defer func() {
			completionErr := runErr
			if runErr == nil && result.Status == "monitoring" {
				completionErr = scheduler.ErrRetry
			}
			if _, err := complete(completionErr); err != nil {
				result.Status = "manual_review"
				result.ManualReview = true
				runErr = errors.Join(runErr, fmt.Errorf("persist migration monitor execution result: %w", err))
			}
		}()
	}
	if options.DeliverHandoff == nil {
		return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, errors.New("migration handoff delivery is unavailable")
	}
	token, err := options.LoadHetznerToken()
	if err != nil {
		return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, fmt.Errorf("load migration provider token: %w", err)
	}
	if !validHetznerToken(token) {
		return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, errors.New("migration provider token is invalid")
	}
	availability, err := availabilityWithToken(ctx, request.ServerType, request.Locations, token, options)
	if err != nil {
		return Result{Status: "manual_review", ServerType: request.ServerType, ManualReview: true}, fmt.Errorf("check migration availability: %w", err)
	}
	if len(availability.Locations) == 0 {
		return Result{Status: "monitoring", ServerType: request.ServerType}, nil
	}
	created, createErr := createWithToken(ctx, request, token, options)
	if createErr != nil {
		created.Status = "manual_review"
		created.ManualReview = true
		return created, createErr
	}
	return created, nil
}

// ScheduledExecutor runs only a verified migration scheduler message. A
// read-only unavailable result keeps the job enabled; every other result or
// error leaves it disabled and requires manual review.
func ScheduledExecutor(ctx context.Context, message string, jobs MonitorScheduler, options Options) error {
	if jobs == nil {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.New("migration scheduler is unavailable")}
	}
	scheduled, err := parseSchedulerMessage(message)
	if err != nil {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: err}
	}
	job, ok := jobs.Get(scheduled.JobID)
	if !ok || !job.Enabled || job.ReviewRequired || job.Message != message || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.New("migration scheduler job is not the approved monitor")}
	}
	coordinator, err := monitorExecutionCoordinatorFor(jobs)
	if err != nil {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: err}
	}
	runOptions := completeOptions(options)
	result, runErr := executeMonitor(ctx, scheduled.Request, runOptions, coordinator, scheduled.JobID)
	if runErr != nil {
		disableErr := stopAndVerifyMonitor(coordinator, scheduled.JobID)
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.Join(runErr, disableErr)}
	}
	if result.Status == "monitoring" {
		return scheduler.ErrRetry
	}
	// executeMonitor already disabled and verified the exact job before the
	// create request when it returned a non-monitoring result.
	return nil
}

// MigrationStatus reads only scheduler state and its non-secret request.
func MigrationStatus(jobID string, jobs MonitorScheduler) (StatusResult, error) {
	if jobs == nil || !safeJobID(jobID) {
		return StatusResult{}, errors.New("migration status is unavailable")
	}
	job, ok := jobs.Get(jobID)
	if !ok {
		return StatusResult{}, errors.New("migration status is unavailable")
	}
	scheduled, err := parseSchedulerMessage(job.Message)
	if err != nil || scheduled.JobID != jobID {
		return StatusResult{}, errors.New("migration status is unavailable")
	}
	return StatusResult{
		MonitorID:      job.ID,
		MonitorOn:      job.Enabled,
		SchedulerState: job.Status,
		Migration:      Result{Status: statusFromJob(job), ServerType: scheduled.Request.ServerType, MonitorID: job.ID, ManualReview: job.ReviewRequired},
		Request:        scheduled.Request,
	}, nil
}

// StopMonitor disables only the exact local monitor. It does not contact the
// provider or GoAsterWeb and does not retry or delete infrastructure.
func StopMonitor(jobID string, jobs MonitorScheduler) (Result, error) {
	if jobs == nil || !safeJobID(jobID) {
		return Result{}, errors.New("migration monitor is unavailable")
	}
	job, ok := jobs.Get(jobID)
	if !ok {
		return Result{}, errors.New("migration monitor is unavailable")
	}
	scheduled, err := parseSchedulerMessage(job.Message)
	if err != nil || scheduled.JobID != jobID {
		return Result{}, errors.New("migration monitor is unavailable")
	}
	coordinator, err := monitorExecutionCoordinatorFor(jobs)
	if err != nil {
		return Result{}, fmt.Errorf("stop migration monitor: %w", err)
	}
	if err := stopAndVerifyMonitor(coordinator, jobID); err != nil {
		return Result{}, fmt.Errorf("disable migration monitor: %w", err)
	}
	return Result{Status: "stopped", ServerType: scheduled.Request.ServerType, MonitorID: jobID}, nil
}

func schedulerMessage(request Request) (string, error) {
	data, err := json.Marshal(scheduledMigration{Version: "v1", JobID: request.JobID, Request: request})
	if err != nil {
		return "", fmt.Errorf("encode migration monitor message: %w", err)
	}
	return schedulerMessagePrefix + string(data), nil
}

func parseSchedulerMessage(message string) (scheduledMigration, error) {
	if !strings.HasPrefix(message, schedulerMessagePrefix) {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(message, schedulerMessagePrefix)))
	decoder.DisallowUnknownFields()
	var scheduled scheduledMigration
	if err := decoder.Decode(&scheduled); err != nil {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return scheduledMigration{}, errors.New("migration scheduler message contains trailing data")
	}
	if scheduled.Version != "v1" || !safeJobID(scheduled.JobID) || scheduled.Request.JobID != scheduled.JobID || validateMachineRequest(scheduled.Request.ServerType, scheduled.Request.Locations) != nil {
		return scheduledMigration{}, errors.New("migration scheduler message is invalid")
	}
	if scheduled.Request.ApprovedHourlyGross != "" || scheduled.Request.ApprovedCurrency != "" {
		return scheduledMigration{}, errors.New("legacy migration price approval fields are unsupported")
	}
	return scheduled, nil
}

func newJobID() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate migration monitor ID: %w", err)
	}
	return "migration-" + hex.EncodeToString(random[:]), nil
}

func safeJobID(value string) bool {
	if !strings.HasPrefix(value, "migration-") || len(value) > 96 || !safeName(value, 96) {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func safeCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func monitorExecutionCoordinatorFor(jobs MonitorScheduler) (monitorExecutionCoordinator, error) {
	coordinator, ok := jobs.(monitorExecutionCoordinator)
	if !ok || coordinator == nil {
		return nil, errors.New("migration monitor execution claim capability is unavailable")
	}
	return coordinator, nil
}

func stopAndVerifyMonitor(coordinator monitorExecutionCoordinator, id string) error {
	job, err := coordinator.StopIfNotRunning(id)
	if err != nil {
		return err
	}
	if job.ID != id || job.Enabled || job.Status == scheduler.StatusRunning {
		return errors.New("migration monitor disable could not be verified")
	}
	return nil
}

func statusFromJob(job scheduler.Job) string {
	if job.ReviewRequired || job.Status == scheduler.StatusFailed || job.Status == scheduler.StatusRunning {
		return "manual_review"
	}
	if job.Enabled {
		return "monitoring"
	}
	return "stopped"
}
