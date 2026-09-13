package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/go-github/v88/github"

	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
)

// ErrRunnerNotFound indicates no runner with the given name is currently
// registered — treated as "already gone" by cleanup, not an error.
var ErrRunnerNotFound = errors.New("github: runner not found")

// RegistrationToken is a short-lived, single-use token an EC2 instance uses
// to register itself as a runner. Generated here and handed to the instance
// via UserData — the instance never sees the App private key, JWT, or
// installation token that produced it.
type RegistrationToken struct {
	Token     string
	ExpiresAt time.Time
}

// CreateRegistrationToken requests a new runner registration token, scoped
// to the repository or organization per cfg.Scope.
func CreateRegistrationToken(ctx context.Context, client *github.Client, cfg config.RunnerConfig) (*RegistrationToken, error) {
	var (
		tok *github.RegistrationToken
		err error
	)
	if cfg.Scope == config.ScopeOrganization {
		tok, _, err = client.Actions.CreateOrganizationRegistrationToken(ctx, cfg.Organization)
	} else {
		tok, _, err = client.Actions.CreateRegistrationToken(ctx, cfg.Organization, cfg.Repository)
	}
	if err != nil {
		return nil, fmt.Errorf("github: create registration token: %w", err)
	}
	return &RegistrationToken{Token: tok.GetToken(), ExpiresAt: tok.GetExpiresAt().Time}, nil
}

// RemoveRunner deregisters a runner by its GitHub-assigned runner ID, scoped
// per cfg.Scope. A 404 (already removed) is treated as success so cleanup
// stays idempotent under retries.
func RemoveRunner(ctx context.Context, client *github.Client, cfg config.RunnerConfig, runnerID int64) error {
	var (
		resp *github.Response
		err  error
	)
	if cfg.Scope == config.ScopeOrganization {
		resp, err = client.Actions.RemoveOrganizationRunner(ctx, cfg.Organization, runnerID)
	} else {
		resp, err = client.Actions.RemoveRunner(ctx, cfg.Organization, cfg.Repository, runnerID)
	}
	if err != nil && !isNotFound(resp, err) {
		return fmt.Errorf("github: remove runner %d: %w", runnerID, err)
	}
	return nil
}

// GetRunner fetches a runner's current status/busy state, scoped per
// cfg.Scope.
func GetRunner(ctx context.Context, client *github.Client, cfg config.RunnerConfig, runnerID int64) (*github.Runner, error) {
	var (
		runner *github.Runner
		err    error
	)
	if cfg.Scope == config.ScopeOrganization {
		runner, _, err = client.Actions.GetOrganizationRunner(ctx, cfg.Organization, runnerID)
	} else {
		runner, _, err = client.Actions.GetRunner(ctx, cfg.Organization, cfg.Repository, runnerID)
	}
	if err != nil {
		return nil, fmt.Errorf("github: get runner %d: %w", runnerID, err)
	}
	return runner, nil
}

// FindRunnerByName resolves a runner's GitHub-assigned ID by the name this
// platform gave it at registration (see internal/runner.runnerName).
// GitHub's registration API never returns a runner ID directly — this
// server-side name filter is how cleanup and orphan reconciliation recover
// it. Returns ErrRunnerNotFound if no such runner is currently registered.
func FindRunnerByName(ctx context.Context, client *github.Client, cfg config.RunnerConfig, name string) (*github.Runner, error) {
	opts := &github.ListRunnersOptions{Name: github.Ptr(name)}

	var (
		runners *github.Runners
		err     error
	)
	if cfg.Scope == config.ScopeOrganization {
		runners, _, err = client.Actions.ListOrganizationRunners(ctx, cfg.Organization, opts)
	} else {
		runners, _, err = client.Actions.ListRunners(ctx, cfg.Organization, cfg.Repository, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("github: list runners named %q: %w", name, err)
	}
	for _, r := range runners.Runners {
		if r.GetName() == name {
			return r, nil
		}
	}
	return nil, ErrRunnerNotFound
}

func isNotFound(resp *github.Response, err error) bool {
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return true
	}
	var ghErr *github.ErrorResponse
	return errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusNotFound
}
