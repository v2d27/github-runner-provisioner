package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// deliveryTTL bounds how long a delivery-id dedupe record is kept — long
// enough to cover GitHub's webhook retry window, short enough to keep the
// table lean.
const deliveryTTL = 24 * time.Hour

// RecordDelivery records a GitHub webhook delivery ID exactly once.
// firstSeen is false when the same delivery ID was already recorded — the
// webhook Lambda must treat that as a duplicate and skip re-processing
// without erroring (GitHub retries deliver-at-least-once).
func (c *Client) RecordDelivery(ctx context.Context, deliveryID, eventType, action string, jobID int64) (firstSeen bool, err error) {
	now := time.Now()
	item := map[string]types.AttributeValue{
		"pk":          &types.AttributeValueMemberS{Value: eventPK(deliveryID)},
		"sk":          &types.AttributeValueMemberS{Value: receivedSK},
		"entity_type": &types.AttributeValueMemberS{Value: "EVENT"},
		"event_type":  &types.AttributeValueMemberS{Value: eventType},
		"action":      &types.AttributeValueMemberS{Value: action},
		"job_id":      &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", jobID)},
		"received_at": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
		"ttl":         &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Add(deliveryTTL).Unix())},
	}
	_, err = c.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(c.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err == nil {
		return true, nil
	}
	if isConditionalCheckFailed(err) {
		return false, nil
	}
	return false, fmt.Errorf("store: record delivery %s: %w", deliveryID, err)
}
