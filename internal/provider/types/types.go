package types

import (
	"context"
	"time"
)

type SandboxSpec struct {
	ExternalID   string
	Profile      string
	ImageVersion string
	Image        string
}

type ProviderContainer struct {
	ProviderRef string
	State       string
}

type ExecRequest struct {
	Command        []string
	Timeout        time.Duration
	MaxStdoutBytes int
	MaxStderrBytes int
}

type ExecResult struct {
	Status       string
	ExitCode     *int
	Stdout       string
	Stderr       string
	TruncatedOut bool
	TruncatedErr bool
}

type FileEntry struct {
	Path      string
	Name      string
	Type      string
	SizeBytes int64
	ModTime   time.Time
	Hash      *FileHash
}

type FileHash struct {
	Algorithm string
	Value     string
}

type UsageSnapshot struct {
	WorkspaceBytes int64
	FileCount      int64
}

type ContainerProvider interface {
	CreateSandbox(ctx context.Context, spec SandboxSpec) (ProviderContainer, error)
	StartSandbox(ctx context.Context, providerRef string) error
	StopSandbox(ctx context.Context, providerRef string) error
	PruneSandbox(ctx context.Context, providerRef string) error
	Exec(ctx context.Context, providerRef string, req ExecRequest) (ExecResult, error)
	UploadFile(ctx context.Context, providerRef string, path string, content []byte) error
	DownloadFile(ctx context.Context, providerRef string, path string) ([]byte, error)
	DeletePath(ctx context.Context, providerRef string, path string) error
	ListWorkspace(ctx context.Context, providerRef string) ([]FileEntry, error)
	ReadUsage(ctx context.Context, providerRef string) (UsageSnapshot, error)
}
