// Command provision is the SQS-triggered controller/reconciliation Lambda:
// profile resolution, runner allocation, and EC2 provisioning. See
// docs/infrastructure/enterprise-standard-upgrade.md section 19.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	awsinternal "github.me/v2d27/github-runner-provisioner/internal/aws"
	"github.me/v2d27/github-runner-provisioner/internal/config"
	ghclient "github.me/v2d27/github-runner-provisioner/internal/github"
	"github.me/v2d27/github-runner-provisioner/internal/runner"
	"github.me/v2d27/github-runner-provisioner/internal/store"
	"github.me/v2d27/github-runner-provisioner/internal/webhook"
)

var (
	logger  = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	service *runner.Service
)

func init() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("provision: load config", "error", err)
		os.Exit(1)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error("provision: load aws config", "error", err)
		os.Exit(1)
	}

	secrets := awsinternal.NewSecrets(awsCfg)
	privateKey, err := secrets.GetSecretString(ctx, cfg.GitHub.PrivateKeySecretName)
	if err != nil {
		logger.Error("provision: load github app private key", "error", err)
		os.Exit(1)
	}

	st := store.New(awsCfg, mustEnv("DYNAMODB_TABLE_NAME"))
	ghProvider := ghclient.NewProvider(ghclient.AppCredentials{
		AppID:      cfg.GitHub.AppID,
		PrivateKey: []byte(privateKey),
	}, cfg, st)
	ec2Client := awsinternal.NewEC2(awsCfg)
	amiResolver := awsinternal.NewAMIResolver(awsCfg)

	service = runner.NewService(cfg, st, ghProvider, ec2Client, amiResolver, mustEnv("LAUNCH_TEMPLATE_ID"), logger)
}

func handleSQSEvent(ctx context.Context, sqsEvent events.SQSEvent) (events.SQSEventResponse, error) {
	var failures []events.SQSBatchItemFailure

	for _, record := range sqsEvent.Records {
		var normalized webhook.NormalizedEvent
		if err := json.Unmarshal([]byte(record.Body), &normalized); err != nil {
			logger.Error("provision: unmarshal message", "message_id", record.MessageId, "error", err)
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
			continue
		}

		err := service.Handle(ctx, runner.Event{
			Action:        normalized.Action,
			JobID:         normalized.JobID,
			WorkflowRunID: normalized.WorkflowRunID,
			Labels:        normalized.Labels,
		})
		if err != nil {
			logger.Error("provision: handle event", "job_id", normalized.JobID, "action", normalized.Action, "error", err)
			failures = append(failures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
		}
	}

	return events.SQSEventResponse{BatchItemFailures: failures}, nil
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		logger.Error("provision: missing required environment variable", "name", name)
		os.Exit(1)
	}
	return v
}

func main() {
	lambda.Start(handleSQSEvent)
}
