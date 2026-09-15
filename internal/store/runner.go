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

	"github.me/v2d27/github-runner-provisioner/internal/config"
)

// RunnerStatus is the EC2 instance's own lifecycle state. Busy/idle is a
// per-slot concept (see SlotStatus) since one instance can host several
// independent runner processes (config.Profile.RunnerCount, 1-2) — this
// status only tracks the instance itself: not yet launched, launched and
// hosting live runner slots, or on its way out.
type RunnerStatus string

const (
	RunnerProvisioning RunnerStatus = "PROVISIONING"
	// RunnerActive means the EC2 instance is up and its runner slots are
	// each independently busy or idle — see Runner.Slots.
	RunnerActive      RunnerStatus = "ACTIVE"
	RunnerTerminating RunnerStatus = "TERMINATING"
	RunnerTerminated  RunnerStatus = "TERMINATED"
	RunnerFailed      RunnerStatus = "FAILED"
)

// SlotStatus is one runner process's own allocation state, independent of
// its siblings on the same instance.
type SlotStatus string

const (
	// SlotBusy is also the placeholder state every slot starts in while the
	// instance is still PROVISIONING (before BindToJob promotes any
	// non-triggering slot to IDLE) — harmless, since a slot is never
	// allocatable (see gsi1pk/gsi1sk) until then.
	SlotBusy SlotStatus = "BUSY"
	SlotIdle SlotStatus = "IDLE"
)

// idlePoolPrefix namespaces GSI1's sort key; GSI1 is only ever populated
// while a row has at least one IDLE slot, so the prefix is constant rather
// than reflecting a per-row status.
const idlePoolPrefix = "IDLE"

// Slot is one GitHub-registered runner process on a shared EC2 instance.
// Every slot is allocated, freed and idle-timed out independently of its
// siblings — an instance is only terminated once every slot is
// simultaneously IDLE and past its own TerminateAfter (or the instance
// itself has outlived runner.auto_terminating_time regardless of any slot's
// own timer) — see internal/cleanup.
type Slot struct {
	Index int `dynamodbav:"index"`
	// Name is the GitHub-registered runner name (e.g. "...-1") — the lookup
	// key cleanup uses to resolve this slot's GitHub-assigned runner ID
	// (GitHub's registration API never returns one directly).
	Name           string     `dynamodbav:"name"`
	GitHubRunnerID int64      `dynamodbav:"github_runner_id,omitempty"`
	Status         SlotStatus `dynamodbav:"status"`

	JobID         int64 `dynamodbav:"job_id,omitempty"`
	WorkflowRunID int64 `dynamodbav:"workflow_run_id,omitempty"`

	// StartingSince is set by BindToJob the moment the slot is created,
	// independent of its Busy/Idle status, and removed once MarkSlotReady's
	// runner-ready callback confirms the runner process actually came up.
	// Left uncleared past runner.boot_timeout, cleanup's boot-timeout sweep
	// (reconcileStuckStarting) treats the instance as a failed boot. Once
	// cleared it is never set again, so a slot's later busy/idle churn (real
	// jobs) is never mistaken for a still-booting one.
	StartingSince int64 `dynamodbav:"starting_since,omitempty"`

	IdleSince      int64 `dynamodbav:"idle_since,omitempty"`
	TerminateAfter int64 `dynamodbav:"terminate_after,omitempty"`
}

