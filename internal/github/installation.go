package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v88/github"

	"github.me/v2d27/github-runner-provisioner/internal/config"
)

// ResolveInstallationID looks up the installation ID for the configured
// organization or repository using an App-authenticated (JWT) client.
// Callers should prefer a statically configured ID
// (config.GitHubConfig.InstallationID) and only fall back to this when it is
// unset (0), since it costs a GitHub API call — see Provider.Client, which
// also persists the result via an InstallationCache to avoid repeating it.
func ResolveInstallationID(ctx context.Context, appClient *github.Client, cfg config.RunnerConfig) (int64, error) {
	if cfg.Scope == config.ScopeOrganization {
		inst, _, err := appClient.Apps.GetOrganizationInstallation(ctx, cfg.Organization)
		if err != nil {
			return 0, fmt.Errorf("github: resolve organization installation for %s: %w", cfg.Organization, err)
		}
		return inst.GetID(), nil
	}

	inst, _, err := appClient.Apps.GetRepositoryInstallation(ctx, cfg.Organization, cfg.Repository)
	if err != nil {
		return 0, fmt.Errorf("github: resolve repository installation for %s/%s: %w", cfg.Organization, cfg.Repository, err)
	}
	return inst.GetID(), nil
}
