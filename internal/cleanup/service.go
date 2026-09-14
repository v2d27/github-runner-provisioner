// Package cleanup implements the periodic reaper: expired IDLE runners,
// stuck-in-PROVISIONING orphans, and (via the same Lambda) immediate
// handling of EC2 Spot interruption notices.
//
// See docs/infrastructure/enterprise-standard-upgrade.md sections 20, 30
// and 32.
package cleanup

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.me/v2d27/github-runner-provisioner/internal/aws"
	"github.me/v2d27/github-runner-provisioner/internal/config"
	ghclient "github.me/v2d27/github-runner-provisioner/internal/github"
	"github.me/v2d27/github-runner-provisioner/internal/store"
)

// staleProvisioningAfter bounds how long a runner/job may sit in
// PROVISIONING before being treated as an orphan — comfortably longer than
// any expected EC2 launch + registration time.
const staleProvisioningAfter = 10 * time.Minute

type Service struct {
	cfg    *config.Config
	store  *store.Client
	github *ghclient.Provider
	ec2    *aws.EC2
	logger *slog.Logger
}

func NewService(cfg *config.Config, st *store.Client, gh *ghclient.Provider, ec2 *aws.EC2, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, store: st, github: gh, ec2: ec2, logger: logger}
}

// RunScheduledSweep performs the three-minutely reconciliation: terminate
// instances whose every runner slot is idle and eligible (see
// eligibleForTermination), fail any instance with a slot that never
// confirmed ready within runner.boot_timeout (see reconcileStuckStarting),
// and recover PROVISIONING orphans (runner and job side).
func (s *Service) RunScheduledSweep(ctx context.Context) error {
	now := time.Now()
	var errs []error

	active, err := s.store.ListActiveInstances(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, r := range active {
		if !eligibleForTermination(r, now, s.cfg.Runner.AutoTerminatingTime.Duration()) {
			continue
		}
		if err := s.terminateRunner(ctx, r); err != nil {
			s.logger.Error("cleanup: terminate expired instance", "runner_id", r.RunnerID, "error", err)
			errs = append(errs, err)
		}
	}
	if err := s.reconcileStuckStarting(ctx, active, now); err != nil {
		errs = append(errs, err)
	}

	cutoff := now.Add(-staleProvisioningAfter)
	if err := s.reconcileStaleProvisioningRunners(ctx, cutoff); err != nil {
		errs = append(errs, err)
	}
	if err := s.reconcileStaleProvisioningJobs(ctx, cutoff); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (s *Service) reconcileStaleProvisioningRunners(ctx context.Context, cutoff time.Time) error {
	stale, err := s.store.ListStaleProvisioning(ctx, cutoff)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range stale {
		if r.InstanceID == "" {
			// RunInstances itself never completed — nothing to terminate.
			if err := s.store.MarkRunnerFailed(ctx, r.RunnerID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		state, found, err := s.ec2.InstanceState(ctx, r.InstanceID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !found || state == "terminated" || state == "shutting-down" {
			if err := s.store.MarkRunnerFailed(ctx, r.RunnerID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		// Instance exists but was never bound to a job (BindToJob never
		// ran) — we don't know what job it was for, so it can't safely
		// serve one. Terminate it; the job side sweep independently
		// reverts/fails the job so it gets a fresh runner.
		if err := s.terminateRunner(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reconcileStuckStarting fails any active runner with a slot whose
// starting_since marker (set by BindToJob) is still there past
// runner.boot_timeout — its userdata script's runner-ready callback never
// arrived (see store.MarkSlotReady), so the boot is presumed dead. This is
// independent of the slot's current Busy/Idle status: BindToJob already made
// every slot immediately allocatable (see its doc comment), so by the time
// this fires the slot may already be busy with a job, idle in the pool, or
// (the failure case this exists for) stuck exactly as BindToJob left it,
// with no runner process ever having come up to claim it.
//
// Terminating is safe even for a slot that never actually registered:
// terminateRunner's per-slot GitHub lookup treats "not found" as
// already-gone. Any job bound to a stuck slot is reverted to QUEUED so it
// gets a fresh runner instead of waiting forever.
func (s *Service) reconcileStuckStarting(ctx context.Context, active []*store.Runner, now time.Time) error {
	bootTimeout := s.cfg.Runner.BootTimeout.Duration()
	var errs []error
	for _, r := range active {
		jobIDs, stuck := stuckStartingJobIDs(r, now, bootTimeout)
		if !stuck {
			continue
		}
		s.logger.Warn("cleanup: runner slot never confirmed ready, treating boot as failed", "runner_id", r.RunnerID, "instance_id", r.InstanceID)
		if err := s.terminateRunner(ctx, r); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, jobID := range jobIDs {
			job, err := s.store.GetJob(ctx, jobID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				errs = append(errs, err)
				continue
			}
			if err := s.store.RevertAllocation(ctx, job); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// stuckStartingJobIDs reports whether r has any slot whose starting_since is
// still set past bootTimeout, and the JobIDs (if any) bound to such slots
// that need reverting to QUEUED once the runner itself is torn down.
func stuckStartingJobIDs(r *store.Runner, now time.Time, bootTimeout time.Duration) (jobIDs []int64, stuck bool) {
	for _, slot := range r.Slots {
		if slot.StartingSince == 0 || now.Sub(time.Unix(slot.StartingSince, 0)) < bootTimeout {
			continue
		}
		stuck = true
		if slot.JobID != 0 {
			jobIDs = append(jobIDs, slot.JobID)
		}
	}
	return jobIDs, stuck
}

func (s *Service) reconcileStaleProvisioningJobs(ctx context.Context, cutoff time.Time) error {
	stale, err := s.store.ListStaleProvisioningJobs(ctx, cutoff)
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range stale {
		if err := s.store.RevertProvisioning(ctx, j); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HandleSpotInterruption reacts to an "EC2 Spot Instance Interruption
// Warning" event: the instance has ~2 minutes left, so this promptly
// deregisters it from GitHub and marks it terminated rather than waiting for
// the next scheduled sweep (which doesn't otherwise look at TERMINATING).
func (s *Service) HandleSpotInterruption(ctx context.Context, instanceID string) error {
	r, err := s.store.FindRunnerByInstanceID(ctx, instanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.logger.Info("cleanup: spot interruption for untracked instance", "instance_id", instanceID)
			return nil
		}
		return err
	}
	return s.terminateRunner(ctx, r)
}

// terminateRunner drives a runner through TERMINATING -> deregistered from
// GitHub -> EC2 terminated -> TERMINATED. It re-checks the runner's status
// immediately before terminating (doc section 20): if a job claimed it
// between the caller's read and this call, TransitionToTerminating's
// condition fails and this is a safe no-op.
func (s *Service) terminateRunner(ctx context.Context, r *store.Runner) error {
	moved, err := s.store.TransitionToTerminating(ctx, r.RunnerID)
	if err != nil {
		return err
	}
	if !moved {
		s.logger.Info("cleanup: runner no longer eligible for termination, skipping", "runner_id", r.RunnerID)
		return nil
	}

	client, err := s.github.Client(ctx)
	if err != nil {
		return err
	}

	// r.Slots holds one GitHub registration per runner process the instance
	// was told to run (config.Profile.RunnerCount, 1-2) — every one of them
	// must be deregistered, or the unremoved ones sit in GitHub's runner
	// list forever (offline, unremovable by any later sweep once the
	// instance and its DynamoDB row are both gone).
	for i, slot := range r.Slots {
		ghRunner, err := ghclient.FindRunnerByName(ctx, client, s.cfg.Runner, slot.Name)
		switch {
		case err == nil:
			id := ghRunner.GetID()
			if err := ghclient.RemoveRunner(ctx, client, s.cfg.Runner, id); err != nil {
				return err
			}
			r.Slots[i].GitHubRunnerID = id
		case errors.Is(err, ghclient.ErrRunnerNotFound):
			// Already deregistered (or never finished registering) — fine.
		default:
			return err
		}
	}

	if r.InstanceID != "" {
		if err := s.ec2.TerminateInstance(ctx, r.InstanceID); err != nil {
			return err
		}
	}

	if err := s.store.MarkTerminated(ctx, r.RunnerID, r.Slots); err != nil {
		return err
	}
	s.logger.Info("cleanup: runner terminated", "runner_id", r.RunnerID, "instance_id", r.InstanceID, "slots", len(r.Slots))
	return nil
}

// eligibleForTermination reports whether every slot on r is currently idle,
// and either every slot has individually passed its own idle_timeout
// (TerminateAfter), or the instance itself has outlived
// runner.auto_terminating_time regardless of any slot's own timer — see the
// Runner/Slot doc comments in internal/store/runner.go.
func eligibleForTermination(r *store.Runner, now time.Time, autoTerminatingTime time.Duration) bool {
	if len(r.Slots) == 0 {
		return false
	}

	allExpired := true
	for _, s := range r.Slots {
		if s.Status != store.SlotIdle {
			return false // at least one slot still busy — never eligible
		}
		if s.TerminateAfter == 0 || now.Unix() < s.TerminateAfter {
			allExpired = false
		}
	}
	if allExpired {
		return true
	}

	return now.Sub(time.Unix(r.CreatedAt, 0)) > autoTerminatingTime
}
