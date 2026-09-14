// Command cleanup is the EventBridge-triggered Lambda: the rate(3 minutes)
// reconciliation sweep, and (via the same function, distinguished by
// detail-type) immediate handling of EC2 Spot interruption notices. See
// docs/infrastructure/enterprise-standard-upgrade.md sections 20, 30, 32.
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
	"github.me/v2d27/github-runner-provisioner/internal/cleanup"
	"github.me/v2d27/github-runner-provisioner/internal/config"
	ghclient "github.me/v2d27/github-runner-provisioner/internal/github"
	"github.me/v2d27/github-runner-provisioner/internal/store"
)

// spotInterruptionDetailType is the EventBridge detail-type for the
// "EC2 Spot Instance Interruption Warning" event, distinguishing it from
// the ordinary rate(3 minutes) "Scheduled Event".
const spotInterruptionDetailType = "EC2 Spot Instance Interruption Warning"

type spotInterruptionDetail struct {
	InstanceID string `json:"instance-id"`
}

var (
	logger  = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	service *cleanup.Service
)

func init() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("cleanup: load config", "error", err)
		os.Exit(1)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error("cleanup: load aws config", "error", err)
		os.Exit(1)
	}

	secrets := awsinternal.NewSecrets(awsCfg)
	privateKey, err := secrets.GetSecretString(ctx, cfg.GitHub.PrivateKeySecretName)
	if err != nil {
		logger.Error("cleanup: load github app private key", "error", err)
		os.Exit(1)
	}

	st := store.New(awsCfg, mustEnv("DYNAMODB_TABLE_NAME"))
	ghProvider := ghclient.NewProvider(ghclient.AppCredentials{
		AppID:      cfg.GitHub.AppID,
		PrivateKey: []byte(privateKey),
	}, cfg, st)
	ec2Client := awsinternal.NewEC2(awsCfg)

	service = cleanup.NewService(cfg, st, ghProvider, ec2Client, logger)
}

func handleEvent(ctx context.Context, event events.CloudWatchEvent) error {
	if event.DetailType == spotInterruptionDetailType {
		var detail spotInterruptionDetail
		if err := json.Unmarshal(event.Detail, &detail); err != nil {
			logger.Error("cleanup: unmarshal spot interruption detail", "error", err)
			return err
		}
		if err := service.HandleSpotInterruption(ctx, detail.InstanceID); err != nil {
			logger.Error("cleanup: handle spot interruption", "instance_id", detail.InstanceID, "error", err)
			return err
		}
		return nil
	}

	if err := service.RunScheduledSweep(ctx); err != nil {
		logger.Error("cleanup: scheduled sweep", "error", err)
		return err
	}
	return nil
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		logger.Error("cleanup: missing required environment variable", "name", name)
		os.Exit(1)
	}
	return v
}

func main() {
	lambda.Start(handleEvent)
}
