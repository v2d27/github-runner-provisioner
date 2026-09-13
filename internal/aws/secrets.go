package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Secrets wraps Secrets Manager lookups. Every value it returns (GitHub App
// private key, webhook HMAC secret) stays in Lambda memory only — it is
// never written to DynamoDB, logs, or EC2 UserData.
type Secrets struct {
	client *secretsmanager.Client
}

func NewSecrets(cfg aws.Config) *Secrets {
	return &Secrets{client: secretsmanager.NewFromConfig(cfg)}
}

// GetSecretString fetches the current string value of a secret.
func (s *Secrets) GetSecretString(ctx context.Context, name string) (string, error) {
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		return "", fmt.Errorf("aws: get secret %s: %w", name, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("aws: secret %s has no string value", name)
	}
	return *out.SecretString, nil
}