// Runner is one EC2 instance, hosting one or more independent runner Slots
// (config.Profile.RunnerCount, 1-2).
type Runner struct {
	PK         string `dynamodbav:"pk"`
	SK         string `dynamodbav:"sk"`
	EntityType string `dynamodbav:"entity_type"`

	RunnerID string `dynamodbav:"runner_id"`
	Slots    []Slot `dynamodbav:"slots"`

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

	// ReadyToken authenticates the runner-ready callback (cmd/ready,
	// MarkSlotReady): a random secret generated at provisioning time and
	// handed to the instance via UserData — the same trust model already
	// used for GitHub registration tokens, so the instance never needs any
	// broader AWS credential just to report its own readiness.
	ReadyToken string `dynamodbav:"ready_token"`

	Status RunnerStatus `dynamodbav:"status"`

	CreatedAt int64 `dynamodbav:"created_at"`

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
// it's always known which image a given instance booted from. names holds
// one entry per runner process the instance will install (config.Profile.
// RunnerCount); every slot starts BUSY (a placeholder — see SlotBusy) since
// none are allocatable until BindToJob promotes the non-triggering ones to
// IDLE once the instance is confirmed launched. readyToken is the
// runner-ready callback's shared secret (see MarkSlotReady), generated fresh
// per runner and handed to the instance via UserData.
func NewProvisioningRunner(sel PoolSelector, runnerID string, names []string, labels []string, architecture, instanceType, ami, readyToken string) *Runner {
	now := time.Now().Unix()

	slots := make([]Slot, len(names))
	for i, name := range names {
		slots[i] = Slot{Index: i, Name: name, Status: SlotBusy}
	}

	return &Runner{
		PK:           runnerPK(runnerID),
		SK:           stateSK,
		EntityType:   "RUNNER",
		RunnerID:     runnerID,
		Slots:        slots,
		Scope:        sel.Scope,
		Organization: sel.Organization,
		Repository:   sel.Repository,
		Profile:      sel.Profile,
		Group:        sel.Group,
		Labels:       labels,
		Architecture: architecture,
		InstanceType: instanceType,
		AMI:          ami,
		ReadyToken:   readyToken,
		Status:       RunnerProvisioning,
		CreatedAt:    now,
		GSI2PK:       runnerStatusGSI2PK(RunnerProvisioning),
		GSI2SK:       now,
	}
}

func poolGSI1SK(idleSince int64) string {
	return fmt.Sprintf("%s#%s", idlePoolPrefix, time.Unix(idleSince, 0).UTC().Format(time.RFC3339))
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

// BindToJob binds a just-provisioned runner's slot 0 directly to the job
// that triggered its creation, moving the instance PROVISIONING->ACTIVE and
// the job PROVISIONING->ALLOCATED in the same transaction. Writing the
// job's runner_id/slot_index here — mirroring what AllocateIdleRunner does
// on the reuse path — is what lets handleCompleted later find this exact
// slot and call MarkIdle; without it, slot 0 would stay BUSY forever once
// its job completes.
//
// Any other slot (runnerCount > 1) is promoted straight to IDLE with its own
// idle_timeout clock starting now, and the pool's GSI1 entry is populated —
// making it immediately reusable by a different, concurrently queued job of
// the same profile, without waiting for a whole new instance, and without
// waiting for the instance to actually finish booting: reusing a slot that's
// about to be ready is never worse than launching a whole new instance,
// which would take just as long to boot from scratch.
//
// Every slot also gets starting_since = now, independent of its Busy/Idle
// status — the "this slot's process hasn't confirmed itself yet" marker
// MarkSlotReady clears once its runner-ready callback arrives. It's what
// lets cleanup's boot-timeout sweep (reconcileStuckStarting) tell a slot
// whose install genuinely failed apart from one that's simply busy or idle
// as normal, without gating allocability on it.
func (c *Client) BindToJob(ctx context.Context, runnerID string, jobID, workflowRunID int64, sel PoolSelector, runnerCount int, idleTimeout time.Duration) error {
	now := time.Now()

	setClauses := []string{
		"#status = :active",
		"slots[0].#status = :busy",
		"slots[0].starting_since = :now",
		"slots[0].job_id = :jobID",
		"slots[0].workflow_run_id = :wfID",
	}
	removeClauses := []string{"gsi2pk", "gsi2sk"}
	values := map[string]types.AttributeValue{
		":provisioning": &types.AttributeValueMemberS{Value: string(RunnerProvisioning)},
		":active":       &types.AttributeValueMemberS{Value: string(RunnerActive)},
		":busy":         &types.AttributeValueMemberS{Value: string(SlotBusy)},
		":now":          &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
		":jobID":        &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", jobID)},
		":wfID":         &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", workflowRunID)},
	}

	if runnerCount > 1 {
		terminateAfter := now.Add(idleTimeout).Unix()
		setClauses = append(setClauses, "gsi1pk = :gsi1pk", "gsi1sk = :gsi1sk")
		values[":gsi1pk"] = &types.AttributeValueMemberS{Value: sel.gsi1PK()}
		values[":gsi1sk"] = &types.AttributeValueMemberS{Value: poolGSI1SK(now.Unix())}
		values[":idleSlot"] = &types.AttributeValueMemberS{Value: string(SlotIdle)}
		values[":idleSince"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())}
		values[":term"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", terminateAfter)}
		for i := 1; i < runnerCount; i++ {
			setClauses = append(setClauses,
				fmt.Sprintf("slots[%d].#status = :idleSlot", i),
				fmt.Sprintf("slots[%d].idle_since = :idleSince", i),
				fmt.Sprintf("slots[%d].terminate_after = :term", i),
				fmt.Sprintf("slots[%d].starting_since = :now", i),
			)
		}
	}

	_, err := c.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(c.table),
					Key: map[string]types.AttributeValue{
						"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
						"sk": &types.AttributeValueMemberS{Value: stateSK},
					},
					ConditionExpression: aws.String("#status = :provisioning"),
					UpdateExpression: aws.String(
						"SET " + joinClauses(setClauses) + " REMOVE " + joinClauses(removeClauses),
					),
					ExpressionAttributeNames:  map[string]string{"#status": "status"},
					ExpressionAttributeValues: values,
				},
			},
			{
				Update: &types.Update{
					TableName: aws.String(c.table),
					Key: map[string]types.AttributeValue{
						"pk": &types.AttributeValueMemberS{Value: jobPK(jobID)},
						"sk": &types.AttributeValueMemberS{Value: stateSK},
					},
					ConditionExpression:      aws.String("#status = :provisioning"),
					UpdateExpression:         aws.String("SET #status = :allocated, runner_id = :runnerID, slot_index = :slotIndex, updated_at = :now REMOVE gsi2pk, gsi2sk"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":provisioning": &types.AttributeValueMemberS{Value: string(JobProvisioning)},
						":allocated":    &types.AttributeValueMemberS{Value: string(JobAllocated)},
						":runnerID":     &types.AttributeValueMemberS{Value: runnerID},
						":slotIndex":    &types.AttributeValueMemberN{Value: "0"},
						":now":          &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("store: bind runner %s to job %d: %w", runnerID, jobID, err)
	}
	return nil
}

// MarkSlotReady confirms that slot slotIndex's runner process actually
// registered with GitHub and started — called by cmd/ready once the
// instance's userdata script's own callback arrives (see internal/ready and
// the ReadyToken doc comment on Runner). It only clears the starting_since
// marker BindToJob set; it never touches the slot's Busy/Idle status, GSI1
// membership, job binding, etc. — those were already correct the instant
// BindToJob ran, since a fast-following job may already have claimed this
// exact slot via AllocateIdleRunner before its boot even finished (see
// BindToJob). Once cleared, starting_since is never set again for this
// slot's lifetime — its ongoing busy/idle churn afterward is real work, not
// something this method needs to know about.
//
// A ConditionalCheckFailed (starting_since already cleared, e.g. a retried
// callback) is swallowed, not an error.
func (c *Client) MarkSlotReady(ctx context.Context, runnerID string, slotIndex int) error {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression: aws.String(fmt.Sprintf("attribute_exists(slots[%d].starting_since)", slotIndex)),
		UpdateExpression:    aws.String(fmt.Sprintf("REMOVE slots[%d].starting_since", slotIndex)),
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return fmt.Errorf("store: mark runner %s slot %d ready: %w", runnerID, slotIndex, err)
	}
	return nil
}

func joinClauses(clauses []string) string {
	out := ""
	for i, c := range clauses {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// AllocateIdleRunner tries to claim one IDLE runner slot from the given
// pool for job, oldest-idle-first. It returns (nil, false, nil) if the pool
// has no idle slot, or every candidate lost its race to another concurrent
// invocation. The Slot IDLE->BUSY and Job QUEUED->ALLOCATED writes happen
// together in one transaction so a crash between them can never leave a
// BUSY slot with no job, or a Job stuck ALLOCATED with no real slot.
func (c *Client) AllocateIdleRunner(ctx context.Context, sel PoolSelector, job *Job) (*Runner, bool, error) {
	out, err := c.ddb.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(c.table),
		IndexName:              aws.String("gsi1"),
		KeyConditionExpression: aws.String("gsi1pk = :pk AND begins_with(gsi1sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: sel.gsi1PK()},
			":prefix": &types.AttributeValueMemberS{Value: idlePoolPrefix + "#"},
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

		claimIdx := -1
		otherIdle := false
		for i, s := range candidate.Slots {
			if s.Status != SlotIdle {
				continue
			}
			if claimIdx == -1 {
				claimIdx = i
			} else {
				otherIdle = true
			}
		}
		if claimIdx == -1 {
			continue // stale GSI1 entry (shouldn't normally happen) — try the next candidate
		}

		now := time.Now().Unix()
		runnerUpdateExpr := fmt.Sprintf(
			"SET slots[%d].#status = :busy, slots[%d].job_id = :jobID, slots[%d].workflow_run_id = :wfID "+
				"REMOVE slots[%d].idle_since, slots[%d].terminate_after",
			claimIdx, claimIdx, claimIdx, claimIdx, claimIdx,
		)
		if !otherIdle {
			runnerUpdateExpr += ", gsi1pk, gsi1sk"
		}

		_, err := c.ddb.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: []types.TransactWriteItem{
				{
					Update: &types.Update{
						TableName: aws.String(c.table),
						Key: map[string]types.AttributeValue{
							"pk": &types.AttributeValueMemberS{Value: runnerPK(candidate.RunnerID)},
							"sk": &types.AttributeValueMemberS{Value: stateSK},
						},
						ConditionExpression:      aws.String(fmt.Sprintf("slots[%d].#status = :idle", claimIdx)),
						UpdateExpression:         aws.String(runnerUpdateExpr),
						ExpressionAttributeNames: map[string]string{"#status": "status"},
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":idle":  &types.AttributeValueMemberS{Value: string(SlotIdle)},
							":busy":  &types.AttributeValueMemberS{Value: string(SlotBusy)},
							":jobID": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", job.JobID)},
							":wfID":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", job.WorkflowRunID)},
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
						UpdateExpression:    aws.String("SET #status = :allocated, runner_id = :runnerID, slot_index = :slotIndex, updated_at = :now REMOVE gsi2pk, gsi2sk"),
						ExpressionAttributeNames: map[string]string{
							"#status": "status",
						},
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":queued":    &types.AttributeValueMemberS{Value: string(JobQueued)},
							":allocated": &types.AttributeValueMemberS{Value: string(JobAllocated)},
							":runnerID":  &types.AttributeValueMemberS{Value: candidate.RunnerID},
							":slotIndex": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", claimIdx)},
							":now":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now)},
						},
					},
				},
			},
		})
		if err == nil {
			candidate.Slots[claimIdx].Status = SlotBusy
			candidate.Slots[claimIdx].JobID = job.JobID
			candidate.Slots[claimIdx].WorkflowRunID = job.WorkflowRunID
			return &candidate, true, nil
		}
		if isConditionalCheckFailed(err) {
			continue // lost the race (or job already claimed) — try the next candidate
		}
		return nil, false, fmt.Errorf("store: allocate runner %s slot %d: %w", candidate.RunnerID, claimIdx, err)
	}
	return nil, false, nil
}

