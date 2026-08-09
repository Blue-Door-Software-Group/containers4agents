package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

type Provider struct {
	host            string
	cli             *client.Client
	imagePullPolicy string
	registryAuth    *registry.AuthConfig
}

func New(host string) (*Provider, error) {
	return NewWithOptions(host, Options{})
}

type Options struct {
	ImagePullPolicy  string
	RegistryServer   string
	RegistryUsername string
	RegistryPassword string
}

func NewWithOptions(host string, opts Options) (*Provider, error) {
	clientOpts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	}
	if strings.TrimSpace(host) != "" {
		clientOpts = append(clientOpts, client.WithHost(host))
	}

	cli, err := client.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("%w: init docker client: %v", types.ErrProviderUnavailable, err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(pingCtx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("%w: ping docker daemon: %v", types.ErrProviderUnavailable, err)
	}

	pullPolicy, err := normalizeImagePullPolicy(opts.ImagePullPolicy)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	registryAuth, err := buildRegistryAuthConfig(opts.RegistryServer, opts.RegistryUsername, opts.RegistryPassword)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}

	return &Provider{
		host:            host,
		cli:             cli,
		imagePullPolicy: pullPolicy,
		registryAuth:    registryAuth,
	}, nil
}

func (p *Provider) CreateSandbox(ctx context.Context, spec types.SandboxSpec) (types.ProviderContainer, error) {
	if err := p.ensureImage(ctx, spec.Image); err != nil {
		return types.ProviderContainer{}, err
	}

	cfg := &container.Config{
		Image:      spec.Image,
		WorkingDir: "/workspace",
		Labels: map[string]string{
			"c4a.external_id":   spec.ExternalID,
			"c4a.profile":       spec.Profile,
			"c4a.image_version": spec.ImageVersion,
		},
		Cmd: []string{"sh", "-lc", "mkdir -p /workspace && chmod 0777 /workspace && sleep infinity"},
	}

	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode("none"),
		Resources: container.Resources{
			Memory:     spec.Runtime.MemoryMaxBytes,
			MemorySwap: spec.Runtime.MemoryMaxBytes,
			NanoCPUs:   int64(spec.Runtime.CPUMaxCores * 1_000_000_000),
		},
		CapDrop:         []string{},
		SecurityOpt:     []string{},
		ReadonlyRootfs:  spec.Runtime.ReadOnlyRootFS,
		PublishAllPorts: false,
		Privileged:      false,
		AutoRemove:      false,
		RestartPolicy:   container.RestartPolicy{Name: "no"},
	}
	if spec.Runtime.MemoryMaxBytes <= 0 {
		hostCfg.Resources.Memory = 0
		hostCfg.Resources.MemorySwap = 0
	}
	if spec.Runtime.CPUMaxCores <= 0 {
		hostCfg.Resources.NanoCPUs = 0
	}
	if spec.Runtime.PIDsMax > 0 {
		pids := int64(spec.Runtime.PIDsMax)
		hostCfg.Resources.PidsLimit = &pids
	}
	if spec.Runtime.DropAllCapabilities {
		hostCfg.CapDrop = append(hostCfg.CapDrop, "ALL")
	}
	if spec.Runtime.NoNewPrivileges {
		hostCfg.SecurityOpt = append(hostCfg.SecurityOpt, "no-new-privileges")
	}
	if spec.Runtime.SeccompProfilePath != "" {
		hostCfg.SecurityOpt = append(hostCfg.SecurityOpt, "seccomp="+spec.Runtime.SeccompProfilePath)
	}
	if spec.Runtime.TmpDiskMaxBytes > 0 {
		hostCfg.Tmpfs = map[string]string{
			"/tmp": fmt.Sprintf("rw,noexec,nosuid,size=%d", spec.Runtime.TmpDiskMaxBytes),
		}
	}

	created, err := p.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return types.ProviderContainer{}, mapDockerErr(err)
	}
	containerID := strings.TrimSpace(created.ID)
	if err := p.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return types.ProviderContainer{}, mapDockerErr(err)
	}

	return types.ProviderContainer{
		ProviderRef: containerID,
		State:       "running",
	}, nil
}

