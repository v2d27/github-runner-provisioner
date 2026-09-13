package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/oklog/ulid/v2"

	"github.me/v2d27/provision-github-runner-on-demand/internal/config"
)

// RunnerStatus is the runner lifecycle state.
//
// This platform binds a freshly provisioned runner directly to the job that
// triggered it (see AllocateIdleRunner / FinishProvisioning), so the
// doc's separate PROVISIONING -> STARTING -> IDLE handoff collapses to
// PROVISIONING -> BUSY: a runner goes straight from "not yet launched" to
// "launched and reserved for job X", and only reaches IDLE once that job
// completes and the runner becomes reusable.
type RunnerStatus string

const (
	RunnerProvisioning RunnerStatus = "PROVISIONING"
	RunnerBusy         RunnerStatus = "BUSY"
	RunnerIdle         RunnerStatus = "IDLE"
	RunnerTerminating  RunnerStatus = "TERMINATING"
	RunnerTerminated   RunnerStatus = "TERMINATED"
	RunnerFailed       RunnerStatus = "FAILED"
)

// terminatedTTL bounds how long a TERMINATED/FAILED runner record survives —
// long enough for post-mortem debugging, short enough to keep the table lean.
const terminatedTTL = 7 * 24 * time.Hour

// Runner is one EC2-backed GitHub Actions runner.
type Runner struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entity_type"`

	RunnerID       string `dynamodbav:"runner_id"`
	Name           string `dynamodbav:"name"`
	GitHubRunnerID int64  `dynamodbav:"github_runner_id,omitempty"`

	Scope        config.Scope `dynamodbav:"scope"`
	Organization string       `dynamodbav:"organization"`
	Repository   string       `dynamodbav:"repository,omitempty"`

	Profile      string   `dynamodbav:"profile"`
	Group        string   `dynamodbav:"group"`
	Labels       []string `dynamodbav:"labels"`
	Architecture string   `dynamodbav:"architecture"`

	InstanceID   string `dynamodbav:"instance_id,omitempty"`
	InstanceType string `dynamodbav:"instance_type"`
	AMI          string `dynamodbav:"ami"`

	Status RunnerStatus `dynamodbav:"status"`

	JobID         int64 `dynamodbav:"job_id,omitempty"`
	WorkflowRunID int64 `dynamodbav:"workflow_run_id,omitempty"`

	CreatedAt      int64 `dynamodbav:"created_at"`
	IdleSince      int64 `dynamodbav:"idle_since,omitempty"`
	TerminateAfter int64 `dynamodbav:"terminate_after,omitempty"`

	TTL int64 `dynamodbav:"ttl,omitempty"`

	GSI1PK string `dynamodbav:"gsi1pk,omitempty"`
	GSI1SK string `dynamodbav:"gsi1sk,omitempty"`
	GSI2PK string `dynamodbav:"gsi2pk,omitempty"`
	GSI2SK int64  `dynamodbav:"gsi2sk,omitempty"`
	GSI3PK string `dynamodbav:"gsi3pk,omitempty"`
	GSI3SK string `dynamodbav:"gsi3sk,omitempty"`
}

// NewRunnerID returns a fresh, time-sortable runner identifier.
func NewRunnerID() string {
	return ulid.Make().String()
}

// NewProvisioningRunner builds the Runner row written *before* RunInstances
// is called, so a durable record exists even if the EC2 call itself fails or
// times out (reconciled later by ListStaleProvisioning). ami is the AMI ID
// actually resolved for this launch — whether pinned in config.Profile.AMI
// or resolved dynamically via config.Profile.AMILookup — recorded here so
// it's always known which image a given instance booted from.
func NewProvisioningRunner(sel PoolSelector, runnerID, name string, labels []string, architecture, instanceType, ami string) *Runner {
	now := time.Now().Unix()
	return &Runner{
		PK:           runnerPK(runnerID),
		SK:           stateSK,
		EntityType:   "RUNNER",
		RunnerID:     runnerID,
		Name:         name,
		Scope:        sel.Scope,
		Organization: sel.Organization,
		Repository:   sel.Repository,
		Profile:      sel.Profile,
		Group:        sel.Group,
		Labels:       labels,
		Architecture: architecture,
		InstanceType: instanceType,
		AMI:          ami,
		Status:       RunnerProvisioning,
		CreatedAt:    now,
		GSI1PK:       sel.gsi1PK(),
		GSI1SK:       gsi1SK(RunnerProvisioning, now),
		GSI2PK:       runnerStatusGSI2PK(RunnerProvisioning),
		GSI2SK:       now,
	}
}

