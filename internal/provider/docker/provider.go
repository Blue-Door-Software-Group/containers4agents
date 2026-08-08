package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

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
	if providerRef == "" {
		return types.ExecResult{}, fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}
	if len(req.Command) == 0 {
		return types.ExecResult{}, fmt.Errorf("%w: empty command", types.ErrProviderInternal)
	}

	stdoutBuffer := newLimitedBuffer(req.MaxStdoutBytes)
	stderrBuffer := newLimitedBuffer(req.MaxStderrBytes)

	args := make([]string, 0, 5+len(req.Command))
	args = append(args, "exec", "-w", "/workspace", providerRef)
	args = append(args, req.Command...)

	command := exec.CommandContext(ctx, "docker", args...)
	if p.host != "" {
		command.Env = append(os.Environ(), "DOCKER_HOST="+p.host)
	}
	command.Stdout = stdoutBuffer
	command.Stderr = stderrBuffer

	err := command.Run()
	result := types.ExecResult{
		Stdout:       stdoutBuffer.String(),
		Stderr:       stderrBuffer.String(),
		TruncatedOut: stdoutBuffer.Truncated(),
		TruncatedErr: stderrBuffer.Truncated(),
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		result.Status = "timeout"
		return result, nil
	}
	if err == nil {
		exitCode := 0
		result.Status = "ok"
		result.ExitCode = &exitCode
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode := exitErr.ExitCode()
		result.ExitCode = &exitCode
		if strings.Contains(result.Stderr, "is not running") {
			result.Status = "killed"
		} else {
			result.Status = "user_error"
		}
		return result, nil
	}

	return types.ExecResult{}, mapDockerErr(fmt.Errorf("docker %s: %w", strings.Join(args, " "), err))
}

func (p *Provider) UploadFile(ctx context.Context, providerRef string, path string, content []byte) error {
	if providerRef == "" {
		return fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}
	if path == "" {
		return fmt.Errorf("%w: missing file path", types.ErrProviderInternal)
	}

	script := strings.Join([]string{
		"set -eu",
		`rel="$1"`,
		`dest="/workspace/$rel"`,
		`parent=$(dirname "$dest")`,
		`mkdir -p "$parent"`,
		`resolved_parent=$(readlink -f "$parent")`,
		`case "$resolved_parent/" in /workspace/*) ;; *) echo "path escapes workspace" >&2; exit 42;; esac`,
		`final="$resolved_parent/$(basename "$dest")"`,
		`if [ -L "$final" ]; then`,
		`  resolved_final=$(readlink -f "$final" || true)`,
		`  case "$resolved_final" in /workspace/*) ;; *) echo "symlink escapes workspace" >&2; exit 42;; esac`,
		`fi`,
		`cat > "$final"`,
	}, "; ")

	_, stderr, err := p.runDockerCommand(ctx, content, "exec", "-i", providerRef, "sh", "-lc", script, "--", path)
	if err != nil {
		return p.mapFileOpErr(err, stderr, "upload", path)
	}
	return nil
}

func (p *Provider) DownloadFile(ctx context.Context, providerRef string, path string) ([]byte, error) {
	if providerRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}
	if path == "" {
		return nil, fmt.Errorf("%w: missing file path", types.ErrProviderInternal)
	}

	script := strings.Join([]string{
		"set -eu",
		`rel="$1"`,
		`target="/workspace/$rel"`,
		`resolved=$(readlink -f "$target" 2>/dev/null || true)`,
		`if [ -z "$resolved" ]; then echo "not found" >&2; exit 44; fi`,
		`case "$resolved" in /workspace/*) ;; *) echo "path escapes workspace" >&2; exit 42;; esac`,
		`if [ ! -f "$resolved" ]; then echo "not a regular file" >&2; exit 45; fi`,
		`cat "$resolved"`,
	}, "; ")

	stdout, stderr, err := p.runDockerCommand(ctx, nil, "exec", providerRef, "sh", "-lc", script, "--", path)
	if err != nil {
		return nil, p.mapFileOpErr(err, stderr, "download", path)
	}
	return stdout, nil
}

func (p *Provider) DeletePath(ctx context.Context, providerRef string, path string) error {
	if providerRef == "" {
		return fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}
	if path == "" {
		return fmt.Errorf("%w: missing file path", types.ErrProviderInternal)
	}

	script := strings.Join([]string{
		"set -eu",
		`rel="$1"`,
		`target="/workspace/$rel"`,
		`resolved=$(readlink -f "$target" 2>/dev/null || true)`,
		`if [ -z "$resolved" ]; then echo "not found" >&2; exit 44; fi`,
		`case "$resolved" in /workspace/*) ;; *) echo "path escapes workspace" >&2; exit 42;; esac`,
		`if [ "$resolved" = "/workspace" ]; then echo "cannot delete workspace root" >&2; exit 42; fi`,
		`rm -rf -- "$resolved"`,
	}, "; ")

	_, stderr, err := p.runDockerCommand(ctx, nil, "exec", providerRef, "sh", "-lc", script, "--", path)
	if err != nil {
		return p.mapFileOpErr(err, stderr, "delete", path)
	}
	return nil
}

