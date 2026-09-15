package runner

import "github.me/v2d27/github-runner-provisioner/internal/store"

// This file documents (and lets callers assert) the legal state transitions
// enforced by internal/store's conditional/transactional writes. The
// DynamoDB conditions are the actual enforcement; these tables exist so
// service.go can log a clear warning when an incoming GitHub event implies a
// transition that shouldn't be possible, instead of silently no-op'ing.
//
// See the RunnerStatus doc comment in internal/store/runner.go: busy/idle is
// a per-slot concept (store.Slot), not tracked by this outer instance-level
// status — PROVISIONING goes straight to ACTIVE once the instance is up,
// regardless of how many of its slots are currently busy vs idle.
var validRunnerTransitions = map[store.RunnerStatus][]store.RunnerStatus{
	store.RunnerProvisioning: {store.RunnerActive, store.RunnerFailed, store.RunnerTerminating},
	store.RunnerActive:       {store.RunnerTerminating},
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
	// QUEUED/FAILED here is RevertAllocation: the runner this job was bound
	// to never confirmed ready within runner.boot_timeout (see
	// internal/cleanup.reconcileStuckStarting).
	store.JobAllocated: {store.JobCompleted, store.JobQueued, store.JobFailed},
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
