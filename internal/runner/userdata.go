package runner

import (
	"fmt"
	"strings"

	"github.me/v2d27/github-runner-provisioner/internal/config"
)

// UserDataParams carries exactly what an EC2 instance needs to register
// itself as one or more runners. Per
// docs/infrastructure/request-github-runner-token-architecture.md, this is
// the only GitHub-related secret the instance ever sees — no App private
// key, JWT, or installation token.
//
// RegistrationTokens and RunnerNames each carry exactly RunnerCount entries,
// index-aligned: RegistrationTokens[i] registers as RunnerNames[i]. GitHub
// registration tokens are single-purpose per config.sh run, so every runner
// process needs its own.
type UserDataParams struct {
	Scope              config.Scope
	Organization       string
	Repository         string
	RegistrationTokens []string
	RunnerNames        []string
	// RunnerCount is len(RegistrationTokens)==len(RunnerNames): how many
	// independent runner processes (each its own OS user) to install, 1-2.
	RunnerCount int
	// VirtualRAMGB, when non-zero, backs a swapfile of this size (GB) —
	// see instance_expand_ram in RenderUserData.
	VirtualRAMGB int
	Labels       []string
	Group        string // empty = GitHub default group
	Architecture string // "x86_64" | "arm64"

	// RunnerID, ReadyToken and ReadyCallbackURL together let each installed
	// runner process notify internal/ready once it has registered with
	// GitHub and started — see store.MarkSlotReady. ReadyToken is compared
	// against store.Runner.ReadyToken; it is this platform's only means of
	// authenticating the callback, so the instance never needs a broader AWS
	// credential just to report its own readiness.
	RunnerID         string
	ReadyToken       string
	ReadyCallbackURL string
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

// RenderUserData produces the EC2 UserData script:
//
//  1. installs Docker and unzip (Ubuntu/apt only — every profile in
//     configs/runner.yaml today resolves to an Ubuntu AMI; a profile pointed
//     at an Amazon Linux/yum-based image is not supported by this script),
//  2. optionally backs a swapfile ("virtual RAM") if VirtualRAMGB is set,
//  3. installs, registers and starts p.RunnerCount (1-2) independent GitHub
//     Actions runner processes, each its own OS user granted passwordless
//     sudo and docker-group membership.
//
// Runners are not started with --ephemeral, since this platform reuses a
// runner across jobs for up to idle_timeout
// (docs/infrastructure/enterprise-standard-upgrade.md section 6).
func RenderUserData(p UserDataParams) string {
	var runnerGroupFlag string
	if p.Group != "" {
		runnerGroupFlag = fmt.Sprintf(" --runnergroup %q", p.Group)
	}

	arch := runnerArch(p.Architecture)
	labels := strings.Join(p.Labels, ",")
	url := p.url()

	var script strings.Builder
	fmt.Fprintf(&script, `#!/bin/bash
set -euxo pipefail

RUNNER_ARCH=%s
export DEBIAN_FRONTEND=noninteractive

# --- Docker + unzip ----------------------------------------------------------

apt-get update -y
apt-get install -y --no-install-recommends ca-certificates curl gnupg unzip

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update -y
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
`, arch)

	if p.VirtualRAMGB > 0 {
		fmt.Fprintf(&script, `
# --- Virtual RAM (disk-backed swap) ------------------------------------------

fallocate -l %dG /swapfile
chmod 600 /swapfile
mkswap /swapfile
swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
`, p.VirtualRAMGB)
	}

	script.WriteString(`
# --- GitHub Actions runner ----------------------------------------------------

RUNNER_VERSION=$(curl -fsSL https://api.github.com/repos/actions/runner/releases/latest | grep -o '"tag_name": *"v[^"]*"' | cut -d'"' -f4 | tr -d 'v')
curl -fsSL -o /tmp/actions-runner.tar.gz \
  "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${RUNNER_ARCH}-${RUNNER_VERSION}.tar.gz"
`)

	for i := 0; i < p.RunnerCount; i++ {
		user := fmt.Sprintf("github-runner-%d", i+1)
		home := fmt.Sprintf("/opt/actions-runner-%d", i+1)

		// Sent once config.sh (registration) and svc.sh start have both
		// succeeded — set -euxo pipefail above means the script already
		// aborted before reaching here if either failed, so a callback
		// firing at all is itself proof this runner process is live. A
		// failed callback (network hiccup, etc.) must not abort the rest of
		// the script — the `|| echo` keeps `set -e` from tripping — cleanup's
		// boot-timeout sweep is the backstop if the slot never confirms.
		readyBody := fmt.Sprintf(`{"runner_id":%q,"slot_index":%d,"token":%q}`, p.RunnerID, i, p.ReadyToken)

		fmt.Fprintf(&script, `
RUNNER_USER=%q
RUNNER_HOME=%q

id -u "$RUNNER_USER" >/dev/null 2>&1 || useradd --system --create-home --shell /bin/bash "$RUNNER_USER"
usermod -aG sudo,docker "$RUNNER_USER"
echo "$RUNNER_USER ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/90-$RUNNER_USER"
chmod 440 "/etc/sudoers.d/90-$RUNNER_USER"

mkdir -p "$RUNNER_HOME"
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

curl -fsS --retry 5 --retry-delay 3 --max-time 10 \
  -X POST %q \
  -H 'Content-Type: application/json' \
  -d '%s' \
  || echo "warning: runner-ready callback failed for slot %d" >&2
`, user, home, url, p.RegistrationTokens[i], p.RunnerNames[i], labels, runnerGroupFlag, p.ReadyCallbackURL, readyBody, i)
	}

	return script.String()
}