func (p *Provider) ListWorkspace(ctx context.Context, providerRef string) ([]types.FileEntry, error) {
	if providerRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}

	script := strings.Join([]string{
		"set -eu",
		`find /workspace -mindepth 1 -print0 | while IFS= read -r -d '' p; do`,
		`  rel="${p#/workspace/}"`,
		`  name="${rel##*/}"`,
		`  if [ -d "$p" ]; then`,
		`    typ="directory"; size="0"; hash=""`,
		`  elif [ -f "$p" ]; then`,
		`    typ="file"; size=$(wc -c < "$p" | tr -d ' '); hash=$(sha256sum "$p" | awk '{print $1}')`,
		`  elif [ -L "$p" ]; then`,
		`    typ="symlink"; size="0"; hash=""`,
		`  else`,
		`    typ="other"; size="0"; hash=""`,
		`  fi`,
		`  mtime=$(stat -c %Y "$p" 2>/dev/null || stat -f %m "$p")`,
		`  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$rel" "$name" "$typ" "$size" "$mtime" "$hash"`,
		`done`,
	}, "; ")

	stdout, stderr, err := p.runDockerCommand(ctx, nil, "exec", providerRef, "sh", "-lc", script)
	if err != nil {
		return nil, p.mapFileOpErr(err, stderr, "manifest", "")
	}

	text := strings.TrimSpace(string(stdout))
	if text == "" {
		return []types.FileEntry{}, nil
	}

	lines := strings.Split(text, "\n")
	entries := make([]types.FileEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) != 6 {
			return nil, fmt.Errorf("%w: malformed manifest entry %q", types.ErrProviderInternal, line)
		}

		size, parseErr := strconv.ParseInt(parts[3], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: invalid file size %q", types.ErrProviderInternal, parts[3])
		}
		mtimeUnix, parseErr := strconv.ParseInt(parts[4], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: invalid mtime %q", types.ErrProviderInternal, parts[4])
		}

		entry := types.FileEntry{
			Path:      parts[0],
			Name:      parts[1],
			Type:      parts[2],
			SizeBytes: size,
			ModTime:   time.Unix(mtimeUnix, 0).UTC(),
		}
		if entry.Type == "file" && parts[5] != "" {
			entry.Hash = &types.FileHash{
				Algorithm: "sha256",
				Value:     parts[5],
			}
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

func (p *Provider) ReadUsage(ctx context.Context, providerRef string) (types.UsageSnapshot, error) {
	if providerRef == "" {
		return types.UsageSnapshot{}, fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}

	script := strings.Join([]string{
		"set -eu",
		`bytes=$(find /workspace -type f -exec wc -c {} + 2>/dev/null | awk 'BEGIN{s=0} { if ($2 != "total") s += $1 } END { print s + 0 }')`,
		`count=$(find /workspace -type f 2>/dev/null | wc -l | tr -d ' ')`,
		`printf '%s\t%s\n' "$bytes" "$count"`,
	}, "; ")

	stdout, stderr, err := p.runDockerCommand(ctx, nil, "exec", providerRef, "sh", "-lc", script)
	if err != nil {
		return types.UsageSnapshot{}, p.mapFileOpErr(err, stderr, "usage", "")
	}

	parts := strings.Split(strings.TrimSpace(string(stdout)), "\t")
	if len(parts) != 2 {
		return types.UsageSnapshot{}, fmt.Errorf("%w: malformed usage output %q", types.ErrProviderInternal, strings.TrimSpace(string(stdout)))
	}

	bytesUsed, parseErr := strconv.ParseInt(parts[0], 10, 64)
	if parseErr != nil {
		return types.UsageSnapshot{}, fmt.Errorf("%w: invalid usage bytes %q", types.ErrProviderInternal, parts[0])
	}
	fileCount, parseErr := strconv.ParseInt(parts[1], 10, 64)
	if parseErr != nil {
		return types.UsageSnapshot{}, fmt.Errorf("%w: invalid usage file count %q", types.ErrProviderInternal, parts[1])
	}

	return types.UsageSnapshot{
		WorkspaceBytes: bytesUsed,
		FileCount:      fileCount,
	}, nil
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

func (p *Provider) runDockerCommand(ctx context.Context, stdin []byte, args ...string) ([]byte, string, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	if p.host != "" {
		command.Env = append(os.Environ(), "DOCKER_HOST="+p.host)
	}
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	return stdout.Bytes(), strings.TrimSpace(stderr.String()), err
}

func (p *Provider) mapFileOpErr(err error, stderr string, operation string, path string) error {
	if err == nil {
		return nil
	}
	if code, ok := dockerExitCode(err); ok {
		switch code {
		case 42:
			return types.ErrProviderDenied
		case 44:
			return types.ErrProviderNotFound
		case 45:
			return types.ErrProviderDenied
		}
	}

	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	switch {
	case strings.Contains(message, "No such container"):
		return types.ErrProviderNotFound
	case strings.Contains(message, "Cannot connect to the Docker daemon"):
		return types.ErrProviderUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return types.ErrProviderTimeout
	default:
		if path != "" {
			return fmt.Errorf("%w: %s %s: %s", types.ErrProviderInternal, operation, path, message)
		}
		return fmt.Errorf("%w: %s: %s", types.ErrProviderInternal, operation, message)
	}
}

func dockerExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	return exitErr.ExitCode(), true
}

var _ types.ContainerProvider = (*Provider)(nil)
var _ interface {
	State(context.Context, string) (string, error)
} = (*Provider)(nil)

type limitedBuffer struct {
	builder   strings.Builder
	maxBytes  int
	written   int
	truncated bool
}

func newLimitedBuffer(maxBytes int) *limitedBuffer {
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &limitedBuffer{maxBytes: maxBytes}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.written += len(p)

	remaining := b.maxBytes - b.builder.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.builder.Write(p[:remaining])
		} else {
			_, _ = b.builder.Write(p)
		}
	}
	if b.written > b.maxBytes {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.builder.String()
}

func (b *limitedBuffer) Truncated() bool {
	return b.truncated
}

var _ io.Writer = (*limitedBuffer)(nil)
