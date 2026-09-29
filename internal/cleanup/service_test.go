package cleanup

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.me/v2d27/github-runner-provisioner/internal/aws"
	"github.me/v2d27/github-runner-provisioner/internal/store"
)

func TestEligibleForTermination(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const autoTerminatingTime = time.Hour
	young := now.Add(-10 * time.Minute).Unix()
	old := now.Add(-2 * time.Hour).Unix()
	expired := now.Add(-time.Minute).Unix()
	pending := now.Add(time.Minute).Unix()

	idle := func(terminateAfter int64) store.Slot {
		return store.Slot{Status: store.SlotIdle, TerminateAfter: terminateAfter}
	}
	busy := store.Slot{Status: store.SlotBusy, JobID: 42}

	tests := []struct {
		name      string
		createdAt int64
		slots     []store.Slot
		want      bool
	}{
		{"no slots", old, nil, false},
		{"young, every slot idle and expired", young, []store.Slot{idle(expired), idle(expired)}, true},
		{"young, one slot's idle timer still running", young, []store.Slot{idle(expired), idle(pending)}, false},
		{"young, one slot busy", young, []store.Slot{idle(expired), busy}, false},
		{"old, idle timers still running", old, []store.Slot{idle(pending), idle(pending)}, true},
		// A BUSY slot on an instance past auto_terminating_time is what a
		// missed workflow_job.completed webhook looks like — it must not pin
		// the instance forever; terminateExpiredRunner makes the final call.
		{"old, slot stuck busy", old, []store.Slot{idle(expired), busy}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &store.Runner{CreatedAt: tt.createdAt, Slots: tt.slots}
			if got := eligibleForTermination(r, now, autoTerminatingTime); got != tt.want {
				t.Errorf("eligibleForTermination() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTracks(t *testing.T) {
	const id = "i-0123"
	tests := []struct {
		name string
		r    *store.Runner
		want bool
	}{
		{"no record", nil, false},
		{"active, same instance", &store.Runner{Status: store.RunnerActive, InstanceID: id}, true},
		{"terminating, same instance", &store.Runner{Status: store.RunnerTerminating, InstanceID: id}, true},
		// The provision Lambda died before SetInstanceID landed.
		{"provisioning, instance id never recorded", &store.Runner{Status: store.RunnerProvisioning}, false},
		// RunInstances timed out client-side but actually launched.
		{"failed", &store.Runner{Status: store.RunnerFailed, InstanceID: id}, false},
		{"terminated", &store.Runner{Status: store.RunnerTerminated, InstanceID: id}, false},
		{"different instance", &store.Runner{Status: store.RunnerActive, InstanceID: "i-other"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tracks(tt.r, id); got != tt.want {
				t.Errorf("tracks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOrphanRunner(t *testing.T) {
	inst := aws.ManagedInstance{InstanceID: "i-0123", Tags: map[string]string{"Name": "runner-arm64-abc"}}

	got := orphanRunner(nil, "01ABC", inst)
	if got.InstanceID != "i-0123" || len(got.Slots) != 2 || got.Slots[0].Name != "runner-arm64-abc-1" || got.Slots[1].Name != "runner-arm64-abc-2" {
		t.Errorf("orphanRunner(no record) = %+v", got)
	}

	rec := &store.Runner{Slots: []store.Slot{{Name: "runner-arm64-abc-1"}}}
	got = orphanRunner(rec, "01ABC", inst)
	if got.InstanceID != "i-0123" || len(got.Slots) != 1 || got.Slots[0].Name != "runner-arm64-abc-1" {
		t.Errorf("orphanRunner(record) = %+v", got)
	}
	got.Slots[0].GitHubRunnerID = 7
	if rec.Slots[0].GitHubRunnerID != 0 {
		t.Error("orphanRunner must not alias the record's slots")
	}
}

func TestWorkerRunning(t *testing.T) {
	s := &Service{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Unix(1_800_000_000, 0)
	busy := map[string]aws.RunnerProcesses{"i-busy": {Listeners: 2, Workers: 1}, "i-idle": {Listeners: 2}}

	tests := []struct {
		name     string
		instance string
		age      time.Duration
		want     bool
	}{
		{"worker running", "i-busy", time.Hour, true},
		{"listeners only", "i-idle", time.Hour, false},
		{"not checked", "i-unknown", time.Hour, false},
		{"worker running past the ceiling", "i-busy", processBusyCeiling + time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &store.Runner{InstanceID: tt.instance, CreatedAt: now.Add(-tt.age).Unix()}
			if got := s.workerRunning(r, busy, now); got != tt.want {
				t.Errorf("workerRunning() = %v, want %v", got, tt.want)
			}
		})
	}
}
