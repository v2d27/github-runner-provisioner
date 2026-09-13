package github

import (
	"fmt"

	"github.com/google/go-github/v88/github"
)

// WorkflowJobEventType is the X-GitHub-Event header value this platform
// reacts to. Every other event type is ignored by the webhook Lambda.
const WorkflowJobEventType = "workflow_job"

// ValidateSignature verifies the X-Hub-Signature-256 header against the
// webhook's HMAC secret. Callers must reject the request if this returns an
// error — never parse or act on a payload before this succeeds.
func ValidateSignature(secret []byte, payload []byte, signatureHeader string) error {
	if signatureHeader == "" {
		return fmt.Errorf("github: missing X-Hub-Signature-256 header")
	}
	if err := github.ValidateSignature(signatureHeader, payload, secret); err != nil {
		return fmt.Errorf("github: invalid webhook signature: %w", err)
	}
	return nil
}

// ParseWorkflowJobEvent parses a workflow_job webhook payload. It returns
// (nil, nil) for any other event type so callers can ack-and-ignore instead
// of treating it as an error.
func ParseWorkflowJobEvent(eventType string, payload []byte) (*github.WorkflowJobEvent, error) {
	if eventType != WorkflowJobEventType {
		return nil, nil
	}

	parsed, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		return nil, fmt.Errorf("github: parse workflow_job payload: %w", err)
	}
	event, ok := parsed.(*github.WorkflowJobEvent)
	if !ok {
		return nil, fmt.Errorf("github: unexpected payload type %T for workflow_job", parsed)
	}
	return event, nil
}
