package runner

import (
	"fmt"
	"strings"

	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
)

// UserDataParams carries exactly what an EC2 instance needs to register
// itself as a runner. Per
// docs/infrastructure/request-github-runner-token-architecture.md, this is
// the only GitHub-related secret the instance ever sees — no App private
// key, JWT, or installation token.
type UserDataParams struct {
	Scope             config.Scope
	Organization      string
	Repository        string
	RegistrationToken string
	RunnerName        string
	Labels            []string
	Group             string // empty = GitHub default group
	Architecture      string // "x86_64" | "arm64"
}

func (p UserDataParams) url() string {
	if p.Scope == config.ScopeOrganization {
		return fmt.Sprintf("https://github.com/%s", p.Organization)
	}
	return fmt.Sprintf("https://github.com/%s/%s", p.Organization, p.Repository)
}

// runnerArch maps our config.Profile.Architecture values to the arch token
// used in actions/runner release asset names.
func runnerArch(architecture string) string {
	if architecture == "arm64" {
		return "arm64"
	}
	return "x64"
}

// RenderUserData produces the EC2 UserData script: install the GitHub
// Actions runner, register it with the one-time registration token, and
// start it as a systemd service. The runner is not started with
// --ephemeral, since this platform reuses a runner across jobs for up to
// idle_timeout (docs/infrastructure/enterprise-standard-upgrade.md section 6).
func RenderUserData(p UserDataParams) string {
	var runnerGroupFlag string
	if p.Group != "" {
		runnerGroupFlag = fmt.Sprintf(" --runnergroup %q", p.Group)
	}

	arch := runnerArch(p.Architecture)
	labels := strings.Join(p.Labels, ",")

	return fmt.Sprintf(`#!/bin/bash
set -euxo pipefail

RUNNER_USER=github-runner
RUNNER_HOME=/opt/actions-runner
RUNNER_ARCH=%s

id -u "$RUNNER_USER" >/dev/null 2>&1 || useradd --system --create-home --shell /bin/bash "$RUNNER_USER"
mkdir -p "$RUNNER_HOME"

RUNNER_VERSION=$(curl -fsSL https://api.github.com/repos/actions/runner/releases/latest | grep -o '"tag_name": *"v[^"]*"' | cut -d'"' -f4 | tr -d 'v')
curl -fsSL -o /tmp/actions-runner.tar.gz \
  "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${RUNNER_ARCH}-${RUNNER_VERSION}.tar.gz"
tar xzf /tmp/actions-runner.tar.gz -C "$RUNNER_HOME"
chown -R "$RUNNER_USER:$RUNNER_USER" "$RUNNER_HOME"

sudo -u "$RUNNER_USER" -- "$RUNNER_HOME"/config.sh \
  --url %q \
  --token %q \
  --name %q \
  --labels %q \
  --unattended \
  --replace%s

cd "$RUNNER_HOME"
./svc.sh install "$RUNNER_USER"
./svc.sh start
`, arch, p.url(), p.RegistrationToken, p.RunnerName, labels, runnerGroupFlag)
}
