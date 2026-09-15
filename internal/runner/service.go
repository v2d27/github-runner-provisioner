package runner

import (
	"context"
	"log/slog"
	"time"

	"github.me/v2d27/github-runner-provisioner/internal/aws"
	"github.me/v2d27/github-runner-provisioner/internal/config"
	ghclient "github.me/v2d27/github-runner-provisioner/internal/github"
	"github.me/v2d27/github-runner-provisioner/internal/store"
)

// A "no idle slot" result from AllocateIdleRunner is often transient rather
// than a genuine capacity shortage: BindToJob promotes a freshly provisioned
// instance's non-triggering slots to IDLE immediately, and a completing
// job's own slot is freed by handleCompleted independently — so two jobs of
// the same profile queuing within the same event-processing tick can easily
// see zero idle slots for an instant even though one is about to free up.
// allocationRetryWindow/allocationRetryInterval bound how long handleQueued
// waits for that to resolve before concluding new EC2 capacity is actually
// needed — cheap next to how long an instance takes to boot, so it's worth
// waiting out first. Comfortably inside runner.provision_lambda_timeout
// (default 60s).
const (
	allocationRetryWindow   = 5 * time.Second
	allocationRetryInterval = 500 * time.Millisecond
)

// GitHub workflow_job actions this platform reacts to. Any other action
// (e.g. "waiting") is a no-op.
const (
	ActionQueued     = "queued"
	ActionInProgress = "in_progress"
	ActionCompleted  = "completed"
)

// Event is the normalized workflow_job event handed to Service by
// cmd/provision, decoded from the SQS message body the webhook Lambda
// produced.
type Event struct {
	Action        string
	JobID         int64
	WorkflowRunID int64
	Labels        []string
}

// Service resolves a workflow_job event into a runner allocation or a new
// EC2-backed runner, per
// docs/infrastructure/enterprise-standard-upgrade.md section 19.
type Service struct {
	cfg              *config.Config
	store            *store.Client
	github           *ghclient.Provider
	ec2              *aws.EC2
	ami              *aws.AMIResolver
	launchTemplateID string
	// readyCallbackURL is the runner-ready endpoint (internal/ready, fronted
	// by the same API Gateway as the webhook) every provisioned instance is
	// told to call back via UserData once each of its runner processes
	// registers with GitHub and starts. See store.MarkSlotReady.
	readyCallbackURL string
	logger           *slog.Logger
}

func NewService(cfg *config.Config, st *store.Client, gh *ghclient.Provider, ec2 *aws.EC2, ami *aws.AMIResolver, launchTemplateID, readyCallbackURL string, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, store: st, github: gh, ec2: ec2, ami: ami, launchTemplateID: launchTemplateID, readyCallbackURL: readyCallbackURL, logger: logger}
}

// Handle processes one normalized workflow_job event.
func (s *Service) Handle(ctx context.Context, ev Event) error {
	switch ev.Action {
	case ActionQueued:
		return s.handleQueued(ctx, ev)
	case ActionInProgress:
		return s.handleInProgress(ctx, ev)
	case ActionCompleted:
		return s.handleCompleted(ctx, ev)
	default:
		s.logger.Info("runner: ignoring unsupported workflow_job action", "action", ev.Action, "job_id", ev.JobID)
		return nil
	}
}

func (s *Service) poolSelector(profileName string) store.PoolSelector {
	return store.PoolSelector{
		Scope:        s.cfg.Runner.Scope,
		Organization: s.cfg.Runner.Organization,
		Repository:   s.cfg.Runner.Repository,
		Profile:      profileName,
		Group:        s.cfg.Runner.Group,
	}
}

