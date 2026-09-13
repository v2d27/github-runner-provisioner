package webhook

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Queue sends normalized events to the runner job queue (with its DLQ,
// configured in terraform/modules/github-runner-on-aws/sqs.tf).
type Queue struct {
	client   *sqs.Client
	queueURL string
}

func NewQueue(cfg aws.Config, queueURL string) *Queue {
	return &Queue{client: sqs.NewFromConfig(cfg), queueURL: queueURL}
}

func (q *Queue) Send(ctx context.Context, body string) error {
	_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(body),
	})
	if err != nil {
		return fmt.Errorf("webhook: send to queue: %w", err)
	}
	return nil
}
