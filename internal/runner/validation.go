// Package runner is the domain orchestrator: given a normalized workflow_job
// event, it resolves a runner profile, allocates or provisions a runner,
// and drives the lifecycle transitions in internal/store.
package runner

import (
	"errors"

	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
)

// ErrNoMatchingProfile indicates no configured profile has any label in
// common with the incoming workflow_job's labels.
var ErrNoMatchingProfile = errors.New("runner: no matching profile")

// ResolveProfile finds the profile that shares at least one label with the
// incoming workflow_job labels — profiles are not required to match every
// label, just one. config.Validate already rejects any label declared by
// more than one profile, so at most one match is possible here.
//
// There's no separate platform-wide prefix: profile Labels are entirely
// user-named in configs/runner.yaml (defaulting to the profile's own key if
// left empty). Namespace them yourself if you need to avoid colliding with
// an unrelated workflow in the same GitHub organization, e.g.
// "<project_name>-<profile>" such as "erp-sport-amd64".
func ResolveProfile(cfg config.RunnerConfig, labels []string) (name string, profile config.Profile, err error) {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[l] = struct{}{}
	}

	for candidateName, candidate := range cfg.Profiles {
		if labelSetIntersects(candidate.Labels, set) {
			return candidateName, candidate, nil
		}
	}
	return "", config.Profile{}, ErrNoMatchingProfile
}

func labelSetIntersects(candidateLabels []string, have map[string]struct{}) bool {
	for _, l := range candidateLabels {
		if _, ok := have[l]; ok {
			return true
		}
	}
	return false
}
