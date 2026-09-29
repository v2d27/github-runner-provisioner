package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// RunnerProcessCommand lists an instance's runner processes: one
// Runner.Listener per registered runner, plus a Runner.Worker for each one
// currently executing a job. `|| true` because grep exits 1 when nothing
// matches, which SSM would otherwise report as a failed command.
const RunnerProcessCommand = "ps aux | grep -E 'Runner.Listener|Runner.Worker' | grep -v grep || true"

// sendCommandBatch is SendCommand's per-call InstanceIds limit.
const sendCommandBatch = 50

// RunnerProcesses is what RunnerProcessCommand found on one instance.
type RunnerProcesses struct {
	Listeners int
	Workers   int
	Output    string
}

// Busy reports whether any runner on the instance is executing a job.
func (p RunnerProcesses) Busy() bool { return p.Workers > 0 }

// ParseRunnerProcesses counts the Runner.Listener/Runner.Worker lines in
// RunnerProcessCommand's output. It doesn't depend on where the runner is
// installed, so it works for any layout.
func ParseRunnerProcesses(output string) RunnerProcesses {
	p := RunnerProcesses{Output: strings.TrimSpace(output)}
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.Contains(line, "Runner.Worker"):
			p.Workers++
		case strings.Contains(line, "Runner.Listener"):
			p.Listeners++
		}
	}
	return p
}

// SSM runs RunnerProcessCommand on runner instances via Run Command.
type SSM struct {
	client *ssm.Client
}

func NewSSM(cfg aws.Config) *SSM {
	return &SSM{client: ssm.NewFromConfig(cfg)}
}

// CheckRunnerProcesses runs RunnerProcessCommand on every instance in ids
// whose SSM agent is online and returns the result for each one that
// answered within wait. An instance missing from the result couldn't be
// checked (agent offline, command failed or timed out): callers must fall
// back to another signal, never assume it is idle.
func (s *SSM) CheckRunnerProcesses(ctx context.Context, ids []string, wait time.Duration) (map[string]RunnerProcesses, error) {
	results := make(map[string]RunnerProcesses)
	online, err := s.onlineInstances(ctx, ids)
	if err != nil || len(online) == 0 {
		return results, err
	}

	var commandIDs []string
	for start := 0; start < len(online); start += sendCommandBatch {
		batch := online[start:min(start+sendCommandBatch, len(online))]
		out, err := s.client.SendCommand(ctx, &ssm.SendCommandInput{
			DocumentName:   aws.String("AWS-RunShellScript"),
			InstanceIds:    batch,
			Parameters:     map[string][]string{"commands": {RunnerProcessCommand}},
			TimeoutSeconds: aws.Int32(30),
			Comment:        aws.String("github-runner-provisioner: runner process check"),
		})
		if err != nil {
			return results, fmt.Errorf("aws: send runner process check: %w", err)
		}
		commandIDs = append(commandIDs, aws.ToString(out.Command.CommandId))
	}

	pending := make(map[string]bool, len(online))
	for _, id := range online {
		pending[id] = true
	}
	deadline := time.Now().Add(wait)
	for len(pending) > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		case <-time.After(time.Second):
		}
		for _, commandID := range commandIDs {
			p := ssm.NewListCommandInvocationsPaginator(s.client, &ssm.ListCommandInvocationsInput{
				CommandId: aws.String(commandID),
				Details:   true,
			})
			for p.HasMorePages() {
				page, err := p.NextPage(ctx)
				if err != nil {
					return results, fmt.Errorf("aws: list runner process check results: %w", err)
				}
				for _, inv := range page.CommandInvocations {
					id := aws.ToString(inv.InstanceId)
					if !pending[id] {
						continue
					}
					switch inv.Status {
					case types.CommandInvocationStatusSuccess:
						var output string
						for _, plugin := range inv.CommandPlugins {
							output += aws.ToString(plugin.Output)
						}
						results[id] = ParseRunnerProcesses(output)
						delete(pending, id)
					case types.CommandInvocationStatusFailed, types.CommandInvocationStatusTimedOut, types.CommandInvocationStatusCancelled:
						delete(pending, id)
					}
				}
			}
		}
	}
	return results, nil
}

// onlineInstances filters ids down to instances whose SSM agent is
// currently online: SendCommand rejects the whole call if any instance in
// it isn't a reachable managed instance.
func (s *SSM) onlineInstances(ctx context.Context, ids []string) ([]string, error) {
	var online []string
	for start := 0; start < len(ids); start += sendCommandBatch {
		batch := ids[start:min(start+sendCommandBatch, len(ids))]
		p := ssm.NewDescribeInstanceInformationPaginator(s.client, &ssm.DescribeInstanceInformationInput{
			Filters: []types.InstanceInformationStringFilter{{Key: aws.String("InstanceIds"), Values: batch}},
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("aws: describe ssm instance information: %w", err)
			}
			for _, info := range page.InstanceInformationList {
				if info.PingStatus == types.PingStatusOnline {
					online = append(online, aws.ToString(info.InstanceId))
				}
			}
		}
	}
	return online, nil
}
