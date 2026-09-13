// Command webhook is the API Gateway HTTP API Lambda: it verifies incoming
// GitHub webhook deliveries, dedupes them, and forwards normalized
// workflow_job events to SQS for cmd/provision to act on. See
// docs/infrastructure/enterprise-standard-upgrade.md section 25.
package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	awsinternal "github.me/v2d27/provision-github-runner-on-demand/internal/aws"
	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
	"github.me/v2d27/provision-github-runner-on-demand/internal/store"
	"github.me/v2d27/provision-github-runner-on-demand/internal/webhook"
)

var (
	logger  = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler *webhook.Handler
)

func init() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("webhook: load config", "error", err)
		os.Exit(1)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error("webhook: load aws config", "error", err)
		os.Exit(1)
	}

	secrets := awsinternal.NewSecrets(awsCfg)
	secretValue, err := secrets.GetSecretString(ctx, cfg.Webhook.SecretName)
	if err != nil {
		logger.Error("webhook: load webhook secret", "error", err)
		os.Exit(1)
	}

	st := store.New(awsCfg, mustEnv("DYNAMODB_TABLE_NAME"))
	queue := webhook.NewQueue(awsCfg, mustEnv("QUEUE_URL"))

	handler = webhook.NewHandler([]byte(secretValue), st, queue, logger)
}

func handleRequest(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	body := []byte(req.Body)
	if req.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			logger.Error("webhook: decode base64 body", "error", err)
			return events.APIGatewayV2HTTPResponse{StatusCode: 400}, nil
		}
		body = decoded
	}

	status, err := handler.Handle(ctx, req.Headers, body)
	if err != nil {
		logger.Error("webhook: handle request", "status", status, "error", err)
	}
	// Always return the intended HTTP status via a nil Go error, so API
	// Gateway relays it to GitHub as-is instead of a generic 502 — GitHub's
	// own webhook retry behavior is driven by that status code.
	return events.APIGatewayV2HTTPResponse{StatusCode: status}, nil
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		logger.Error("webhook: missing required environment variable", "name", name)
		os.Exit(1)
	}
	return v
}

func main() {
	lambda.Start(handleRequest)
}
