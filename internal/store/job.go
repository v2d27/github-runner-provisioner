package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.me/v2d27/github-runner-provisioner/internal/config"
)

// JobStatus is the workflow_job's state as tracked by this platform.
type JobStatus string

const (
	JobQueued       JobStatus = "QUEUED"
	JobProvisioning JobStatus = "PROVISIONING"
	JobAllocated    JobStatus = "ALLOCATED"
	JobCompleted    JobStatus = "COMPLETED"
	JobFailed       JobStatus = "FAILED"
	JobIgnored      JobStatus = "IGNORED"
)

// maxProvisionAttempts bounds how many times a job can bounce from
// PROVISIONING back to QUEUED (e.g. after an EC2 launch failure) before it's
// given up on as FAILED.
const maxProvisionAttempts = 3

// completedJobTTL bounds how long a terminal Job record survives.
const completedJobTTL = 48 * time.Hour

// Job is one GitHub Actions workflow_job as tracked by this platform.
type Job struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entity_type"`

	JobID         int64        `dynamodbav:"job_id"`
	WorkflowRunID int64        `dynamodbav:"workflow_run_id"`
	Scope         config.Scope `dynamodbav:"scope"`
	Organization  string       `dynamodbav:"organization"`
	Repository    string       `dynamodbav:"repository,omitempty"`
	Labels        []string     `dynamodbav:"labels"`
	Profile       string       `dynamodbav:"profile,omitempty"`
	Group         string       `dynamodbav:"group,omitempty"`

	Status   JobStatus `dynamodbav:"status"`
	RunnerID string    `dynamodbav:"runner_id,omitempty"`
	// SlotIndex is which of RunnerID's Slots (store.Runner.Slots) this job
	// was bound to — set alongside RunnerID by both AllocateIdleRunner and
	// BindToJob, and read back by handleCompleted to target the right slot
	// in MarkIdle. Meaningful only when RunnerID is non-empty; index 0 is a
	// legitimate value indistinguishable from "unset", which is fine since
	// callers never consult it without first checking RunnerID != "".
	SlotIndex  int `dynamodbav:"slot_index,omitempty"`
	RetryCount int `dynamodbav:"retry_count"`

	CreatedAt int64 `dynamodbav:"created_at"`
	UpdatedAt int64 `dynamodbav:"updated_at"`
	TTL       int64 `dynamodbav:"ttl,omitempty"`

	GSI2PK string `dynamodbav:"gsi2pk,omitempty"`
	GSI2SK int64  `dynamodbav:"gsi2sk,omitempty"`
}

// NewQueuedJob builds the Job row for a newly observed workflow_job.queued
// event.
func NewQueuedJob(jobID, workflowRunID int64, sel PoolSelector, labels []string) *Job {
	now := time.Now().Unix()
	return &Job{
		PK:            jobPK(jobID),
		SK:            stateSK,
		EntityType:    "JOB",
		JobID:         jobID,
		WorkflowRunID: workflowRunID,
		Scope:         sel.Scope,
		Organization:  sel.Organization,
		Repository:    sel.Repository,
		Labels:        labels,
		Profile:       sel.Profile,
		Group:         sel.Group,
		Status:        JobQueued,
		CreatedAt:     now,
		UpdatedAt:     now,
		GSI2PK:        jobStatusGSI2PK(JobQueued),
		GSI2SK:        now,
	}
}

