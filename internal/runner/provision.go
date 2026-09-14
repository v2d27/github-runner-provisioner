package runner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.me/v2d27/github-runner-provisioner/internal/aws"
	"github.me/v2d27/github-runner-provisioner/internal/config"
	ghclient "github.me/v2d27/github-runner-provisioner/internal/github"
	"github.me/v2d27/github-runner-provisioner/internal/store"
)

// provisionRunner claims a registration token, launches the EC2 instance and
// binds it to job. Every failure path reverts the job's provisioning claim
// (via RevertProvisioning) so it can be retried instead of stranded.
func (s *Service) provisionRunner(ctx context.Context, sel store.PoolSelector, profile config.Profile, job *store.Job) error {
	client, err := s.github.Client(ctx)
	if err != nil {
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: get github client: %w", err)
	}

	regToken, err := ghclient.CreateRegistrationToken(ctx, client, s.cfg.Runner)
	if err != nil {
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: create registration token: %w", err)
	}

	imageID, err := s.resolveAMI(ctx, profile)
	if err != nil {
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: resolve ami: %w", err)
	}

	runnerID := store.NewRunnerID()
	name := runnerName(sel.Profile, runnerID)
	labels := runnerLabels(profile.Labels)

	runnerRow := store.NewProvisioningRunner(sel, runnerID, name, labels, profile.Architecture, profile.InstanceType, imageID)
	if err := s.store.CreateProvisioning(ctx, runnerRow); err != nil {
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: create provisioning row: %w", err)
	}

	userData := RenderUserData(UserDataParams{
		Scope:             s.cfg.Runner.Scope,
		Organization:      s.cfg.Runner.Organization,
		Repository:        s.cfg.Runner.Repository,
		RegistrationToken: regToken.Token,
		RunnerName:        name,
		Labels:            labels,
		Group:             s.cfg.Runner.Group,
		Architecture:      profile.Architecture,
	})

	instanceID, err := s.ec2.RunInstance(ctx, aws.LaunchInput{
		LaunchTemplateID: s.launchTemplateID,
		ImageID:          imageID,
		InstanceType:     profile.InstanceType,
		Spot:             profile.Spot,
		UserData:         userData,
		Tags: map[string]string{
			"Name":          name,
			"ManagedBy":     "github-runner-provisioner",
			"RunnerProfile": sel.Profile,
		},
	})
	if err != nil {
		// No instance exists — nothing to reconcile via GSI3, just fail the
		// runner row outright and give the job back for retry.
		if markErr := s.store.MarkRunnerFailed(ctx, runnerID); markErr != nil {
			s.logger.Error("runner: mark failed runner after launch error", "runner_id", runnerID, "error", markErr)
		}
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: launch instance: %w", err)
	}

	// Durably record the instance ID before binding to the job — if the
	// process dies right here, ListStaleProvisioning + FindRunnerByInstanceID
	// can still find and reconcile this instance later.
	if err := s.store.SetInstanceID(ctx, runnerID, instanceID); err != nil {
		return fmt.Errorf("runner: record instance id: %w", err)
	}

	if err := s.store.BindToJob(ctx, runnerID, job.JobID, job.WorkflowRunID); err != nil {
		return fmt.Errorf("runner: bind runner to job: %w", err)
	}

	s.logger.Info("runner: provisioned new runner", "runner_id", runnerID, "instance_id", instanceID, "job_id", job.JobID, "profile", sel.Profile)
	return nil
}

// resolveAMI returns profile.AMI if pinned, otherwise resolves
// profile.AMILookup to the newest matching AMI via s.ami. config.Validate
// guarantees exactly one of the two is set.
func (s *Service) resolveAMI(ctx context.Context, profile config.Profile) (string, error) {
	if profile.AMI != "" {
		return profile.AMI, nil
	}
	return s.ami.LatestAMI(ctx, profile.AMILookup.OS, profile.AMILookup.Version, profile.Architecture)
}

func (s *Service) revertProvisioning(ctx context.Context, job *store.Job) {
	if err := s.store.RevertProvisioning(ctx, job); err != nil {
		s.logger.Error("runner: revert provisioning claim", "job_id", job.JobID, "error", err)
	}
}

// runnerName produces a short, deterministic, human-recognizable runner
// name. It doubles as the lookup key cleanup uses to resolve a GitHub-
// assigned runner ID (GitHub's registration API doesn't return one).
func runnerName(profile, runnerID string) string {
	short := runnerID
	if len(short) > 10 {
		short = short[len(short)-10:]
	}
	return fmt.Sprintf("%s-%s", profile, strings.ToLower(short))
}

// runnerLabels combines the fixed "self-hosted" label every GitHub
// self-hosted runner must carry with the profile's own (user-named) labels.
func runnerLabels(profileLabels []string) []string {
	set := map[string]struct{}{"self-hosted": {}}
	for _, l := range profileLabels {
		set[l] = struct{}{}
	}
	labels := make([]string, 0, len(set))
	for l := range set {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels
}
