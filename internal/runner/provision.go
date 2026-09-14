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

	// One registration token per runner process the instance will run
	// (profile.RunnerCount, 1-2) — each config.sh registration consumes its
	// own token.
	regTokens := make([]string, profile.RunnerCount)
	for i := range regTokens {
		regToken, err := ghclient.CreateRegistrationToken(ctx, client, s.cfg.Runner)
		if err != nil {
			s.revertProvisioning(ctx, job)
			return fmt.Errorf("runner: create registration token: %w", err)
		}
		regTokens[i] = regToken.Token
	}

	imageID, err := s.resolveAMI(ctx, profile)
	if err != nil {
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: resolve ami: %w", err)
	}

	runnerID := store.NewRunnerID()
	base, names := runnerNames(sel.Profile, runnerID, profile.RunnerCount)
	labels := runnerLabels(profile.Labels)

	runnerRow := store.NewProvisioningRunner(sel, runnerID, names, labels, profile.Architecture, profile.InstanceType, imageID)
	if err := s.store.CreateProvisioning(ctx, runnerRow); err != nil {
		s.revertProvisioning(ctx, job)
		return fmt.Errorf("runner: create provisioning row: %w", err)
	}

	userData := RenderUserData(UserDataParams{
		Scope:              s.cfg.Runner.Scope,
		Organization:       s.cfg.Runner.Organization,
		Repository:         s.cfg.Runner.Repository,
		RegistrationTokens: regTokens,
		RunnerNames:        names,
		RunnerCount:        profile.RunnerCount,
		VirtualRAMGB:       profile.VirtualRAMGB,
		Labels:             labels,
		Group:              s.cfg.Runner.Group,
		Architecture:       profile.Architecture,
	})

	instanceID, err := s.ec2.RunInstance(ctx, aws.LaunchInput{
		LaunchTemplateID: s.launchTemplateID,
		ImageID:          imageID,
		InstanceType:     profile.InstanceType,
		Spot:             profile.Spot,
		DiskSizeGB:       profile.DiskSizeGB,
		UserData:         userData,
		Tags: map[string]string{
			// The bare base name, not a per-slot name (e.g. not names[0]) —
			// one EC2 instance can host multiple runner slots
			// (profile.RunnerCount), so tagging it with a single slot's
			// identity would be misleading.
			"Name":          base,
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

	if err := s.store.BindToJob(ctx, runnerID, job.JobID, job.WorkflowRunID, sel, profile.RunnerCount, s.cfg.Runner.IdleTimeout.Duration()); err != nil {
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

// runnerNames returns the instance's bare base name (used for the EC2 Name
// tag — never suffixed, since one instance can host multiple runner slots)
// alongside one GitHub-registration name per runner process it will run
// (count — config.Profile.RunnerCount, 1-2), e.g. base "amd64-abc1234567"
// with names "amd64-abc1234567-1", "amd64-abc1234567-2". Each name doubles
// as a lookup key cleanup uses to resolve its GitHub-assigned runner ID
// (GitHub's registration API doesn't return one directly) — see
// store.Slot.Name.
func runnerNames(profile, runnerID string, count int) (base string, names []string) {
	short := runnerID
	if len(short) > 10 {
		short = short[len(short)-10:]
	}
	base = fmt.Sprintf("%s-%s", profile, strings.ToLower(short))

	names = make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%d", base, i+1)
	}
	return base, names
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