func (s *Service) handleQueued(ctx context.Context, ev Event) error {
	profileName, _, err := ResolveProfile(s.cfg.Runner, ev.Labels)
	if err != nil {
		s.logger.Info("runner: ignoring job with no matching profile", "job_id", ev.JobID, "labels", ev.Labels)
		return nil
	}
	sel := s.poolSelector(profileName)

	job, created, err := s.store.CreateJobIfNotExists(ctx, store.NewQueuedJob(ev.JobID, ev.WorkflowRunID, sel, ev.Labels))
	if err != nil {
		return err
	}
	if !created && job.Status != store.JobQueued {
		// Already allocated/provisioning/completed/ignored by a previous
		// delivery of this event — GitHub webhooks are at-least-once.
		s.logger.Info("runner: job already handled, skipping redelivery", "job_id", ev.JobID, "status", job.Status)
		return nil
	}

	runner, allocated, err := s.allocateIdleRunnerWithRetry(ctx, sel, job)
	if err != nil {
		return err
	}
	if allocated {
		s.logger.Info("runner: allocated idle runner", "runner_id", runner.RunnerID, "job_id", ev.JobID, "profile", profileName)
		return nil
	}

	profile := s.cfg.Runner.Profiles[profileName]
	claimed, err := s.store.ClaimForProvisioning(ctx, ev.JobID)
	if err != nil {
		return err
	}
	if !claimed {
		// Another concurrent invocation (or the allocation transaction
		// above, run by a competing invocation) is already handling this
		// job — this is the provisioning lock doing its job, not an error.
		s.logger.Info("runner: job already claimed for provisioning by another invocation", "job_id", ev.JobID)
		return nil
	}

	return s.provisionRunner(ctx, sel, profile, job)
}

// allocateIdleRunnerWithRetry re-queries the idle pool for up to
// allocationRetryWindow before giving up. See the doc comment on
// allocationRetryWindow for why an initial "no idle slot" result doesn't
// necessarily mean new EC2 capacity is needed.
func (s *Service) allocateIdleRunnerWithRetry(ctx context.Context, sel store.PoolSelector, job *store.Job) (*store.Runner, bool, error) {
	deadline := time.Now().Add(allocationRetryWindow)

	for attempt := 0; ; attempt++ {
		runner, allocated, err := s.store.AllocateIdleRunner(ctx, sel, job)
		if err != nil || allocated {
			return runner, allocated, err
		}
		if !time.Now().Before(deadline) {
			if attempt > 0 {
				s.logger.Info("runner: no idle slot freed up within retry window, provisioning new instance", "job_id", job.JobID, "attempts", attempt+1)
			}
			return nil, false, nil
		}

		select {
		case <-time.After(allocationRetryInterval):
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

func (s *Service) handleInProgress(ctx context.Context, ev Event) error {
	job, err := s.store.GetJob(ctx, ev.JobID)
	if err != nil {
		if err == store.ErrNotFound {
			s.logger.Warn("runner: in_progress for unknown job", "job_id", ev.JobID)
			return nil
		}
		return err
	}
	if job.RunnerID == "" {
		s.logger.Warn("runner: in_progress for job with no bound runner", "job_id", ev.JobID, "status", job.Status)
		return nil
	}

	runner, err := s.store.GetRunner(ctx, job.RunnerID)
	if err != nil {
		return err
	}
	if job.SlotIndex < 0 || job.SlotIndex >= len(runner.Slots) {
		s.logger.Warn("runner: in_progress but job's slot_index is out of range", "job_id", ev.JobID, "runner_id", runner.RunnerID, "slot_index", job.SlotIndex, "slot_count", len(runner.Slots))
		return nil
	}
	if runner.Slots[job.SlotIndex].Status != store.SlotBusy {
		s.logger.Warn("runner: in_progress but slot not BUSY", "job_id", ev.JobID, "runner_id", runner.RunnerID, "slot_index", job.SlotIndex, "status", runner.Slots[job.SlotIndex].Status)
	}
	return nil
}

func (s *Service) handleCompleted(ctx context.Context, ev Event) error {
	job, err := s.store.GetJob(ctx, ev.JobID)
	if err != nil {
		if err == store.ErrNotFound {
			s.logger.Warn("runner: completed for unknown job", "job_id", ev.JobID)
			return nil
		}
		return err
	}

	if err := s.store.MarkCompleted(ctx, ev.JobID); err != nil {
		return err
	}
	if job.RunnerID == "" {
		return nil // job was ignored/never allocated, nothing to free
	}
	sel := s.poolSelector(job.Profile)
	if err := s.store.MarkIdle(ctx, job.RunnerID, job.SlotIndex, sel, s.cfg.Runner.IdleTimeout.Duration()); err != nil {
		return err
	}
	s.logger.Info("runner: job completed, slot idle", "job_id", ev.JobID, "runner_id", job.RunnerID, "slot_index", job.SlotIndex, "idle_timeout", s.cfg.Runner.IdleTimeout.String())
	return nil
}
