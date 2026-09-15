// Package github wraps GitHub App authentication and the small slice of the
// REST API this platform needs (installation resolution, registration
// tokens, runner/runner-group management, webhook verification).
//
// Per docs/infrastructure/request-github-runner-token-architecture.md: there
// is no PAT anywhere. The App private key lives in Secrets Manager and is
// only ever held in memory by the webhook/provision/cleanup Lambdas; EC2
// instances receive nothing but a short-lived registration token.
package github

import (
	"fmt"
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
)

// AppCredentials identifies the GitHub App used to authenticate every
// request this platform makes.
type AppCredentials struct {
	AppID      int64
	PrivateKey []byte
}

// NewAppClient returns a client authenticated as the GitHub App itself via a
// signed JWT. It can only call App-level endpoints (e.g. resolving an
// installation ID) — never repository/organization runner APIs, which
// require an installation token from NewInstallationClient.
func NewAppClient(creds AppCredentials) (*github.Client, error) {
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, creds.AppID, creds.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github: build app transport: %w", err)
	}
	client, err := github.NewClient(github.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		return nil, fmt.Errorf("github: new app client: %w", err)
	}
	return client, nil
}

// NewInstallationClient returns a client authenticated as a specific
// installation. The underlying transport mints and refreshes installation
// access tokens automatically as they expire.
func NewInstallationClient(creds AppCredentials, installationID int64) (*github.Client, error) {
	tr, err := ghinstallation.New(http.DefaultTransport, creds.AppID, installationID, creds.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github: build installation transport: %w", err)
	}
	client, err := github.NewClient(github.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		return nil, fmt.Errorf("github: new installation client: %w", err)
	}
	return client, nil
}
