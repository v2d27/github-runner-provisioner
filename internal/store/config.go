package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// installationIDCacheKey and its TTL. A resolved installation ID rarely
// changes, but a short TTL lets a re-installed/reconfigured GitHub App
// self-heal without a code change.
const (
	installationIDCacheKey = "installation_id"
	installationIDCacheTTL = 12 * time.Hour
)

// GetInstallationID implements github.InstallationCache, letting the
// resolved installation ID survive across Lambda cold starts instead of
// being re-resolved (and re-charged against GitHub API rate limits) on every
// one.
func (c *Client) GetInstallationID(ctx context.Context) (id int64, ok bool, err error) {
	out, err := c.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: configPK(installationIDCacheKey)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
	})
	if err != nil {
		return 0, false, fmt.Errorf("store: get cached installation id: %w", err)
	}
	if out.Item == nil {
		return 0, false, nil
	}
	v, ok := out.Item["value"].(*types.AttributeValueMemberN)
	if !ok {
		return 0, false, nil
	}
	var parsed int64
	if _, err := fmt.Sscanf(v.Value, "%d", &parsed); err != nil {
		return 0, false, nil
	}
	return parsed, true, nil
}

// PutInstallationID caches a resolved installation ID.
func (c *Client) PutInstallationID(ctx context.Context, id int64) error {
	_, err := c.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(c.table),
		Item: map[string]types.AttributeValue{
			"pk":          &types.AttributeValueMemberS{Value: configPK(installationIDCacheKey)},
			"sk":          &types.AttributeValueMemberS{Value: stateSK},
			"entity_type": &types.AttributeValueMemberS{Value: "CONFIG"},
			"value":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", id)},
			"ttl":         &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(installationIDCacheTTL).Unix())},
		},
	})
	if err != nil {
		return fmt.Errorf("store: cache installation id: %w", err)
	}
	return nil
}
