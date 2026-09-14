// Command ready is the API Gateway HTTP API Lambda that runner instances
// call back once one of their runner processes registers with GitHub and
// starts — see internal/ready and store.MarkSlotReady.
package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.me/v2d27/github-runner-provisioner/internal/ready"
	"github.me/v2d27/github-runner-provisioner/internal/store"
)

var (
	logger  = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler *ready.Handler
)

func init() {
	ctx := context.Background()

	// No RUNNER_CONFIG_JSON here: this Lambda only ever touches store.Client
	// (keyed by DYNAMODB_TABLE_NAME below), never GitHub or runner.yaml's
	// policy knobs.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error("ready: load aws config", "error", err)
		os.Exit(1)
	}

	// recordTTL is 0 (unused): MarkSlotReady, the only store method this
	// Lambda ever calls, never writes a ttl attribute.
	st := store.New(awsCfg, mustEnv("DYNAMODB_TABLE_NAME"), 0)
	handler = ready.NewHandler(st, logger)
}

func handleRequest(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	body := []byte(req.Body)
	if req.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			logger.Error("ready: decode base64 body", "error", err)
			return events.APIGatewayV2HTTPResponse{StatusCode: 400}, nil
		}
		body = decoded
	}

	status, err := handler.Handle(ctx, body)
	if err != nil {
		logger.Error("ready: handle request", "status", status, "error", err)
	}
	// As with cmd/webhook: always a nil Go error so API Gateway relays the
	// intended status to the caller instead of a generic 502.
	return events.APIGatewayV2HTTPResponse{StatusCode: status}, nil
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		logger.Error("ready: missing required environment variable", "name", name)
		os.Exit(1)
	}
	return v
}

func main() {
	lambda.Start(handleRequest)
}