func (p *Provider) StartSandbox(ctx context.Context, providerRef string) error {
	if err := p.cli.ContainerStart(ctx, providerRef, container.StartOptions{}); err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func (p *Provider) StopSandbox(ctx context.Context, providerRef string) error {
	timeout := 10
	if err := p.cli.ContainerStop(ctx, providerRef, container.StopOptions{Timeout: &timeout}); err != nil {
		return mapDockerErr(err)
	}
	return nil
}

func (p *Provider) PruneSandbox(ctx context.Context, providerRef string) error {
	if err := p.cli.ContainerRemove(ctx, providerRef, container.RemoveOptions{Force: true}); err != nil {
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

	exitCode, err := p.runExec(ctx, providerRef, req.Command, req.User, nil, stdoutBuffer, stderrBuffer)
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
	if err != nil {
		return types.ExecResult{}, mapDockerErr(err)
	}
	if exitCode == 0 {
		exitCode := 0
		result.Status = "ok"
		result.ExitCode = &exitCode
		return result, nil
	}
	result.ExitCode = &exitCode
	result.Status = mapExecExitStatus(result.Stderr, exitCode)
	return result, nil
}

func mapExecExitStatus(stderr string, exitCode int) string {
	message := strings.ToLower(stderr)
	switch {
	case strings.Contains(message, "out of memory"), strings.Contains(message, "oom"), exitCode == 137:
		return "oom"
	case strings.Contains(message, "no space left on device"), strings.Contains(message, "disk quota exceeded"):
		return "disk_quota"
	case strings.Contains(message, "operation not permitted"), strings.Contains(message, "permission denied"), strings.Contains(message, "seccomp"):
		return "sandbox_violation"
	case strings.Contains(message, "is not running"):
		return "killed"
	default:
		return "user_error"
	}
}

func (p *Provider) UploadFile(ctx context.Context, providerRef string, path string, content io.Reader) error {
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

	stderrBuffer := newLimitedBuffer(16 * 1024)
	if err := p.runFileScript(
		ctx,
		providerRef,
		[]string{"sh", "-lc", script, "--", path},
		content,
		io.Discard,
		stderrBuffer,
		"upload",
		path,
	); err != nil {
		return err
	}
	return nil
}

func (p *Provider) DownloadFile(ctx context.Context, providerRef string, path string, dst io.Writer) error {
	if providerRef == "" {
		return fmt.Errorf("%w: missing provider reference", types.ErrProviderInternal)
	}
	if path == "" {
		return fmt.Errorf("%w: missing file path", types.ErrProviderInternal)
	}
	if dst == nil {
		return fmt.Errorf("%w: missing destination writer", types.ErrProviderInternal)
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

	stderrBuffer := newLimitedBuffer(16 * 1024)
	if err := p.runFileScript(
		ctx,
		providerRef,
		[]string{"sh", "-lc", script, "--", path},
		nil,
		dst,
		stderrBuffer,
		"download",
		path,
	); err != nil {
		return err
	}
	return nil
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

	stderrBuffer := newLimitedBuffer(16 * 1024)
	if err := p.runFileScript(
		ctx,
		providerRef,
		[]string{"sh", "-lc", script, "--", path},
		nil,
		io.Discard,
		stderrBuffer,
		"delete",
		path,
	); err != nil {
		return err
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

	stdoutBuffer := &bytes.Buffer{}
	stderrBuffer := newLimitedBuffer(16 * 1024)
	if err := p.runFileScript(
		ctx,
		providerRef,
		[]string{"sh", "-lc", script},
		nil,
		stdoutBuffer,
		stderrBuffer,
		"manifest",
		"",
	); err != nil {
		return nil, err
	}

	text := strings.TrimSpace(stdoutBuffer.String())
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

	stdoutBuffer := &bytes.Buffer{}
	stderrBuffer := newLimitedBuffer(16 * 1024)
	if err := p.runFileScript(
		ctx,
		providerRef,
		[]string{"sh", "-lc", script},
		nil,
		stdoutBuffer,
		stderrBuffer,
		"usage",
		"",
	); err != nil {
		return types.UsageSnapshot{}, err
	}

	parts := strings.Split(strings.TrimSpace(stdoutBuffer.String()), "\t")
	if len(parts) != 2 {
		return types.UsageSnapshot{}, fmt.Errorf("%w: malformed usage output %q", types.ErrProviderInternal, strings.TrimSpace(stdoutBuffer.String()))
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
	inspect, err := p.cli.ContainerInspect(ctx, providerRef)
	if err != nil {
		return "", mapDockerErr(err)
	}
	if inspect.State == nil {
		return "", types.ErrProviderInternal
	}
	switch inspect.State.Running {
	case true:
		return "running", nil
	case false:
		return "stopped", nil
	}
	return "", types.ErrProviderInternal
}

func (p *Provider) ensureImage(ctx context.Context, imageRef string) error {
	if p.imagePullPolicy == "never" || p.imagePullPolicy == "if-not-present" {
		_, _, inspectErr := p.cli.ImageInspectWithRaw(ctx, imageRef)
		if inspectErr == nil {
			return nil
		}
		if p.imagePullPolicy == "never" {
			return fmt.Errorf("%w: image %s not present locally and pull policy is never", types.ErrProviderNotFound, imageRef)
		}
		if !errdefs.IsNotFound(inspectErr) {
			return mapDockerErr(inspectErr)
		}
	}

	pullOpts := image.PullOptions{}
	if p.registryAuth != nil {
		rawAuth, err := json.Marshal(p.registryAuth)
		if err != nil {
			return fmt.Errorf("%w: encode registry auth: %v", types.ErrProviderInternal, err)
		}
		pullOpts.RegistryAuth = base64.StdEncoding.EncodeToString(rawAuth)
	}

	reader, err := p.cli.ImagePull(ctx, imageRef, pullOpts)
	if err != nil {
		return mapDockerErr(err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)
	return nil
}

func normalizeImagePullPolicy(policy string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(policy))
	if normalized == "" {
		return "if-not-present", nil
	}
	switch normalized {
	case "always", "if-not-present", "never":
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: invalid image pull policy %q", types.ErrProviderInternal, policy)
	}
}

func buildRegistryAuthConfig(server, username, password string) (*registry.AuthConfig, error) {
	if strings.TrimSpace(username) == "" && strings.TrimSpace(password) == "" {
		return nil, nil
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("%w: registry username/password must both be set", types.ErrProviderInternal)
	}
	registryServer := strings.TrimSpace(server)
	if registryServer == "" {
		registryServer = "ghcr.io"
	}
	return &registry.AuthConfig{
		Username:      strings.TrimSpace(username),
		Password:      strings.TrimSpace(password),
		ServerAddress: registryServer,
	}, nil
}

func mapDockerErr(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %v", types.ErrProviderTimeout, err)
	case errdefs.IsNotFound(err):
		return fmt.Errorf("%w: %v", types.ErrProviderNotFound, err)
	case errdefs.IsUnavailable(err), errdefs.IsSystem(err):
		return fmt.Errorf("%w: %v", types.ErrProviderUnavailable, err)
	case errdefs.IsConflict(err):
		return fmt.Errorf("%w: %v", types.ErrProviderConflict, err)
	case errdefs.IsForbidden(err), errdefs.IsUnauthorized(err):
		return fmt.Errorf("%w: %v", types.ErrProviderDenied, err)
	case strings.Contains(message, "cannot connect to the docker daemon"),
		strings.Contains(message, "error during connect"),
		strings.Contains(message, "is the docker daemon running"),
		strings.Contains(message, "connection refused"),
		strings.Contains(message, "no such host"):
		// Preserve provider-unavailable semantics for untyped connectivity errors.
		return fmt.Errorf("%w: %v", types.ErrProviderUnavailable, err)
	default:
		return fmt.Errorf("%w: %v", types.ErrProviderInternal, err)
	}
}

func (p *Provider) Close() error {
	if p == nil || p.cli == nil {
		return nil
	}
	return p.cli.Close()
}

func (p *Provider) mapFileOpErr(err error, stderr string, operation string, path string) error {
	if err == nil {
		return nil
	}

	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	switch {
	case errdefs.IsNotFound(err):
		return types.ErrProviderNotFound
	case errdefs.IsUnavailable(err), errdefs.IsSystem(err):
		return types.ErrProviderUnavailable
	case strings.Contains(strings.ToLower(message), "no space left on device"), strings.Contains(strings.ToLower(message), "disk quota exceeded"):
		return types.ErrProviderQuotaExceeded
	case errors.Is(err, context.DeadlineExceeded):
		return types.ErrProviderTimeout
	default:
		if path != "" {
			return fmt.Errorf("%w: %s %s: %s", types.ErrProviderInternal, operation, path, message)
		}
		return fmt.Errorf("%w: %s: %s", types.ErrProviderInternal, operation, message)
	}
}

func (p *Provider) mapFileExitCode(code int, stderr string, operation string, filePath string) error {
	switch code {
	case 42, 45:
		return types.ErrProviderDenied
	case 44:
		return types.ErrProviderNotFound
	}
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = fmt.Sprintf("operation %s failed with exit code %d", operation, code)
	}
	if filePath != "" {
		return fmt.Errorf("%w: %s %s: %s", types.ErrProviderInternal, operation, filePath, message)
	}
	return fmt.Errorf("%w: %s: %s", types.ErrProviderInternal, operation, message)
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

func (p *Provider) runExec(
	ctx context.Context,
	providerRef string,
	cmd []string,
	user string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (int, error) {
	create, err := p.cli.ContainerExecCreate(ctx, providerRef, container.ExecOptions{
		Cmd:          cmd,
		User:         user,
		WorkingDir:   "/workspace",
		AttachStdin:  stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	})
	if err != nil {
		return 0, err
	}

	attached, err := p.cli.ContainerExecAttach(ctx, create.ID, container.ExecAttachOptions{
		Tty: false,
	})
	if err != nil {
		return 0, err
	}
	defer attached.Close()

	stdoutWriter := stdout
	if stdoutWriter == nil {
		stdoutWriter = io.Discard
	}
	stderrWriter := stderr
	if stderrWriter == nil {
		stderrWriter = io.Discard
	}

	var stdinErr error
	if stdin != nil {
		go func() {
			_, stdinErr = io.Copy(attached.Conn, stdin)
			_ = attached.CloseWrite()
		}()
	}

	if _, err := stdcopy.StdCopy(stdoutWriter, stderrWriter, attached.Reader); err != nil && !errors.Is(err, io.EOF) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, context.DeadlineExceeded
		}
		return 0, err
	}
	if stdinErr != nil && !errors.Is(stdinErr, io.EOF) {
		return 0, stdinErr
	}

	inspect, err := p.cli.ContainerExecInspect(ctx, create.ID)
	if err != nil {
		return 0, err
	}
	return inspect.ExitCode, nil
}

func (p *Provider) runFileScript(
	ctx context.Context,
	providerRef string,
	cmd []string,
	stdin io.Reader,
	stdout io.Writer,
	stderrBuffer *limitedBuffer,
	operation string,
	filePath string,
) error {
	exitCode, err := p.runExec(ctx, providerRef, cmd, "", stdin, stdout, stderrBuffer)
	if err != nil {
		return p.mapFileOpErr(err, stderrBuffer.String(), operation, filePath)
	}
	if exitCode != 0 {
		return p.mapFileExitCode(exitCode, stderrBuffer.String(), operation, filePath)
	}
	return nil
}
