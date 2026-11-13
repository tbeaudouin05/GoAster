package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"goaster/internal/artefacts"
	"goaster/internal/scheduler"
)

const (
	MonitorIntervalSeconds int64 = 1800
	schedulerMessagePrefix       = "goaster_migration_monitor:"
)

// StatusResult contains only safe state and scheduler metadata.
type StatusResult struct {
	Migration    Result `json:"migration"`
	MonitorID    string `json:"monitor_id,omitempty"`
	MonitorState string `json:"monitor_state,omitempty"`
	MonitorOn    bool   `json:"monitor_on"`
}

// SchedulerPath returns the migration-owned persisted scheduler file. It is
// separate from the model-facing generic cron tool's store.
func SchedulerPath() (string, error) {
	root, err := artefacts.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "state", "migration-monitor", "jobs.json"), nil
}

func schedulerJobID(jobID string) string {
	return "migration-" + jobID
}

func schedulerMessage(jobID string) string {
	return schedulerMessagePrefix + jobID
}

func parseSchedulerMessage(message string) (string, bool) {
	if !strings.HasPrefix(message, schedulerMessagePrefix) {
		return "", false
	}
	jobID := strings.TrimPrefix(message, schedulerMessagePrefix)
	return jobID, safeIdentifier(jobID, 256)
}

// StartMonitor durably records the approved request before adding its
// migration-owned recurring scheduler job. It does not perform provider or
// Web I/O.
func StartMonitor(request Request, options Options, jobs *scheduler.Scheduler) (Result, error) {
	if jobs == nil {
		return Result{}, errors.New("migration scheduler unavailable")
	}
	if err := validateRequest(request, options.AllowInsecureHTTP); err != nil {
		return Result{}, err
	}
	options = completeOptions(options)
	if options.DeliverHandoff == nil {
		return Result{}, errors.New("migration delivery unavailable")
	}
	if _, err := options.LoadProviderToken(); err != nil {
		return Result{}, errProviderUnavailable
	}
	state, err := initializeMonitorState(request, options)
	if err != nil {
		return Result{}, err
	}
	base := resultForState(state)
	base.StopMonitor = state.Status != statusMonitoring
	if state.Status != statusMonitoring {
		return base, nil
	}
	id := schedulerJobID(request.JobID)
	if job, exists := jobs.Get(id); exists {
		if job.Message != schedulerMessage(request.JobID) || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
			return Result{}, errors.New("migration scheduler job is not the approved monitor")
		}
		if !job.Enabled {
			if err := enableAndVerifyJob(jobs, id); err != nil {
				return Result{}, err
			}
		}
		base.StopMonitor = false
		return base, nil
	}
	if _, err := jobs.Add(scheduler.Job{
		ID:           id,
		Kind:         scheduler.ScheduleEvery,
		EverySeconds: MonitorIntervalSeconds,
		Message:      schedulerMessage(request.JobID),
	}); err != nil {
		return Result{}, err
	}
	job, exists := jobs.Get(id)
	if !exists || job.Message != schedulerMessage(request.JobID) || !job.Enabled || job.EverySeconds != MonitorIntervalSeconds {
		return Result{}, errors.New("migration scheduler job could not be verified")
	}
	return base, nil
}

func initializeMonitorState(request Request, options Options) (migrationState, error) {
	if err := ensureRuntimeParents(options.Paths); err != nil {
		return migrationState{}, errStateUnavailable
	}
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		return migrationState{}, errStateUnavailable
	}
	defer lock.Close()
	if err := cleanupStateStaging(options.Paths.State); err != nil {
		return migrationState{}, errStateUnavailable
	}
	state, exists, err := loadState(options.Paths.State)
	if err != nil {
		return migrationState{}, errStateUnavailable
	}
	fingerprint := requestFingerprint(request)
	if exists {
		if state.JobID != request.JobID || state.Fingerprint != fingerprint || !sameTarget(state.Target, request.Target) || state.ApprovedBaselineGross != request.ApprovedBaselineGross || state.ApprovedCurrency != request.ApprovedCurrency {
			return migrationState{}, errInvalidInput
		}
		return state, nil
	}
	state = migrationState{
		Version:               stateVersion,
		JobID:                 request.JobID,
		Fingerprint:           fingerprint,
		Target:                request.Target,
		ApprovedBaselineGross: request.ApprovedBaselineGross,
		ApprovedCurrency:      request.ApprovedCurrency,
		Status:                statusMonitoring,
	}
	if err := saveState(options.Paths.State, state); err != nil {
		return migrationState{}, errStateUnavailable
	}
	return state, nil
}

// ScheduledExecutor is the only scheduler path that invokes the monitor. It
// leaves verified read-only unavailability scheduled and disables the exact
// job for every terminal, error, or uncertain path.
func ScheduledExecutor(ctx context.Context, message string, jobs *scheduler.Scheduler, options Options) error {
	jobID, ok := parseSchedulerMessage(message)
	if !ok || jobs == nil {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.New("invalid migration scheduler invocation")}
	}
	schedulerID := schedulerJobID(jobID)
	job, exists := jobs.Get(schedulerID)
	if !exists || job.Message != message || job.Kind != scheduler.ScheduleEvery || job.EverySeconds != MonitorIntervalSeconds {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.New("migration scheduler job is unavailable")}
	}
	if os.Geteuid() != 0 {
		_ = disableAndVerifyJob(jobs, schedulerID)
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.New("migration requires the root agent runtime")}
	}
	if options.DeliverHandoff == nil {
		_ = disableAndVerifyJob(jobs, schedulerID)
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: errors.New("migration delivery capability is unavailable")}
	}
	request, err := requestFromState(jobID, options)
	if err != nil {
		_ = disableAndVerifyJob(jobs, schedulerID)
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: err}
	}
	options.DisableMonitor = func() error { return disableAndVerifyJob(jobs, schedulerID) }
	result, executeErr := Execute(ctx, request, options)
	if executeErr != nil {
		_ = disableAndVerifyJob(jobs, schedulerID)
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: executeErr}
	}
	if result.Status == statusMonitoring || result.Status == "locked" {
		if err := enableAndVerifyJob(jobs, schedulerID); err != nil {
			return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: err}
		}
		return nil
	}
	if err := disableAndVerifyJob(jobs, schedulerID); err != nil {
		return scheduler.ReviewRequiredError{Message: "migration monitor requires manual review", Cause: err}
	}
	return nil
}