func gsi1SK(status RunnerStatus, unixTime int64) string {
	return fmt.Sprintf("%s#%s", status, time.Unix(unixTime, 0).UTC().Format(time.RFC3339))
}

// CreateProvisioning persists a new Runner row. Called before RunInstances.
func (c *Client) CreateProvisioning(ctx context.Context, r *Runner) error {
	item, err := attributevalue.MarshalMap(r)
	if err != nil {
		return fmt.Errorf("store: marshal runner: %w", err)
	}
	_, err = c.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(c.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		return fmt.Errorf("store: create runner %s: %w", r.RunnerID, err)
	}
	return nil
}

// SetInstanceID durably records the launched EC2 instance ID (and populates
// GSI3) as soon as RunInstances succeeds — deliberately a separate step from
// BindToJob, so an instance is always reconcilable via
// FindRunnerByInstanceID even if the process crashes or errors between the
// EC2 call and the job-binding update below.
func (c *Client) SetInstanceID(ctx context.Context, runnerID, instanceID string) error {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		UpdateExpression: aws.String("SET instance_id = :instanceID, gsi3pk = :gsi3pk, gsi3sk = :gsi3sk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":instanceID": &types.AttributeValueMemberS{Value: instanceID},
			":gsi3pk":     &types.AttributeValueMemberS{Value: instanceGSI3PK(instanceID)},
			":gsi3sk":     &types.AttributeValueMemberS{Value: stateSK},
		},
	})
	if err != nil {
		return fmt.Errorf("store: set instance id for runner %s: %w", runnerID, err)
	}
	return nil
}

// BindToJob binds a just-provisioned runner directly to the job that
// triggered its creation, moving it straight to BUSY (see the RunnerStatus
// doc comment for why there's no separate IDLE hop here).
func (c *Client) BindToJob(ctx context.Context, runnerID string, jobID, workflowRunID int64) error {
	now := time.Now().Unix()
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression: aws.String("#status = :provisioning"),
		UpdateExpression: aws.String(
			"SET #status = :busy, job_id = :jobID, workflow_run_id = :wfID, gsi1sk = :gsi1sk REMOVE gsi2pk, gsi2sk",
		),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":provisioning": &types.AttributeValueMemberS{Value: string(RunnerProvisioning)},
			":busy":         &types.AttributeValueMemberS{Value: string(RunnerBusy)},
			":jobID":        &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", jobID)},
			":wfID":         &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", workflowRunID)},
			":gsi1sk":       &types.AttributeValueMemberS{Value: gsi1SK(RunnerBusy, now)},
		},
	})
	if err != nil {
		return fmt.Errorf("store: bind runner %s to job %d: %w", runnerID, jobID, err)
	}
	return nil
}