// MarkIdle transitions one runner slot from BUSY back to IDLE once its job
// completes, starting that slot's own idle-timeout clock. sel identifies the
// pool this runner belongs to (from the completed Job row) so the GSI1 pool
// entry can be (re)populated — this slot is now available regardless of any
// sibling slot's state.
func (c *Client) MarkIdle(ctx context.Context, runnerID string, slotIndex int, sel PoolSelector, idleTimeout time.Duration) error {
	now := time.Now()
	terminateAfter := now.Add(idleTimeout).Unix()
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression: aws.String(fmt.Sprintf("slots[%d].#status = :busy", slotIndex)),
		UpdateExpression: aws.String(fmt.Sprintf(
			"SET slots[%d].#status = :idle, slots[%d].idle_since = :now, slots[%d].terminate_after = :term, gsi1pk = :gsi1pk, gsi1sk = :gsi1sk "+
				"REMOVE slots[%d].job_id, slots[%d].workflow_run_id",
			slotIndex, slotIndex, slotIndex, slotIndex, slotIndex,
		)),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":busy":   &types.AttributeValueMemberS{Value: string(SlotBusy)},
			":idle":   &types.AttributeValueMemberS{Value: string(SlotIdle)},
			":now":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
			":term":   &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", terminateAfter)},
			":gsi1pk": &types.AttributeValueMemberS{Value: sel.gsi1PK()},
			":gsi1sk": &types.AttributeValueMemberS{Value: poolGSI1SK(now.Unix())},
		},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return fmt.Errorf("store: mark runner %s slot %d idle: %w", runnerID, slotIndex, err)
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

