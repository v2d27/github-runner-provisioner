// Package config loads and validates the platform's runner configuration.
//
// The single source of truth is configs/runner.yaml. Each environment's
// infrastructure/environments/<env>/terragrunt.hcl reads that file at apply time
// and passes its github/webhook/runner sections to every Lambda as the
// RUNNER_CONFIG_JSON environment variable (see
// infrastructure/modules/github-runner-on-aws/lambda.tf), so config changes are
// applied through the normal one-time Terraform setup rather than requiring
// code changes. Load() also accepts a YAML file directly, which is useful for
// local development and for the "one-time setup" workflow described in
// docs/infrastructure/enterprise-standard-upgrade.md.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scope determines whether runners are registered against a single
// repository or an entire organization. GitHub Runner Groups only exist at
// the organization/enterprise level, which is why Validate rejects a
// non-empty Group when Scope is ScopeRepository.
type Scope string

const (
	ScopeRepository   Scope = "repository"
	ScopeOrganization Scope = "organization"
)

const (
	// EnvConfigJSON, when set, takes priority over any YAML file and is how
	// Terraform hands the resolved configs/runner.yaml to each Lambda.
	EnvConfigJSON = "RUNNER_CONFIG_JSON"
	// EnvConfigPath is the fallback YAML file location, used for local
	// development and testing.
	EnvConfigPath     = "CONFIG_PATH"
	defaultConfigPath = "configs/runner.yaml"

	architectureX86_64 = "x86_64"
	architectureArm64  = "arm64"
)

// Config is the fully parsed, validated platform configuration.
type Config struct {
	GitHub  GitHubConfig  `yaml:"github" json:"github"`
	Webhook WebhookConfig `yaml:"webhook" json:"webhook"`
	Runner  RunnerConfig  `yaml:"runner" json:"runner"`
}

// GitHubConfig holds the identifiers needed to authenticate as a GitHub App.
// The private key itself is never stored here — it lives in Secrets Manager
// and is fetched at runtime (see internal/aws.Secrets and internal/github.App).
type GitHubConfig struct {
	AppID int64 `yaml:"app_id" json:"app_id"`
	// InstallationID may be 0, in which case internal/github.InstallationResolver
	// resolves and caches it dynamically from Runner.Organization/Repository.
	InstallationID int64 `yaml:"installation_id" json:"installation_id"`
	// PrivateKeySecretName is the Secrets Manager secret holding the GitHub
	// App's PEM private key.
	PrivateKeySecretName string `yaml:"private_key_secret_name" json:"private_key_secret_name"`
}

// WebhookConfig points at the Secrets Manager secret holding the webhook's
// HMAC signing secret, used to verify X-Hub-Signature-256.
type WebhookConfig struct {
	SecretName string `yaml:"secret_name" json:"secret_name"`
}

// RunnerConfig is the platform policy for how runners are provisioned,
// grouped and reused. See docs/infrastructure/enterprise-standard-upgrade.md
// sections 3-5.
type RunnerConfig struct {
	Scope        Scope              `yaml:"scope" json:"scope"`
	Organization string             `yaml:"organization" json:"organization"`
	Repository   string             `yaml:"repository" json:"repository"`
	Group        string             `yaml:"group" json:"group"`
	IdleTimeout  Duration           `yaml:"idle_timeout" json:"idle_timeout"`
	Profiles     map[string]Profile `yaml:"profiles" json:"profiles"`
}

// Profile maps a set of user-named workflow_job labels to a concrete EC2
// shape. A workflow_job matches this profile if its runs-on: labels contain
// at least one of Labels (see internal/runner.ResolveProfile) — Labels are
// not required to all be present. config.Validate rejects any label that
// appears in more than one profile, since that would make resolution
// ambiguous.
//
// There is no platform-wide prefix: if you want to namespace labels to
// avoid colliding with an unrelated workflow in the same GitHub
// organization, name them yourself, e.g. "<project_name>-<profile>" such as
// "erp-sport-amd64".
//
// Exactly one of AMI or AMILookup must be set: a pinned AMI ID for
// reproducible, review-gated rollouts, or an os/version pair the provision
// Lambda resolves to the newest matching AMI at launch time (see
// internal/aws.AMIResolver) so the AMI never goes stale without anyone
// noticing.
type Profile struct {
	// Labels may be left empty; applyDefaults fills it in with this
	// profile's own map key before Validate runs.
	Labels       []string   `yaml:"labels" json:"labels"`
	Architecture string     `yaml:"architecture" json:"architecture"`
	AMI          string     `yaml:"ami,omitempty" json:"ami,omitempty"`
	AMILookup    *AMILookup `yaml:"ami_lookup,omitempty" json:"ami_lookup,omitempty"`
	InstanceType string     `yaml:"instance_type" json:"instance_type"`
	Spot         bool       `yaml:"spot" json:"spot"`
}

// AMILookup resolves to the newest AMI for an os/version at launch time
// instead of pinning a specific AMI ID. See internal/aws.AMIResolver for the
// supported os values and how each is resolved.
type AMILookup struct {
	OS      string `yaml:"os" json:"os"`
	Version string `yaml:"version" json:"version"`
}

