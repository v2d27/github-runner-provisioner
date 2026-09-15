package github

import (
	"context"
	"sync"

	"github.com/google/go-github/v88/github"

	"github.me/v2d27/github-runner-provisioner/internal/config"
)

// InstallationCache persists a resolved installation ID across cold starts
// so it doesn't need to be re-resolved (and re-charged against GitHub API
// rate limits) on every invocation. Implemented by internal/store.ConfigCache.
type InstallationCache interface {
	GetInstallationID(ctx context.Context) (id int64, ok bool, err error)
	PutInstallationID(ctx context.Context, id int64) error
}

// Provider lazily builds and memoizes an installation-authenticated GitHub
// client for the lifetime of the Lambda execution environment (i.e. across
// warm invocations, reset on cold start).
type Provider struct {
	creds AppCredentials
	cfg   *config.Config
	cache InstallationCache // may be nil

	mu     sync.Mutex
	client *github.Client
}

func NewProvider(creds AppCredentials, cfg *config.Config, cache InstallationCache) *Provider {
	return &Provider{creds: creds, cfg: cfg, cache: cache}
}

// Client returns the memoized installation client, resolving and caching the
// installation ID on first use.
func (p *Provider) Client(ctx context.Context) (*github.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return p.client, nil
	}

	id := p.cfg.GitHub.InstallationID
	if id == 0 && p.cache != nil {
		if cached, ok, err := p.cache.GetInstallationID(ctx); err == nil && ok {
			id = cached
		}
	}

	if id == 0 {
		appClient, err := NewAppClient(p.creds)
		if err != nil {
			return nil, err
		}
		resolved, err := ResolveInstallationID(ctx, appClient, p.cfg.Runner)
		if err != nil {
			return nil, err
		}
		id = resolved
		if p.cache != nil {
			// Best-effort: a failed cache write just means the next cold
			// start resolves again, it's not fatal to this request.
			_ = p.cache.PutInstallationID(ctx, id)
		}
	}

	client, err := NewInstallationClient(p.creds, id)
	if err != nil {
		return nil, err
	}
	p.client = client
	return client, nil
}