func requestFromState(jobID string, options Options) (Request, error) {
	options = completeOptions(options)
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		return Request{}, errStateUnavailable
	}
	defer lock.Close()
	state, exists, err := loadState(options.Paths.State)
	if err != nil || !exists || state.JobID != jobID {
		return Request{}, errStateUnavailable
	}
	return Request{
		Action:                "monitor",
		JobID:                 state.JobID,
		Target:                state.Target,
		ApprovedBaselineGross: state.ApprovedBaselineGross,
		ApprovedCurrency:      state.ApprovedCurrency,
		Approved:              true,
	}, nil
}

func disableAndVerifyJob(jobs *scheduler.Scheduler, id string) error {
	if _, err := jobs.SetEnabled(id, false); err != nil {
		return err
	}
	job, ok := jobs.Get(id)
	if !ok || job.Enabled || job.ID != id {
		return errors.New("migration scheduler disable could not be verified")
	}
	return nil
}

func enableAndVerifyJob(jobs *scheduler.Scheduler, id string) error {
	if _, err := jobs.SetEnabled(id, true); err != nil {
		return err
	}
	job, ok := jobs.Get(id)
	if !ok || !job.Enabled || job.ID != id || job.EverySeconds != MonitorIntervalSeconds {
		return errors.New("migration scheduler enable could not be verified")
	}
	return nil
}

// MigrationStatus reads only local protected state and the exact migration
// scheduler job.
func MigrationStatus(jobID string, options Options, jobs *scheduler.Scheduler) (StatusResult, error) {
	if !safeIdentifier(jobID, 256) || jobs == nil {
		return StatusResult{}, errors.New("migration status unavailable")
	}
	options = completeOptions(options)
	if err := ensureRuntimeParents(options.Paths); err != nil {
		return StatusResult{}, errStateUnavailable
	}
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		return StatusResult{}, errStateUnavailable
	}
	defer lock.Close()
	state, exists, err := loadState(options.Paths.State)
	if err != nil || !exists || state.JobID != jobID {
		return StatusResult{}, errors.New("migration status unavailable")
	}
	job, jobExists := jobs.Get(schedulerJobID(jobID))
	result := StatusResult{Migration: resultForState(state), MonitorID: schedulerJobID(jobID)}
	if jobExists {
		result.MonitorState = job.Status
		result.MonitorOn = job.Enabled
	}
	return result, nil
}

// StopMonitor is an explicit local stop. It never calls the provider or Web.
func StopMonitor(jobID string, options Options, jobs *scheduler.Scheduler) (Result, error) {
	if !safeIdentifier(jobID, 256) || jobs == nil {
		return Result{}, errors.New("migration stop unavailable")
	}
	if err := disableAndVerifyJob(jobs, schedulerJobID(jobID)); err != nil {
		return Result{}, err
	}
	options = completeOptions(options)
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		return Result{}, errStateUnavailable
	}
	defer lock.Close()
	state, exists, err := loadState(options.Paths.State)
	if err != nil || !exists || state.JobID != jobID {
		return Result{JobID: jobID, Status: "stopped", StopMonitor: true}, nil
	}
	if state.Status == statusMonitoring {
		state.Status = statusTerminal
		state.TerminalReason = "stopped"
		if err := saveState(options.Paths.State, state); err != nil {
			return Result{}, errStateUnavailable
		}
	}
	result := resultForState(state)
	result.StopMonitor = true
	return result, nil
}

// RepairHandoff is explicit/manual. It never invokes a provider mutation.
func RepairHandoff(ctx context.Context, jobID string, options Options) (Result, error) {
	if !safeIdentifier(jobID, 256) || options.DeliverHandoff == nil {
		return Result{}, errors.New("migration delivery repair unavailable")
	}
	options = completeOptions(options)
	lock, err := acquireLock(options.Paths.Lock)
	if err != nil {
		return Result{}, errStateUnavailable
	}
	state, exists, readErr := loadState(options.Paths.State)
	lock.Close()
	if readErr != nil || !exists || state.JobID != jobID || state.ServerID <= 0 {
		return Result{}, errors.New("migration delivery repair unavailable")
	}
	if err := DeliverHandoff(ctx, options.Paths, options.Now, func(data []byte) error {
		return options.DeliverHandoff(ctx, data)
	}); err != nil {
		return Result{JobID: jobID, Status: statusDeliveryPending, DeliveryPending: true, StopMonitor: true}, err
	}
	lock, err = acquireLock(options.Paths.Lock)
	if err != nil {
		return Result{JobID: jobID, Status: statusCreated, StopMonitor: true}, nil
	}
	defer lock.Close()
	state, exists, readErr = loadState(options.Paths.State)
	if readErr != nil || !exists || state.JobID != jobID {
		return Result{JobID: jobID, Status: statusCreated, StopMonitor: true}, nil
	}
	result := resultForState(state)
	result.StopMonitor = true
	return result, nil
}
