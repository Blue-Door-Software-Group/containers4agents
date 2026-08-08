package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

type Provider struct {
	host string
}

func New(host string) (*Provider, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, types.ErrProviderUnavailable
	}
	return &Provider{host: host}, nil
}

func (p *Provider) CreateSandbox(ctx context.Context, spec types.SandboxSpec) (types.ProviderContainer, error) {
	if err := p.ensureImage(ctx, spec.Image); err != nil {
		return types.ProviderContainer{}, err
	}

	containerID, err := p.runDocker(
		ctx,
		"create",
		"--network", "none",
		"-w", "/workspace",
		"--label", "c4a.external_id="+spec.ExternalID,
		"--label", "c4a.profile="+spec.Profile,
		"--label", "c4a.image_version="+spec.ImageVersion,
		spec.Image,
		"sh", "-lc", "mkdir -p /workspace && sleep infinity",
	)
	if err != nil {
		return types.ProviderContainer{}, mapDockerErr(err)
	}

	containerID = strings.TrimSpace(containerID)
	if _, err := p.runDocker(ctx, "start", containerID); err != nil {
		return types.ProviderContainer{}, mapDockerErr(err)
	}

	return types.ProviderContainer{
		ProviderRef: containerID,
		State:       "running",
	}, nil
}

func (p *Provider) StartSandbox(ctx context.Context, providerRef string) error {
	if _, err := p.runDocker(ctx, "start", providerRef); err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func (p *Provider) StopSandbox(ctx context.Context, providerRef string) error {
	if _, err := p.runDocker(ctx, "stop", "--time", "10", providerRef); err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func (p *Provider) PruneSandbox(ctx context.Context, providerRef string) error {
	if _, err := p.runDocker(ctx, "rm", "-f", providerRef); err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func (p *Provider) Exec(ctx context.Context, providerRef string, req types.ExecRequest) (types.ExecResult, error) {
	return types.ExecResult{}, types.ErrProviderNotImplemented
}

func (p *Provider) UploadFile(ctx context.Context, providerRef string, path string, content []byte) error {
	return types.ErrProviderNotImplemented
}

func (p *Provider) DownloadFile(ctx context.Context, providerRef string, path string) ([]byte, error) {
	return nil, types.ErrProviderNotImplemented
}

func (p *Provider) DeletePath(ctx context.Context, providerRef string, path string) error {
	return types.ErrProviderNotImplemented
}

func (p *Provider) ListWorkspace(ctx context.Context, providerRef string) ([]types.FileEntry, error) {
	return nil, types.ErrProviderNotImplemented
}

func (p *Provider) ReadUsage(ctx context.Context, providerRef string) (types.UsageSnapshot, error) {
	return types.UsageSnapshot{}, nil
}

func (p *Provider) State(ctx context.Context, providerRef string) (string, error) {
	out, err := p.runDocker(ctx, "inspect", "-f", "{{.State.Running}}", providerRef)
	if err != nil {
		return "", mapDockerErr(err)
	}
	switch strings.TrimSpace(out) {
	case "true":
		return "running", nil
	case "false":
		return "stopped", nil
	default:
		return "", types.ErrProviderInternal
	}
}

func (p *Provider) ensureImage(ctx context.Context, imageRef string) error {
	_, err := p.runDocker(ctx, "pull", imageRef)
	if err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func mapDockerErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case strings.Contains(err.Error(), "No such container"):
		return types.ErrProviderNotFound
	case strings.Contains(err.Error(), "Cannot connect to the Docker daemon"):
		return types.ErrProviderUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return types.ErrProviderTimeout
	default:
		return fmt.Errorf("%w: %v", types.ErrProviderInternal, err)
	}
}

func (p *Provider) Close() error {
	return nil
}

func (p *Provider) runDocker(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	if p.host != "" {
		command.Env = append(os.Environ(), "DOCKER_HOST="+p.host)
	}
	out, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

var _ types.ContainerProvider = (*Provider)(nil)
var _ interface {
	State(context.Context, string) (string, error)
} = (*Provider)(nil)
