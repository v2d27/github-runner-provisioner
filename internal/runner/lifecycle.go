package runner

import "github.me/v2d27/github-runner-provisioner/internal/store"

// This file documents (and lets callers assert) the legal state transitions
// enforced by internal/store's conditional/transactional writes. The
// DynamoDB conditions are the actual enforcement; these tables exist so
// service.go can log a clear warning when an incoming GitHub event implies a
// transition that shouldn't be possible, instead of silently no-op'ing.
//
// See the RunnerStatus doc comment in internal/store/runner.go for why
// PROVISIONING goes straight to BUSY rather than through a separate STARTING
// state.
var validRunnerTransitions = map[store.RunnerStatus][]store.RunnerStatus{
	store.RunnerProvisioning: {store.RunnerBusy, store.RunnerFailed, store.RunnerTerminating},
	store.RunnerBusy:         {store.RunnerIdle, store.RunnerTerminating},
	store.RunnerIdle:         {store.RunnerBusy, store.RunnerTerminating},
	store.RunnerTerminating:  {store.RunnerTerminated},
}

// IsValidRunnerTransition reports whether moving a runner from `from` to
// `to` is expected.
func IsValidRunnerTransition(from, to store.RunnerStatus) bool {
	for _, allowed := range validRunnerTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

var validJobTransitions = map[store.JobStatus][]store.JobStatus{
	store.JobQueued:       {store.JobAllocated, store.JobProvisioning, store.JobIgnored},
	store.JobProvisioning: {store.JobAllocated, store.JobQueued, store.JobFailed},
	store.JobAllocated:    {store.JobCompleted},
}

// IsValidJobTransition reports whether moving a job from `from` to `to` is
// expected.
func IsValidJobTransition(from, to store.JobStatus) bool {
	for _, allowed := range validJobTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}
