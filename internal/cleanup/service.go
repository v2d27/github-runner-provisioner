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

	"github.me/v2d27/provision-github-runner-on-demand/internal/aws"
	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
	ghclient "github.me/v2d27/provision-github-runner-on-demand/internal/github"
	"github.me/v2d27/provision-github-runner-on-demand/internal/store"
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
// expired IDLE runners, and recover PROVISIONING orphans (runner and job
// side).
func (s *Service) RunScheduledSweep(ctx context.Context) error {
	now := time.Now()
	var errs []error

	expired, err := s.store.ListExpiredIdle(ctx, now)
	if err != nil {
		errs = append(errs, err)
	}
	for _, r := range expired {
		if err := s.terminateRunner(ctx, r); err != nil {
			s.logger.Error("cleanup: terminate expired idle runner", "runner_id", r.RunnerID, "error", err)
			errs = append(errs, err)
		}
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

	var githubRunnerID int64
	client, err := s.github.Client(ctx)
	if err != nil {
		return err
	}
	ghRunner, err := ghclient.FindRunnerByName(ctx, client, s.cfg.Runner, r.Name)
	switch {
	case err == nil:
		githubRunnerID = ghRunner.GetID()
		if err := ghclient.RemoveRunner(ctx, client, s.cfg.Runner, githubRunnerID); err != nil {
			return err
		}
	case errors.Is(err, ghclient.ErrRunnerNotFound):
		// Already deregistered (or never finished registering) — fine.
	default:
		return err
	}

	if r.InstanceID != "" {
		if err := s.ec2.TerminateInstance(ctx, r.InstanceID); err != nil {
			return err
		}
	}

	if err := s.store.MarkTerminated(ctx, r.RunnerID, githubRunnerID); err != nil {
		return err
	}
	s.logger.Info("cleanup: runner terminated", "runner_id", r.RunnerID, "instance_id", r.InstanceID, "github_runner_id", githubRunnerID)
	return nil
}