// AllocateIdleRunner tries to claim one IDLE runner from the given pool for
// job, oldest-idle-first. It returns (nil, false, nil) if the pool has no
// idle runner, or every candidate lost its race to another concurrent
// invocation. The Runner IDLE->BUSY and Job QUEUED->ALLOCATED writes happen
// together in one transaction so a crash between them can never leave a BUSY
// runner with no job, or a Job stuck ALLOCATED with no real runner.
func (c *Client) AllocateIdleRunner(ctx context.Context, sel PoolSelector, job *Job) (*Runner, bool, error) {
	out, err := c.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(c.table),
		IndexName:              aws.String("gsi1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk AND begins_with(gsi1sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: sel.gsi1PK()},
			":prefix": &types.AttributeValueMemberS{Value: string(RunnerIdle) + "#"},
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: query idle pool: %w", err)
	}

	for _, item := range out.Items {
		var candidate Runner
		if err := attributevalue.UnmarshalMap(item, &candidate); err != nil {
			return nil, false, fmt.Errorf("store: unmarshal runner candidate: %w", err)
		}

		now := time.Now().Unix()
		_, err := c.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: []types.TransactWriteItem{
				{
					Update: &types.Update{
						TableName: aws.String(c.table),
						Key: map[string]types.AttributeValue{
							"pk": &types.AttributeValueMemberS{Value: runnerPK(candidate.RunnerID)},
							"sk": &types.AttributeValueMemberS{Value: stateSK},
						},
						ConditionExpression: aws.String("#status = :idle"),
						UpdateExpression: aws.String(
							"SET #status = :busy, job_id = :jobID, workflow_run_id = :wfID, gsi1sk = :gsi1sk " +
								"REMOVE gsi2pk, gsi2sk, idle_since, terminate_after",
						),
						ExpressionAttributeNames: map[string]string{"#status": "status"},
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":idle":   &types.AttributeValueMemberS{Value: string(RunnerIdle)},
							":busy":   &types.AttributeValueMemberS{Value: string(RunnerBusy)},
							":jobID":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", job.JobID)},
							":wfID":   &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", job.WorkflowRunID)},
							":gsi1sk": &types.AttributeValueMemberS{Value: gsi1SK(RunnerBusy, now)},
						},
					},
				},
				{
					Update: &types.Update{
						TableName: aws.String(c.table),
						Key: map[string]types.AttributeValue{
							"pk": &types.AttributeValueMemberS{Value: jobPK(job.JobID)},
							"sk": &types.AttributeValueMemberS{Value: stateSK},
						},
						ConditionExpression: aws.String("#status = :queued"),
						UpdateExpression:    aws.String("SET #status = :allocated, runner_id = :runnerID, updated_at = :now REMOVE gsi2pk, gsi2sk"),
						ExpressionAttributeNames: map[string]string{
							"#status": "status",
						},
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":queued":    &types.AttributeValueMemberS{Value: string(JobQueued)},
							":allocated": &types.AttributeValueMemberS{Value: string(JobAllocated)},
							":runnerID":  &types.AttributeValueMemberS{Value: candidate.RunnerID},
							":now":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now)},
						},
					},
				},
			},
		})
		if err == nil {
			candidate.Status = RunnerBusy
			candidate.JobID = job.JobID
			candidate.WorkflowRunID = job.WorkflowRunID
			return &candidate, true, nil
		}
		if isConditionalCheckFailed(err) {
			continue // lost the race (or job already claimed) — try the next candidate
		}
		return nil, false, fmt.Errorf("store: allocate runner %s: %w", candidate.RunnerID, err)
	}
	return nil, false, nil
}

// MarkIdle transitions a runner from BUSY back to IDLE once its job
// completes, starting the idle-timeout clock.
func (c *Client) MarkIdle(ctx context.Context, runnerID string, idleTimeout time.Duration) error {
	now := time.Now()
	terminateAfter := now.Add(idleTimeout).Unix()
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression: aws.String("#status = :busy"),
		UpdateExpression: aws.String(
			"SET #status = :idle, idle_since = :now, terminate_after = :term, gsi1sk = :gsi1sk, gsi2pk = :gsi2pk, gsi2sk = :gsi2sk " +
				"REMOVE job_id, workflow_run_id",
		),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":busy":   &types.AttributeValueMemberS{Value: string(RunnerBusy)},
			":idle":   &types.AttributeValueMemberS{Value: string(RunnerIdle)},
			":now":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
			":term":   &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", terminateAfter)},
			":gsi1sk": &types.AttributeValueMemberS{Value: gsi1SK(RunnerIdle, now.Unix())},
			":gsi2pk": &types.AttributeValueMemberS{Value: runnerStatusGSI2PK(RunnerIdle)},
			":gsi2sk": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", terminateAfter)},
		},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return fmt.Errorf("store: mark runner %s idle: %w", runnerID, err)
	}
	return nil
}

// Get fetches a runner by ID.
func (c *Client) GetRunner(ctx context.Context, runnerID string) (*Runner, error) {
	out, err := c.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store: get runner %s: %w", runnerID, err)
	}
	if out.Item == nil {
		return nil, ErrNotFound
	}
	var r Runner
	if err := attributevalue.UnmarshalMap(out.Item, &r); err != nil {
		return nil, fmt.Errorf("store: unmarshal runner %s: %w", runnerID, err)
	}
	return &r, nil
}

// FindRunnerByInstanceID looks up a runner by its EC2 instance ID (GSI3),
// used by orphan reconciliation and the spot-interruption handler.
func (c *Client) FindRunnerByInstanceID(ctx context.Context, instanceID string) (*Runner, error) {
	out, err := c.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(c.table),
		IndexName:              aws.String("gsi3"),
		KeyConditionExpression: aws.String("gsi3pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: instanceGSI3PK(instanceID)},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("store: find runner by instance %s: %w", instanceID, err)
	}
	if len(out.Items) == 0 {
		return nil, ErrNotFound
	}
	var r Runner
	if err := attributevalue.UnmarshalMap(out.Items[0], &r); err != nil {
		return nil, fmt.Errorf("store: unmarshal runner: %w", err)
	}
	return &r, nil
}