// Load resolves configuration from the environment: RUNNER_CONFIG_JSON takes
// priority (the deployed Lambda path), falling back to the YAML file at
// CONFIG_PATH (or defaultConfigPath) for local development. The result is
// always validated before it's returned.
func Load() (*Config, error) {
	var (
		cfg *Config
		err error
	)

	if raw := os.Getenv(EnvConfigJSON); raw != "" {
		cfg, err = LoadFromJSON([]byte(raw))
	} else {
		path := os.Getenv(EnvConfigPath)
		if path == "" {
			path = defaultConfigPath
		}
		cfg, err = LoadFromYAMLFile(path)
	}
	if err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadFromYAMLFile reads and parses (but does not validate) a runner.yaml file.
func LoadFromYAMLFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadFromJSON parses (but does not validate) a JSON-encoded configuration,
// as handed to Lambdas via the RUNNER_CONFIG_JSON environment variable.
func LoadFromJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse RUNNER_CONFIG_JSON: %w", err)
	}
	return &cfg, nil
}

// Validate enforces the invariants the rest of the platform relies on. It
// first fills in any profile with no declared labels (defaulting to its own
// map key), then collects every violation instead of stopping at the
// first, so a misconfiguration is fully diagnosed in one pass — important
// for a one-time, human-edited YAML setup flow.
func (c *Config) Validate() error {
	c.applyDefaults()

	var errs []string

	if c.GitHub.AppID <= 0 {
		errs = append(errs, "github.app_id is required")
	}
	if strings.TrimSpace(c.GitHub.PrivateKeySecretName) == "" {
		errs = append(errs, "github.private_key_secret_name is required")
	}
	if strings.TrimSpace(c.Webhook.SecretName) == "" {
		errs = append(errs, "webhook.secret_name is required")
	}

	switch c.Runner.Scope {
	case ScopeRepository, ScopeOrganization:
	default:
		errs = append(errs, fmt.Sprintf("runner.scope must be %q or %q, got %q", ScopeRepository, ScopeOrganization, c.Runner.Scope))
	}

	if strings.TrimSpace(c.Runner.Organization) == "" {
		errs = append(errs, "runner.organization is required")
	}
	if c.Runner.Scope == ScopeRepository && strings.TrimSpace(c.Runner.Repository) == "" {
		errs = append(errs, "runner.repository is required when runner.scope is \"repository\"")
	}
	if c.Runner.Scope == ScopeRepository && c.Runner.Group != "" {
		// GitHub Runner Groups only exist at organization/enterprise level —
		// there is no /repos/{owner}/{repo}/actions/runner-groups endpoint.
		errs = append(errs, "runner.group must be empty when runner.scope is \"repository\" (GitHub runner groups only exist at organization level)")
	}

	if c.Runner.IdleTimeout.Duration() <= 0 {
		errs = append(errs, "runner.idle_timeout must be greater than zero")
	}

	if len(c.Runner.Profiles) == 0 {
		errs = append(errs, "runner.profiles must declare at least one profile")
	}
	errs = append(errs, validateProfiles(c.Runner.Profiles)...)

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// applyDefaults fills in values that have a sensible default so Validate
// doesn't need to treat them as required. Currently: a profile with no
// declared labels defaults to a single label equal to its own map key.
func (c *Config) applyDefaults() {
	for name, p := range c.Runner.Profiles {
		if len(p.Labels) == 0 {
			p.Labels = []string{name}
			c.Runner.Profiles[name] = p
		}
	}
}

// validateProfiles requires every label to appear in at most one profile.
// Profile resolution matches a workflow_job if its runs-on: labels contain
// ANY of a profile's Labels (see internal/runner.ResolveProfile) — not all
// of them — so even a single label shared between two profiles would make
// resolution ambiguous, not just two profiles sharing an identical set.
func validateProfiles(profiles map[string]Profile) []string {
	var errs []string
	labelOwner := map[string]string{}

	for name, p := range profiles {
		switch p.Architecture {
		case architectureX86_64, architectureArm64:
		default:
			errs = append(errs, fmt.Sprintf("runner.profiles[%s].architecture must be %q or %q, got %q", name, architectureX86_64, architectureArm64, p.Architecture))
		}
		hasAMI := strings.TrimSpace(p.AMI) != ""
		hasLookup := p.AMILookup != nil
		switch {
		case hasAMI && hasLookup:
			errs = append(errs, fmt.Sprintf("runner.profiles[%s] must set exactly one of ami or ami_lookup, not both", name))
		case !hasAMI && !hasLookup:
			errs = append(errs, fmt.Sprintf("runner.profiles[%s] must set one of ami or ami_lookup", name))
		case hasLookup:
			if strings.TrimSpace(p.AMILookup.OS) == "" {
				errs = append(errs, fmt.Sprintf("runner.profiles[%s].ami_lookup.os is required", name))
			}
			if strings.TrimSpace(p.AMILookup.Version) == "" {
				errs = append(errs, fmt.Sprintf("runner.profiles[%s].ami_lookup.version is required", name))
			}
		}
		if strings.TrimSpace(p.InstanceType) == "" {
			errs = append(errs, fmt.Sprintf("runner.profiles[%s].instance_type is required", name))
		}

		for _, label := range p.Labels {
			if other, ok := labelOwner[label]; ok {
				errs = append(errs, fmt.Sprintf("runner.profiles[%s] and runner.profiles[%s] both declare label %q — profile resolution would be ambiguous", other, name, label))
				continue
			}
			labelOwner[label] = name
		}
	}
	return errs
}
