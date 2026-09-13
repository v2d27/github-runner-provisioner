package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	ghclient "github.me/v2d27/provision-github-runner-on-demand/internal/github"
	"github.me/v2d27/provision-github-runner-on-demand/internal/store"
)

// Handler is the Lambda-runtime-agnostic core of the webhook endpoint:
// verify -> idempotency check -> normalize -> enqueue. cmd/webhook adapts
// this to the API Gateway HTTP API event shape.
type Handler struct {
	secret []byte
	store  *store.Client
	queue  *Queue
	logger *slog.Logger
}

func NewHandler(secret []byte, st *store.Client, q *Queue, logger *slog.Logger) *Handler {
	return &Handler{secret: secret, store: st, queue: q, logger: logger}
}

// Handle verifies, dedupes and forwards a webhook delivery. It returns the
// HTTP status code to send back to GitHub; a non-nil error is always paired
// with a 4xx/5xx status and should be logged by the caller.
func (h *Handler) Handle(ctx context.Context, headers map[string]string, body []byte) (int, error) {
	signature := headerValue(headers, "x-hub-signature-256")
	if err := ghclient.ValidateSignature(h.secret, body, signature); err != nil {
		return http.StatusUnauthorized, err
	}

	deliveryID := headerValue(headers, "x-github-delivery")
	if deliveryID == "" {
		return http.StatusBadRequest, fmt.Errorf("webhook: missing X-GitHub-Delivery header")
	}
	eventType := headerValue(headers, "x-github-event")

	event, err := ghclient.ParseWorkflowJobEvent(eventType, body)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if event == nil {
		// Not a workflow_job event — acknowledge and ignore, per
		// docs/infrastructure/enterprise-standard-upgrade.md section 25.
		return http.StatusOK, nil
	}

	normalized := Normalize(deliveryID, event)

	firstSeen, err := h.store.RecordDelivery(ctx, deliveryID, eventType, normalized.Action, normalized.JobID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !firstSeen {
		h.logger.Info("webhook: duplicate delivery, skipping", "delivery_id", deliveryID)
		return http.StatusOK, nil
	}

	payload, err := json.Marshal(normalized)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("webhook: marshal normalized event: %w", err)
	}
	if err := h.queue.Send(ctx, string(payload)); err != nil {
		return http.StatusInternalServerError, err
	}

	h.logger.Info("webhook: forwarded event", "delivery_id", deliveryID, "action", normalized.Action, "job_id", normalized.JobID)
	return http.StatusOK, nil
}

func headerValue(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