// ListExpiredIdle returns IDLE runners whose terminate_after has passed.
func (c *Client) ListExpiredIdle(ctx context.Context, now time.Time) ([]*Runner, error) {
	return c.queryStatusTimeline(ctx, runnerStatusGSI2PK(RunnerIdle), now.Unix())
}

// ListStaleProvisioning returns runners stuck in PROVISIONING past
// cutoff — orphans from an EC2 call that never completed or was never
// observed to complete (doc section 30).
func (c *Client) ListStaleProvisioning(ctx context.Context, cutoff time.Time) ([]*Runner, error) {
	return c.queryStatusTimeline(ctx, runnerStatusGSI2PK(RunnerProvisioning), cutoff.Unix())
}

func (c *Client) queryStatusTimeline(ctx context.Context, gsi2pk string, maxUnixTime int64) ([]*Runner, error) {
	out, err := c.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(c.table),
		IndexName:              aws.String("gsi2"),
		KeyConditionExpression: aws.String("gsi2pk = :pk AND gsi2sk <= :cutoff"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: gsi2pk},
			":cutoff": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", maxUnixTime)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("store: query status timeline %s: %w", gsi2pk, err)
	}
	runners := make([]*Runner, 0, len(out.Items))
	for _, item := range out.Items {
		var r Runner
		if err := attributevalue.UnmarshalMap(item, &r); err != nil {
			return nil, fmt.Errorf("store: unmarshal runner: %w", err)
		}
		runners = append(runners, &r)
	}
	return runners, nil
}

// TransitionToTerminating conditionally moves a runner into TERMINATING from
// any non-terminal state, dropping it out of the allocation pool and status
// timeline GSIs. Returns false (not an error) if the runner already left the
// state the caller observed — e.g. a job claimed it between the cleanup
// sweep's read and this write.
func (c *Client) TransitionToTerminating(ctx context.Context, runnerID string) (bool, error) {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression: aws.String("#status IN (:idle, :busy, :provisioning)"),
		UpdateExpression:    aws.String("SET #status = :terminating REMOVE gsi1pk, gsi1sk, gsi2pk, gsi2sk"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":idle":         &types.AttributeValueMemberS{Value: string(RunnerIdle)},
			":busy":         &types.AttributeValueMemberS{Value: string(RunnerBusy)},
			":provisioning": &types.AttributeValueMemberS{Value: string(RunnerProvisioning)},
			":terminating":  &types.AttributeValueMemberS{Value: string(RunnerTerminating)},
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return false, nil
		}
		return false, fmt.Errorf("store: transition runner %s to terminating: %w", runnerID, err)
	}
	return true, nil
}

// MarkRunnerFailed terminally fails a runner that never got an EC2 instance
// (RunInstances itself errored) — there's nothing to terminate, just the
// bookkeeping to remove it from every GSI and stop the stale-provisioning
// sweep from finding it again.
func (c *Client) MarkRunnerFailed(ctx context.Context, runnerID string) error {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		UpdateExpression: aws.String("SET #status = :failed, #ttl = :ttl REMOVE gsi1pk, gsi1sk, gsi2pk, gsi2sk"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
			"#ttl":    "ttl",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":failed": &types.AttributeValueMemberS{Value: string(RunnerFailed)},
			":ttl":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(terminatedTTL).Unix())},
		},
	})
	if err != nil {
		return fmt.Errorf("store: mark runner %s failed: %w", runnerID, err)
	}
	return nil
}

// MarkTerminated finalizes a runner record after its EC2 instance and GitHub
// registration have both been removed, recording the GitHub runner ID
// resolved during cleanup and setting a TTL so the record eventually expires.
func (c *Client) MarkTerminated(ctx context.Context, runnerID string, githubRunnerID int64) error {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		UpdateExpression: aws.String("SET #status = :terminated, github_runner_id = :ghID, #ttl = :ttl"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
			"#ttl":    "ttl",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":terminated": &types.AttributeValueMemberS{Value: string(RunnerTerminated)},
			":ghID":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", githubRunnerID)},
			":ttl":        &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(terminatedTTL).Unix())},
		},
	})
	if err != nil {
		return fmt.Errorf("store: mark runner %s terminated: %w", runnerID, err)
	}
	return nil
}
