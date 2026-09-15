// Package webhook implements the webhook Lambda's request handling:
// signature verification, delivery idempotency, and normalizing a
// workflow_job payload into the wire format pushed to SQS.
package webhook

import "github.com/google/go-github/v88/github"

// NormalizedEvent is the SQS message body cmd/provision consumes. Keeping
// this independent of github.WorkflowJobEvent decouples the queue's wire
// format from the go-github library version.
type NormalizedEvent struct {
	EventID       string   `json:"event_id"`
	Action        string   `json:"action"`
	JobID         int64    `json:"job_id"`
	WorkflowRunID int64    `json:"workflow_run_id"`
	Organization  string   `json:"organization"`
	Repository    string   `json:"repository"`
	Labels        []string `json:"labels"`
}

// Normalize converts a parsed workflow_job event plus its delivery ID into
// the queue wire format. Profile resolution happens downstream in
// cmd/provision — the webhook forwards every workflow_job event regardless
// of whether any profile's labels match.
func Normalize(deliveryID string, ev *github.WorkflowJobEvent) NormalizedEvent {
	org := ev.GetOrg().GetLogin()
	repo := ""
	if r := ev.GetRepo(); r != nil {
		repo = r.GetName()
		if org == "" && r.GetOwner() != nil {
			org = r.GetOwner().GetLogin()
		}
	}

	job := ev.GetWorkflowJob()
	return NormalizedEvent{
		EventID:       deliveryID,
		Action:        ev.GetAction(),
		JobID:         job.GetID(),
		WorkflowRunID: job.GetRunID(),
		Organization:  org,
		Repository:    repo,
		Labels:        job.GetLabels(),
	}
}
