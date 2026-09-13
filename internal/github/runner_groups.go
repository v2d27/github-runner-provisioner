package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v88/github"

	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
)

// ValidateRunnerGroup confirms the configured runner group exists before any
// EC2 instance is provisioned, so a typo in runner.yaml fails fast rather
// than producing an orphaned EC2 instance whose runner registration
// silently fails (enterprise-standard-upgrade.md section 21).
//
// GitHub Runner Groups only exist at organization/enterprise level. Callers
// must not invoke this for ScopeRepository — config.Validate already rejects
// a non-empty group in that case, so this only ever runs for
// ScopeOrganization.
func ValidateRunnerGroup(ctx context.Context, client *github.Client, cfg config.RunnerConfig) error {
	if cfg.Group == "" {
		return nil // GitHub's default group, nothing to validate
	}

	groups, _, err := client.Actions.ListOrganizationRunnerGroups(ctx, cfg.Organization, nil)
	if err != nil {
		return fmt.Errorf("github: list runner groups for %s: %w", cfg.Organization, err)
	}
	for _, g := range groups.RunnerGroups {
		if g.GetName() == cfg.Group {
			return nil
		}
	}
	return fmt.Errorf("github: runner group %q not found in organization %s", cfg.Group, cfg.Organization)
}
