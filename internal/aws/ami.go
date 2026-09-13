package aws

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// amiCacheTTL bounds how long a resolved AMI id is reused within the same
// warm Lambda execution environment, so a burst of provisioning events for
// the same profile doesn't call DescribeImages/GetParameter every time.
const amiCacheTTL = 15 * time.Minute

// canonicalOwnerID is Canonical's official AWS account ID for Ubuntu AMIs.
const canonicalOwnerID = "099720109477"

// ubuntuCodenames maps a supported Ubuntu release version to its codename,
// used to build Canonical's official AMI name pattern.
var ubuntuCodenames = map[string]string{
	"24.04": "noble",
	"22.04": "jammy",
	"20.04": "focal",
}

type amiCacheEntry struct {
	id        string
	expiresAt time.Time
}

// AMIResolver finds the newest AMI matching a profile's declared
// os/version/architecture (config.AMILookup) instead of requiring a
// hardcoded, eventually-stale AMI ID in configs/runner.yaml.
type AMIResolver struct {
	ec2 *ec2.Client
	ssm *ssm.Client

	mu    sync.Mutex
	cache map[string]amiCacheEntry
}

func NewAMIResolver(cfg aws.Config) *AMIResolver {
	return &AMIResolver{
		ec2:   ec2.NewFromConfig(cfg),
		ssm:   ssm.NewFromConfig(cfg),
		cache: make(map[string]amiCacheEntry),
	}
}

// LatestAMI resolves the newest AMI id for the given os/version and EC2
// architecture ("x86_64" or "arm64").
func (r *AMIResolver) LatestAMI(ctx context.Context, os, version, architecture string) (string, error) {
	key := strings.ToLower(os) + "/" + version + "/" + architecture
	if id, ok := r.fromCache(key); ok {
		return id, nil
	}

	var (
		id  string
		err error
	)
	switch strings.ToLower(os) {
	case "amazon-linux":
		id, err = r.latestAmazonLinux(ctx, version, architecture)
	case "ubuntu":
		id, err = r.latestUbuntu(ctx, version, architecture)
	default:
		return "", fmt.Errorf("aws: unsupported ami_lookup.os %q (supported: amazon-linux, ubuntu)", os)
	}
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.cache[key] = amiCacheEntry{id: id, expiresAt: time.Now().Add(amiCacheTTL)}
	r.mu.Unlock()
	return id, nil
}

func (r *AMIResolver) fromCache(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.id, true
}

// latestAmazonLinux resolves via AWS's own published SSM parameters — no
// sorting needed, AWS always keeps these pointed at the current release.
func (r *AMIResolver) latestAmazonLinux(ctx context.Context, version, architecture string) (string, error) {
	if version != "2023" {
		return "", fmt.Errorf("aws: unsupported amazon-linux version %q (supported: 2023)", version)
	}
	name := fmt.Sprintf("/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-%s", architecture)

	out, err := r.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name)})
	if err != nil {
		return "", fmt.Errorf("aws: resolve amazon-linux AMI via %s: %w", name, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", fmt.Errorf("aws: ssm parameter %s has no value", name)
	}
	return *out.Parameter.Value, nil
}

// latestUbuntu queries Canonical's official AMIs directly and picks the
// newest by CreationDate — Ubuntu doesn't publish an SSM shortcut the way
// Amazon Linux does.
func (r *AMIResolver) latestUbuntu(ctx context.Context, version, architecture string) (string, error) {
	codename, ok := ubuntuCodenames[version]
	if !ok {
		return "", fmt.Errorf("aws: unsupported ubuntu version %q (supported: %s)", version, strings.Join(supportedUbuntuVersions(), ", "))
	}
	ubuntuArch := "amd64"
	if architecture == "arm64" {
		ubuntuArch = "arm64"
	}

	out, err := r.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{canonicalOwnerID},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{fmt.Sprintf("ubuntu/images/hvm-ssd-gp3/ubuntu-%s-%s-%s-server-*", codename, version, ubuntuArch)}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("architecture"), Values: []string{architecture}},
			{Name: aws.String("root-device-type"), Values: []string{"ebs"}},
			{Name: aws.String("virtualization-type"), Values: []string{"hvm"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("aws: describe ubuntu %s images: %w", version, err)
	}
	if len(out.Images) == 0 {
		return "", fmt.Errorf("aws: no ubuntu %s/%s AMI found", version, architecture)
	}

	sort.Slice(out.Images, func(i, j int) bool {
		return imageCreationDate(out.Images[i]) > imageCreationDate(out.Images[j])
	})
	return *out.Images[0].ImageId, nil
}

func imageCreationDate(img types.Image) string {
	if img.CreationDate == nil {
		return ""
	}
	return *img.CreationDate // RFC3339 — lexical order matches chronological order
}

func supportedUbuntuVersions() []string {
	versions := make([]string, 0, len(ubuntuCodenames))
	for v := range ubuntuCodenames {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return versions
}
