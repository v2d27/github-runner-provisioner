// Package ready implements the runner-ready callback: each runner process an
// EC2 instance's userdata script installs calls this exactly once, right
// after it registers with GitHub and starts, clearing its store.Slot's
// starting_since marker (see store.MarkSlotReady). This does not gate
// allocation — store.BindToJob already made the slot busy/idle and
// allocatable the moment the instance launched, since reusing a
// soon-to-be-ready slot beats waiting on a brand new instance that would
// take just as long to boot. It exists purely so internal/cleanup can tell
// a slot whose install genuinely failed (starting_since left uncleared past
// runner.boot_timeout) apart from one that's simply busy or idle as normal.
package ready

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.me/v2d27/github-runner-provisioner/internal/store"
)

// Request is the JSON body an instance's userdata script posts once one of
// its runner processes registers and starts (see
// internal/runner.RenderUserData).
type Request struct {
	RunnerID  string `json:"runner_id"`
	SlotIndex int    `json:"slot_index"`
	Token     string `json:"token"`
}

// Handler is the Lambda-runtime-agnostic core of the runner-ready endpoint.
// cmd/ready adapts this to the API Gateway HTTP API event shape.
type Handler struct {
	store  *store.Client
	logger *slog.Logger
}

func NewHandler(st *store.Client, logger *slog.Logger) *Handler {
	return &Handler{store: st, logger: logger}
}

// Handle validates req and marks the corresponding slot ready. It returns
// the HTTP status to send back to the instance; a non-nil error is always
// paired with a 4xx/5xx status and should be logged by the caller.
func (h *Handler) Handle(ctx context.Context, body []byte) (int, error) {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return http.StatusBadRequest, fmt.Errorf("ready: decode request: %w", err)
	}
	if req.RunnerID == "" || req.Token == "" {
		return http.StatusBadRequest, errors.New("ready: runner_id and token are required")
	}

	runner, err := h.store.GetRunner(ctx, req.RunnerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return http.StatusNotFound, err
		}
		return http.StatusInternalServerError, err
	}

	// Constant-time comparison: this token is the only thing standing
	// between any caller on the internet and the ability to mark a runner
	// slot ready.
	if subtle.ConstantTimeCompare([]byte(runner.ReadyToken), []byte(req.Token)) != 1 {
		return http.StatusForbidden, fmt.Errorf("ready: invalid token for runner %s", req.RunnerID)
	}
	if req.SlotIndex < 0 || req.SlotIndex >= len(runner.Slots) {
		return http.StatusBadRequest, fmt.Errorf("ready: slot_index %d out of range for runner %s (0-%d)", req.SlotIndex, req.RunnerID, len(runner.Slots)-1)
	}

	if err := h.store.MarkSlotReady(ctx, req.RunnerID, req.SlotIndex); err != nil {
		return http.StatusInternalServerError, err
	}

	h.logger.Info("ready: slot confirmed", "runner_id", req.RunnerID, "slot_index", req.SlotIndex)
	return http.StatusNoContent, nil
}