// ListActiveInstances returns every Runner row currently ACTIVE — the
// candidate set cleanup's per-slot-idle-timeout and auto_terminating_time
// checks run over (see internal/cleanup.eligibleForTermination). A Scan
// (rather than a GSI query) is deliberate: with independent per-slot
// idle/expiry state, "every slot idle, each past its own terminate_after,
// OR the instance older than auto_terminating_time" isn't representable as
// a single GSI sort key. At this platform's scale (a 3-minute sweep over,
// realistically, tens of concurrently live instances) a filtered Scan is
// simpler to reason about than maintaining a second derived multi-slot GSI.
func (c *Client) ListActiveInstances(ctx context.Context) ([]*Runner, error) {
	var runners []*Runner
	input := &dynamodb.ScanInput{
		TableName:                aws.String(c.table),
		FilterExpression:         aws.String("entity_type = :runner AND #status = :active"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":runner": &types.AttributeValueMemberS{Value: "RUNNER"},
			":active": &types.AttributeValueMemberS{Value: string(RunnerActive)},
		},
	}
	for {
		out, err := c.ddb.Scan(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("store: scan active instances: %w", err)
		}
		for _, item := range out.Items {
			var r Runner
			if err := attributevalue.UnmarshalMap(item, &r); err != nil {
				return nil, fmt.Errorf("store: unmarshal runner: %w", err)
			}
			runners = append(runners, &r)
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
	return runners, nil
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
// state the caller observed — e.g. a job claimed a slot between the cleanup
// sweep's read and this write.
func (c *Client) TransitionToTerminating(ctx context.Context, runnerID string) (bool, error) {
	_, err := c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		ConditionExpression: aws.String("#status IN (:provisioning, :active)"),
		UpdateExpression:    aws.String("SET #status = :terminating REMOVE gsi1pk, gsi1sk, gsi2pk, gsi2sk"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":provisioning": &types.AttributeValueMemberS{Value: string(RunnerProvisioning)},
			":active":       &types.AttributeValueMemberS{Value: string(RunnerActive)},
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
			":ttl":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(c.recordTTL).Unix())},
		},
	})
	if err != nil {
		return fmt.Errorf("store: mark runner %s failed: %w", runnerID, err)
	}
	return nil
}

// MarkTerminated finalizes a runner record after its EC2 instance and every
// one of its slots' GitHub registrations have been removed. slots is the
// caller's in-memory copy with each GitHubRunnerID filled in as cleanup
// resolved it, written back wholesale so the final record shows exactly
// what was deregistered. Sets a TTL so the record eventually expires.
func (c *Client) MarkTerminated(ctx context.Context, runnerID string, slots []Slot) error {
	slotsAV, err := attributevalue.Marshal(slots)
	if err != nil {
		return fmt.Errorf("store: marshal slots for runner %s: %w", runnerID, err)
	}

	_, err = c.ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: runnerPK(runnerID)},
			"sk": &types.AttributeValueMemberS{Value: stateSK},
		},
		UpdateExpression: aws.String("SET #status = :terminated, slots = :slots, #ttl = :ttl"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
			"#ttl":    "ttl",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":terminated": &types.AttributeValueMemberS{Value: string(RunnerTerminated)},
			":slots":      slotsAV,
			":ttl":        &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", time.Now().Add(c.recordTTL).Unix())},
		},
	})
	if err != nil {
		return fmt.Errorf("store: mark runner %s terminated: %w", runnerID, err)
	}
	return nil
}
