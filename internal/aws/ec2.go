// Package aws wraps the AWS SDK v2 clients this platform calls directly:
// EC2 (runner instances) and Secrets Manager (GitHub App credentials).
// DynamoDB access lives in internal/store, which owns the data model.
package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// Tags every runner instance is launched with. TagDeployment + TagRunnerID
// let cleanup find an instance even when its instance ID never made it into
// DynamoDB (see ListManagedInstances).
const (
	TagManagedBy   = "ManagedBy"
	ManagedByValue = "github-runner-provisioner"
	TagDeployment  = "RunnerDeployment"
	TagRunnerID    = "RunnerID"
)

// ManagedInstance is one not-yet-terminated runner instance as EC2 sees it.
type ManagedInstance struct {
	InstanceID string
	LaunchTime time.Time
	Tags       map[string]string
}

// EC2 wraps the subset of the EC2 API the provision/cleanup Lambdas need.
// The launch template referenced by LaunchInput.LaunchTemplateID already
// fixes network interfaces, security group, IAM instance profile and IMDSv2
// (see infrastructure/modules/github-runner-on-aws/ec2.tf) — only the profile-specific shape (AMI,
// instance type, Spot, UserData) is supplied per call, so adding a new
// runner profile in configs/runner.yaml never requires a terraform apply.
type EC2 struct {
	client *ec2.Client
}

func NewEC2(cfg aws.Config) *EC2 {
	return &EC2{client: ec2.NewFromConfig(cfg)}
}

// LaunchInput describes one runner instance to launch.
type LaunchInput struct {
	LaunchTemplateID string
	ImageID          string
	InstanceType     string
	Spot             bool
	// DiskSizeGB overrides the AMI's default root EBS volume size. Zero
	// leaves the AMI's own default size in place.
	DiskSizeGB int
	// UserData is plaintext; RunInstance base64-encodes it as EC2 requires.
	UserData string
	Tags     map[string]string
}

// RunInstance launches exactly one instance and returns its instance ID.
func (e *EC2) RunInstance(ctx context.Context, in LaunchInput) (string, error) {
	var marketOptions *types.InstanceMarketOptionsRequest
	if in.Spot {
		marketOptions = &types.InstanceMarketOptionsRequest{
			MarketType: types.MarketTypeSpot,
			SpotOptions: &types.SpotMarketOptions{
				SpotInstanceType:             types.SpotInstanceTypeOneTime,
				InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorTerminate,
			},
		}
	}

	blockDeviceMappings, err := e.rootVolumeOverride(ctx, in.ImageID, in.DiskSizeGB)
	if err != nil {
		return "", fmt.Errorf("aws: resolve root volume for %s: %w", in.ImageID, err)
	}

	out, err := e.client.RunInstances(ctx, &ec2.RunInstancesInput{
		MinCount: aws.Int32(1),
		MaxCount: aws.Int32(1),
		LaunchTemplate: &types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(in.LaunchTemplateID),
			Version:          aws.String("$Latest"),
		},
		ImageId:               aws.String(in.ImageID),
		InstanceType:          types.InstanceType(in.InstanceType),
		InstanceMarketOptions: marketOptions,
		BlockDeviceMappings:   blockDeviceMappings,
		UserData:              aws.String(base64.StdEncoding.EncodeToString([]byte(in.UserData))),
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tagsFromMap(in.Tags)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("aws: run instance: %w", err)
	}
	if len(out.Instances) == 0 || out.Instances[0].InstanceId == nil {
		return "", fmt.Errorf("aws: run instance: no instance returned")
	}
	return *out.Instances[0].InstanceId, nil
}

// rootVolumeOverride resizes imageID's root EBS volume to diskSizeGB. Returns
// nil (no override — the AMI's own default size applies) if diskSizeGB <= 0.
// The root device name varies by AMI (e.g. Ubuntu's /dev/sda1 vs Amazon
// Linux's /dev/xvda), so it's looked up per launch rather than assumed.
func (e *EC2) rootVolumeOverride(ctx context.Context, imageID string, diskSizeGB int) ([]types.BlockDeviceMapping, error) {
	if diskSizeGB <= 0 {
		return nil, nil
	}

	out, err := e.client.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	if err != nil {
		return nil, fmt.Errorf("describe image: %w", err)
	}
	if len(out.Images) == 0 || out.Images[0].RootDeviceName == nil {
		return nil, fmt.Errorf("image %s has no root device name", imageID)
	}

	return []types.BlockDeviceMapping{
		{
			DeviceName: out.Images[0].RootDeviceName,
			Ebs: &types.EbsBlockDevice{
				VolumeSize:          aws.Int32(int32(diskSizeGB)),
				VolumeType:          types.VolumeTypeGp3,
				DeleteOnTermination: aws.Bool(true),
				Encrypted:           aws.Bool(true),
			},
		},
	}, nil
}

// TerminateInstance terminates a single instance. Terminating an instance
// that's already gone is not an error — EC2 reports a recently terminated
// instance as already-terminated, and one it has since forgotten entirely
// as InvalidInstanceID.NotFound; both are treated as success.
func (e *EC2) TerminateInstance(ctx context.Context, instanceID string) error {
	_, err := e.client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidInstanceID.NotFound" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("aws: terminate instance %s: %w", instanceID, err)
	}
	return nil
}

// ListManagedInstances returns every pending/running/stopping/stopped
// instance tagged as a runner of the given deployment.
func (e *EC2) ListManagedInstances(ctx context.Context, deployment string) ([]ManagedInstance, error) {
	p := ec2.NewDescribeInstancesPaginator(e.client, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{Name: aws.String("tag:" + TagManagedBy), Values: []string{ManagedByValue}},
			{Name: aws.String("tag:" + TagDeployment), Values: []string{deployment}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	var out []ManagedInstance
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws: list managed instances: %w", err)
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				tags := make(map[string]string, len(i.Tags))
				for _, t := range i.Tags {
					tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
				}
				out = append(out, ManagedInstance{
					InstanceID: aws.ToString(i.InstanceId),
					LaunchTime: aws.ToTime(i.LaunchTime),
					Tags:       tags,
				})
			}
		}
	}
	return out, nil
}

// InstanceState reports the current lifecycle state (e.g. "running",
// "terminated") of an instance. found is false if EC2 no longer knows about
// the instance at all.
func (e *EC2) InstanceState(ctx context.Context, instanceID string) (state string, found bool, err error) {
	out, err := e.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", false, fmt.Errorf("aws: describe instance %s: %w", instanceID, err)
	}
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			if i.State != nil {
				return string(i.State.Name), true, nil
			}
		}
	}
	return "", false, nil
}

func tagsFromMap(m map[string]string) []types.Tag {
	tags := make([]types.Tag, 0, len(m))
	for k, v := range m {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return tags
}
