// Package store is the single DynamoDB table this platform uses for all
// state: Runner and Job entities, webhook delivery idempotency records, and
// a small config cache (e.g. the resolved GitHub App installation ID).
//
// Schema (see docs/infrastructure/enterprise-standard-upgrade.md sections
// 11-13 for the conceptual model this refines):
//
//	Runner:      pk=RUNNER#<ulid>        sk=STATE
//	Job:         pk=JOB#<github_job_id>  sk=STATE
//	Event:       pk=EVENT#<delivery_id>  sk=RECEIVED
//	ConfigCache: pk=CONFIG#<key>         sk=STATE
//
// Three sparse GSIs, populated only by the item types/statuses that need
// them:
//
//	GSI1 (allocation pool):     gsi1pk=POOL#<scope>#<owner>#<profile>#<group>, gsi1sk=IDLE#<RFC3339 idle-since>
//	                            — Runner only, present exactly while the instance has at least
//	                              one IDLE runner slot (see Runner.Slots); absent otherwise.
//	GSI2 (status timeline):     gsi2pk=RUNNER#STATUS#<status> or JOB#STATUS#<status>, gsi2sk=<unix seconds>
//	                            — Runner PROVISIONING only (cleanup's stuck-provisioning sweep;
//	                              the per-slot-idle/auto_terminating_time sweep instead scans
//	                              ACTIVE instances directly — see ListActiveInstances),
//	                              Job QUEUED/PROVISIONING (defense-in-depth sweep).
//	GSI3 (instance lookup):     gsi3pk=INSTANCE#<instance_id>, gsi3sk=STATE
//	                            — Runner only, once instance_id is known.
//
// A single conditional/transactional write protocol (see AllocateIdleRunner
// and Job.ClaimForProvisioning) replaces a separate distributed lock table:
// the Job's own status transitions are the lock.
package store

import (
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.me/v2d27/github-runner-provisioner/internal/config"
)

const (
	stateSK    = "STATE"
	receivedSK = "RECEIVED"

	attrTTL = "ttl"
)

// ErrNotFound is returned by point lookups when an item does not exist.
var ErrNotFound = errors.New("store: not found")

// Client wraps the single DynamoDB table backing this platform.
type Client struct {
	ddb   *dynamodb.Client
	table string
	// recordTTL bounds how long every TTL-bearing item this platform writes
	// survives before DynamoDB's own TTL sweep reclaims it (see dynamodb.tf's
	// `ttl` block, which just names the attribute and turns the feature on —
	// the actual duration lives here). One knob for every item type
	// (terminated/failed runners, completed/failed/ignored jobs, webhook
	// delivery-dedup records, and the cached GitHub installation ID) rather
	// than a separately tuned duration per type, set via
	// configs/runner.yaml's infrastructure.main.dynamodb_ttl_days.
	recordTTL time.Duration
}

// New builds a store Client for the given table name (from Terraform output)
// and record TTL (infrastructure.main.dynamodb_ttl_days) — both passed to
// each Lambda as environment variables.
func New(cfg aws.Config, tableName string, recordTTL time.Duration) *Client {
	return &Client{ddb: dynamodb.NewFromConfig(cfg), table: tableName, recordTTL: recordTTL}
}

func runnerPK(runnerID string) string  { return "RUNNER#" + runnerID }
func jobPK(jobID int64) string         { return fmt.Sprintf("JOB#%d", jobID) }
func eventPK(deliveryID string) string { return "EVENT#" + deliveryID }
func configPK(key string) string       { return "CONFIG#" + key }

func runnerStatusGSI2PK(status RunnerStatus) string { return "RUNNER#STATUS#" + string(status) }
func jobStatusGSI2PK(status JobStatus) string       { return "JOB#STATUS#" + string(status) }
func instanceGSI3PK(instanceID string) string       { return "INSTANCE#" + instanceID }

// PoolSelector identifies one allocation pool: a set of runners that are
// interchangeable for a given job (same scope/owner/profile/group).
type PoolSelector struct {
	Scope        config.Scope
	Organization string
	Repository   string
	Profile      string
	Group        string
}

func (s PoolSelector) gsi1PK() string {
	owner := "ORG#" + s.Organization
	if s.Scope == config.ScopeRepository {
		owner = "REPO#" + s.Organization + "/" + s.Repository
	}
	group := s.Group
	if group == "" {
		group = "DEFAULT"
	}
	return fmt.Sprintf("POOL#%s#%s#%s#%s", s.Scope, owner, s.Profile, group)
}

// isConditionalCheckFailed reports whether err is a DynamoDB conditional
// write failure — the expected, non-error outcome of a lost race.
func isConditionalCheckFailed(err error) bool {
	var ccf *types.ConditionalCheckFailedException
	if errors.As(err, &ccf) {
		return true
	}
	var tce *types.TransactionCanceledException
	if errors.As(err, &tce) {
		for _, r := range tce.CancellationReasons {
			if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
				return true
			}
		}
	}
	return false
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