// CreateIfNotExists dedupes "job seen" across redelivered/retried webhook
// events: the first caller creates the row, later callers get the existing
// one back with created=false.
func (c *Client) CreateJobIfNotExists(ctx context.Context, job *Job) (existing *Job, created bool, err error) {
	item, err := attributevalue.MarshalMap(job)
	if err != nil {
		return nil, false, fmt.Errorf("store: marshal job: %w", err)
	}
	_, err = c.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(c.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err == nil {
		return job, true, nil
	}
	if !isConditionalCheckFailed(err) {
		return nil, false, fmt.Errorf("store: create job %d: %w", job.JobID, err)
	}

	existing, err = c.GetJob(ctx, job.JobID)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

// GetJob fetches a job by its GitHub job ID.
func (c *Client) GetJob(ctx context.Context, jobID int64) (*Job, error) {
	out, err := c.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: jobPK(jobID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store: get job %d: %w", jobID, err)
	}
	if out.Item == nil {
		return nil, ErrNotFound
	}
	var j Job
	if err := attributevalue.UnmarshalMap(out.Item, &j); err != nil {
		return nil, fmt.Errorf("store: unmarshal job %d: %w", jobID, err)
	}
	return &j, nil
}

// ClaimForProvisioning is the provisioning lock described in
// docs/infrastructure/enterprise-standard-upgrade.md section 10: exactly one
// concurrent invocation wins this conditional update (QUEUED->PROVISIONING);
// every other invocation — including redeliveries of the same SQS message —
// gets claimed=false and must not launch an EC2 instance.
func (c *Client) ClaimForProvisioning(ctx context.Context, jobID int64) (claimed bool, err error) {
	now := time.Now().Unix()
	_, err = c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: jobPK(jobID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression:      aws.String("#status = :queued"),
		UpdateExpression:         aws.String("SET #status = :provisioning, updated_at = :now, gsi2pk = :gsi2pk, gsi2sk = :now"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":queued":       &types.AttributeValueMemberS{Value: string(JobQueued)},
			":provisioning": &types.AttributeValueMemberS{Value: string(JobProvisioning)},
			":now":          &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now)},
			":gsi2pk":       &types.AttributeValueMemberS{Value: jobStatusGSI2PK(JobProvisioning)},
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("store: claim job %d for provisioning: %w", jobID, err)
	}
	return true, nil
}

// RevertProvisioning is called when an EC2 launch fails after this
// invocation won the provisioning claim. It reverts PROVISIONING->QUEUED
// (so a later reconciliation sweep or redelivered message can retry) up to
// maxProvisionAttempts, after which the job is marked FAILED.
func (c *Client) RevertProvisioning(ctx context.Context, job *Job) error {
	now := time.Now().Unix()
	if job.RetryCount+1 >= maxProvisionAttempts {
		_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(c.table),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: jobPK(job.JobID)},
				"sk": &types.AttributeValueMemberS{Value: stateSK},
			},
			UpdateExpression:         aws.String("SET #status = :failed, retry_count = :retries, updated_at = :now REMOVE gsi2pk, gsi2sk"),
			ExpressionAttributeNames: map[string]string{"#status": "status"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":failed":  &types.AttributeValueMemberS{Value: string(JobFailed)},
				":retries": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", job.RetryCount+1)},
				":now":     &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now)},
			},
		})
		if err != nil {
			return fmt.Errorf("store: fail job %d: %w", job.JobID, err)
		}
		return nil
	}

	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: jobPK(job.JobID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression:      aws.String("#status = :provisioning"),
		UpdateExpression:         aws.String("SET #status = :queued, retry_count = :retries, updated_at = :now, gsi2pk = :gsi2pk, gsi2sk = :now"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":provisioning": &types.AttributeValueMemberS{Value: string(JobProvisioning)},
			":queued":       &types.AttributeValueMemberS{Value: string(JobQueued)},
			":retries":      &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", job.RetryCount+1)},
			":now":          &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now)},
			":gsi2pk":       &types.AttributeValueMemberS{Value: jobStatusGSI2PK(JobQueued)},
		},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return fmt.Errorf("store: revert job %d to queued: %w", job.JobID, err)
	}
	return nil
}

// ListStaleProvisioningJobs returns jobs stuck in PROVISIONING past cutoff —
// defense-in-depth alongside ListStaleProvisioning on the runner side, for
// the case where the runner row was never created at all (e.g. the process
// crashed between ClaimForProvisioning and CreateProvisioning).
func (c *Client) ListStaleProvisioningJobs(ctx context.Context, cutoff time.Time) ([]*Job, error) {
	out, err := c.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(c.table),
		IndexName:              aws.String("gsi2"),
		KeyConditionExpression: aws.String("gsi2pk = :pk AND gsi2sk <= :cutoff"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: jobStatusGSI2PK(JobProvisioning)},
			":cutoff": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", cutoff.Unix())},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store: list stale provisioning jobs: %w", err)
	}
	jobs := make([]*Job, 0, len(out.Items))
	for _, item := range out.Items {
		var j Job
		if err := attributevalue.UnmarshalMap(item, &j); err != nil {
			return nil, fmt.Errorf("store: unmarshal job: %w", err)
		}
		jobs = append(jobs, &j)
	}
	return jobs, nil
}

// MarkCompleted finalizes a job once its workflow_job.completed event
// arrives.
func (c *Client) MarkCompleted(ctx context.Context, jobID int64) error {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: jobPK(jobID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		UpdateExpression: aws.String("SET #status = :completed, updated_at = :now, #ttl = :ttl REMOVE gsi2pk, gsi2sk"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
			"#ttl":    "ttl",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":completed": &types.AttributeValueMemberS{Value: string(JobCompleted)},
			":now":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Unix())},
			":ttl":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(completedJobTTL).Unix())},
		},
	})
	if err != nil {
		return fmt.Errorf("store: mark job %d completed: %w", jobID, err)
	}
	return nil
}

// MarkIgnored records a job that was deliberately skipped (no matching
// profile) so idempotency/debugging can distinguish "we saw this and chose
// not to act" from "we never saw this".
func (c *Client) MarkIgnored(ctx context.Context, job *Job) error {
	item, err := attributevalue.MarshalMap(job)
	if err != nil {
		return fmt.Errorf("store: marshal ignored job: %w", err)
	}
	item["status"] = &types.AttributeValueMemberS{Value: string(JobIgnored)}
	item["ttl"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(completedJobTTL).Unix())}
	delete(item, "gsi2pk")
	delete(item, "gsi2sk")
	_, err = c.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(c.table),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("store: mark job %d ignored: %w", job.JobID, err)
	}
	return nil
}
