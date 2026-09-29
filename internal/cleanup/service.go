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
	"fmt"
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

// staleBusyAfter bounds how long a slot DynamoDB still shows BUSY may keep
// an instance past runner.auto_terminating_time alive without GitHub
// corroborating it.
const staleBusyAfter = 10 * time.Minute

// processCheckWait bounds how long a sweep waits for the on-instance runner
// process check (see aws.SSM.CheckRunnerProcesses) to answer.
const processCheckWait = 20 * time.Second

// processBusyCeiling is how old an instance may get before a Runner.Worker
// found on it stops deferring termination on its own — so a hung worker
// process can't keep an instance alive forever. It matches GitHub's default
// job timeout (timeout-minutes: 360); GitHub's own busy flag still applies
// past it.
const processBusyCeiling = 6 * time.Hour

type Service struct {
	cfg    *config.Config
	store  *store.Client
	github *ghclient.Provider
	ec2    *aws.EC2
	ssm    *aws.SSM
	logger *slog.Logger
}

func NewService(cfg *config.Config, st *store.Client, gh *ghclient.Provider, ec2 *aws.EC2, ssm *aws.SSM, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, store: st, github: gh, ec2: ec2, ssm: ssm, logger: logger}
}

// RunScheduledSweep performs the three-minutely reconciliation
func (s *Service) RunScheduledSweep(ctx context.Context) error {
	now := time.Now()
	var errs []error

	live, err := s.store.ListLiveInstances(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	var active, terminating, expired []*store.Runner
	for _, r := range live {
		switch {
		case r.Status == store.RunnerTerminating:
			terminating = append(terminating, r)
		case eligibleForTermination(r, now, s.cfg.Runner.AutoTerminatingTime.Duration()):
			active = append(active, r)
			expired = append(expired, r)
		default:
			active = append(active, r)
		}
	}
	procs := s.checkRunnerProcesses(ctx, append(append([]*store.Runner(nil), terminating...), expired...))

	for _, r := range terminating {
		if err := s.finishTermination(ctx, r, procs); err != nil {
			s.logger.Error("cleanup: resume termination", "runner_id", r.RunnerID, "instance_id", r.InstanceID, "error", err)
			errs = append(errs, err)
		}
	}
	for _, r := range expired {
		if err := s.terminateExpiredRunner(ctx, r, now, procs); err != nil {
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
	if err := s.reconcileUntrackedInstances(ctx, now); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// reconcileUntrackedInstances terminates runner instances EC2 still has but
// no live DynamoDB row tracks — e.g. the provision Lambda died between
// RunInstances and SetInstanceID, so the stale-provisioning sweep marked the
// runner FAILED without ever knowing an instance existed.
func (s *Service) reconcileUntrackedInstances(ctx context.Context, now time.Time) error {
	instances, err := s.ec2.ListManagedInstances(ctx, s.store.TableName())
	if err != nil {
		return err
	}

	var errs []error
	for _, inst := range instances {
		if now.Sub(inst.LaunchTime) < staleProvisioningAfter {
			continue // SetInstanceID may simply not have landed yet
		}
		runnerID := inst.Tags[aws.TagRunnerID]
		if runnerID == "" {
			continue
		}
		r, err := s.store.GetRunner(ctx, runnerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, err)
			continue
		}
		if tracks(r, inst.InstanceID) {
			continue
		}

		orphan := orphanRunner(r, runnerID, inst)
		s.logger.Warn("cleanup: runner instance not tracked by any live runner record, terminating",
			"runner_id", runnerID, "instance_id", inst.InstanceID, "launched", inst.LaunchTime)
		busy, err := s.deregisterRunners(ctx, orphan)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(busy) > 0 {
			s.logger.Info("cleanup: untracked instance still running a job on github, will terminate on a later sweep",
				"instance_id", inst.InstanceID, "busy_slots", busy)
			continue
		}
		if err := s.ec2.TerminateInstance(ctx, inst.InstanceID); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// tracks reports whether r is a live runner record owning instanceID.
func tracks(r *store.Runner, instanceID string) bool {
	if r == nil || r.InstanceID != instanceID {
		return false
	}
	switch r.Status {
	case store.RunnerProvisioning, store.RunnerActive, store.RunnerTerminating:
		return true
	}
	return false
}

// orphanRunner builds the runner to deregister for an untracked instance:
// its record's slots if one exists, otherwise every slot name the instance
// could have registered (<Name tag>-1..-2, the most config.Profile.RunnerCount
// allows) — FindRunnerByName treats the ones that never existed as gone.
func orphanRunner(r *store.Runner, runnerID string, inst aws.ManagedInstance) *store.Runner {
	orphan := &store.Runner{RunnerID: runnerID, InstanceID: inst.InstanceID}
	if r != nil && len(r.Slots) > 0 {
		orphan.Slots = append([]store.Slot(nil), r.Slots...)
		return orphan
	}
	if base := inst.Tags["Name"]; base != "" {
		for i := 1; i <= 2; i++ {
			orphan.Slots = append(orphan.Slots, store.Slot{Index: i - 1, Name: fmt.Sprintf("%s-%d", base, i)})
		}
	}
	return orphan
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
		if err := s.terminateRunner(ctx, r, nil, nil); err != nil {
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
		if err := s.terminateRunner(ctx, r, nil, nil); err != nil {
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
func (s *Service) HandleSpotInterruption(ctx context.Context, instanceID string) error {
	r, err := s.store.FindRunnerByInstanceID(ctx, instanceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.logger.Info("cleanup: spot interruption for untracked instance", "instance_id", instanceID)
			return nil
		}
		return err
	}
	return s.terminateRunner(ctx, r, nil, nil)
}

// terminateExpiredRunner terminates an ACTIVE instance eligibleForTermination
// picked, but only once GitHub agrees none of its runners is busy.
func (s *Service) terminateExpiredRunner(ctx context.Context, r *store.Runner, now time.Time, procs map[string]aws.RunnerProcesses) error {
	fresh, err := s.hasFreshBusySlot(ctx, r, now)
	if err != nil {
		return err
	}
	if fresh {
		return nil
	}
	if s.workerRunning(r, procs, now) {
		return nil
	}

	client, err := s.github.Client(ctx)
	if err != nil {
		return err
	}
	for _, slot := range r.Slots {
		ghRunner, err := ghclient.FindRunnerByName(ctx, client, s.cfg.Runner, slot.Name)
		if errors.Is(err, ghclient.ErrRunnerNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if ghRunner.GetBusy() {
			s.logger.Warn("cleanup: github reports runner busy although its slot is not tracked as running a job here, deferring termination",
				"runner_id", r.RunnerID, "instance_id", r.InstanceID, "slot", slot.Name, "slot_status", slot.Status)
			return nil
		}
	}

	return s.terminateRunner(ctx, r, r.Slots, procs)
}

// checkRunnerProcesses runs the on-instance process check on every runner
// in rs at once. A failure is logged, not returned: an instance without a
// result simply falls back to GitHub's busy flag.
func (s *Service) checkRunnerProcesses(ctx context.Context, rs []*store.Runner) map[string]aws.RunnerProcesses {
	var ids []string
	for _, r := range rs {
		if r.InstanceID != "" {
			ids = append(ids, r.InstanceID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	procs, err := s.ssm.CheckRunnerProcesses(ctx, ids, processCheckWait)
	if err != nil {
		s.logger.Warn("cleanup: runner process check failed, relying on github busy status", "error", err)
	}
	if len(procs) < len(ids) {
		s.logger.Info("cleanup: runner process check unanswered by some instances, relying on github busy status for them",
			"checked", len(procs), "requested", len(ids))
	}
	return procs
}

// workerRunning reports whether the process check found a Runner.Worker —
// a job executing — on r's instance, unless the instance is past
// processBusyCeiling.
func (s *Service) workerRunning(r *store.Runner, procs map[string]aws.RunnerProcesses, now time.Time) bool {
	p, ok := procs[r.InstanceID]
	if !ok || !p.Busy() {
		return false
	}
	if now.Sub(time.Unix(r.CreatedAt, 0)) > processBusyCeiling {
		s.logger.Warn("cleanup: Runner.Worker still running past the process-busy ceiling, no longer deferring termination for it",
			"runner_id", r.RunnerID, "instance_id", r.InstanceID, "processes", p.Output)
		return false
	}
	s.logger.Info("cleanup: Runner.Worker running on instance, deferring termination",
		"runner_id", r.RunnerID, "instance_id", r.InstanceID, "workers", p.Workers, "processes", p.Output)
	return true
}

// hasFreshBusySlot reports whether any slot DynamoDB shows BUSY is bound to
// a job allocated to it less than staleBusyAfter ago
func (s *Service) hasFreshBusySlot(ctx context.Context, r *store.Runner, now time.Time) (bool, error) {
	for _, slot := range r.Slots {
		if slot.Status != store.SlotBusy || slot.JobID == 0 {
			continue
		}
		job, err := s.store.GetJob(ctx, slot.JobID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if job.Status == store.JobAllocated && job.RunnerID == r.RunnerID && now.Sub(time.Unix(job.UpdatedAt, 0)) < staleBusyAfter {
			return true, nil
		}
	}
	return false, nil
}

// terminateRunner moves a runner into TERMINATING and then drives it the
// rest of the way via finishTermination. observed is passed through to
// store.TransitionToTerminating: if the runner (or, when non-nil, any of its
// slots) changed since the caller read it, this is a safe no-op. procs, when
// non-nil, is the sweep's on-instance process check (see finishTermination).
func (s *Service) terminateRunner(ctx context.Context, r *store.Runner, observed []store.Slot, procs map[string]aws.RunnerProcesses) error {
	moved, err := s.store.TransitionToTerminating(ctx, r.RunnerID, observed)
	if err != nil {
		return err
	}
	if !moved {
		s.logger.Info("cleanup: runner no longer eligible for termination, skipping", "runner_id", r.RunnerID)
		return nil
	}
	return s.finishTermination(ctx, r, procs)
}

// finishTermination drives a TERMINATING runner through deregistered from
// GitHub -> EC2 terminated -> TERMINATED.
//
// Beyond GitHub's per-runner busy flag, a Runner.Worker the on-instance
// process check (procs) found also holds off the EC2 termination itself:
// the job is left to finish, and the next sweep retries.
func (s *Service) finishTermination(ctx context.Context, r *store.Runner, procs map[string]aws.RunnerProcesses) error {
	busy, err := s.deregisterRunners(ctx, r)
	if err != nil {
		return err
	}
	if len(busy) > 0 {
		s.logger.Info("cleanup: runner still running a job on github, will finish termination on a later sweep",
			"runner_id", r.RunnerID, "instance_id", r.InstanceID, "busy_slots", busy)
		return nil
	}
	if s.workerRunning(r, procs, time.Now()) {
		return nil
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

// deregisterRunners removes every one of r's slots from GitHub that isn't
// busy, recording each removed slot's GitHub runner ID on r.Slots. It
// returns the names of slots GitHub reports busy, which can't be removed
// until their job finishes (GitHub answers 422).
func (s *Service) deregisterRunners(ctx context.Context, r *store.Runner) (busy []string, err error) {
	client, err := s.github.Client(ctx)
	if err != nil {
		return nil, err
	}

	for i, slot := range r.Slots {
		ghRunner, err := ghclient.FindRunnerByName(ctx, client, s.cfg.Runner, slot.Name)
		switch {
		case err == nil:
			if ghRunner.GetBusy() {
				busy = append(busy, slot.Name)
				continue
			}
			id := ghRunner.GetID()
			if err := ghclient.RemoveRunner(ctx, client, s.cfg.Runner, id); err != nil {
				if errors.Is(err, ghclient.ErrRunnerBusy) {
					busy = append(busy, slot.Name) // picked up a job since FindRunnerByName
					continue
				}
				return nil, err
			}
			r.Slots[i].GitHubRunnerID = id
		case errors.Is(err, ghclient.ErrRunnerNotFound):
			// Already deregistered (or never finished registering) — fine.
		default:
			return nil, err
		}
	}
	return busy, nil
}

// eligibleForTermination reports whether r is a termination candidate:
// either every slot is idle and has individually passed its own
// idle_timeout (TerminateAfter), or the instance itself has outlived
// runner.auto_terminating_time regardless of any slot's own timer — see the
// Runner/Slot doc comments in internal/store/runner.go.
func eligibleForTermination(r *store.Runner, now time.Time, autoTerminatingTime time.Duration) bool {
	if len(r.Slots) == 0 {
		return false
	}
	if now.Sub(time.Unix(r.CreatedAt, 0)) > autoTerminatingTime {
		return true
	}

	for _, s := range r.Slots {
		if s.Status != store.SlotIdle || s.TerminateAfter == 0 || now.Unix() < s.TerminateAfter {
			return false
		}
	}
	return true
}
