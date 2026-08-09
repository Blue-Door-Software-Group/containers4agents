package docker

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

func TestNormalizeImagePullPolicy(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default empty", input: "", want: "if-not-present"},
		{name: "always", input: "always", want: "always"},
		{name: "if-not-present mixed case", input: "If-Not-Present", want: "if-not-present"},
		{name: "never", input: "never", want: "never"},
		{name: "invalid", input: "sometimes", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeImagePullPolicy(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestBuildRegistryAuthConfig(t *testing.T) {
	cfg, err := buildRegistryAuthConfig("ghcr.io", "", "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil auth config when credentials omitted, got %+v", cfg)
	}

	cfg, err = buildRegistryAuthConfig("ghcr.io", "user", "token")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil auth config")
	}
	if cfg.ServerAddress != "ghcr.io" || cfg.Username != "user" || cfg.Password != "token" {
		t.Fatalf("unexpected auth config: %+v", cfg)
	}

	if _, err := buildRegistryAuthConfig("ghcr.io", "user", ""); err == nil {
		t.Fatal("expected error for partial credentials")
	}
	if _, err := buildRegistryAuthConfig("ghcr.io", "", "token"); err == nil {
		t.Fatal("expected error for partial credentials")
	}
}

func TestParseManifestOutputHandlesSpecialCharacters(t *testing.T) {
	withSpecials := "dir/line\nbreak and space.txt"
	name := "line\nbreak and space.txt"
	raw := bytes.Join([][]byte{
		[]byte(withSpecials),
		[]byte(name),
		[]byte("file"),
		[]byte("12"),
		[]byte("1723320000"),
		[]byte("abc123"),
	}, []byte{0})
	raw = append(raw, 0)

	entries, err := parseManifestOutput(raw)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if entries[0].Path != withSpecials {
		t.Fatalf("expected path %q, got %q", withSpecials, entries[0].Path)
	}
	if entries[0].Name != name {
		t.Fatalf("expected name %q, got %q", name, entries[0].Name)
	}
	if entries[0].Type != "file" || entries[0].SizeBytes != 12 {
		t.Fatalf("unexpected parsed entry: %+v", entries[0])
	}
	if entries[0].Hash == nil || entries[0].Hash.Value != "abc123" {
		t.Fatalf("expected hash abc123, got %+v", entries[0].Hash)
	}
	if !entries[0].ModTime.Equal(time.Unix(1723320000, 0).UTC()) {
		t.Fatalf("unexpected mtime: %s", entries[0].ModTime)
	}
}

func TestMapExecExitStatus(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		exitCode int
		want     string
	}{
		{name: "oom by message", stderr: "Out of memory while allocating", exitCode: 1, want: "oom"},
		{name: "oom by code 137", stderr: "", exitCode: 137, want: "oom"},
		{name: "disk quota", stderr: "write failed: No space left on device", exitCode: 1, want: "disk_quota"},
		{name: "sandbox violation", stderr: "Operation not permitted", exitCode: 1, want: "sandbox_violation"},
		{name: "killed", stderr: "container is not running", exitCode: 1, want: "killed"},
		{name: "user error fallback", stderr: "command not found", exitCode: 127, want: "user_error"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := mapExecExitStatus(tt.stderr, tt.exitCode)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestMapFileExitCode(t *testing.T) {
	p := &Provider{}
	tests := []struct {
		name     string
		exitCode int
		wantErr  error
	}{
		{name: "path escape maps denied", exitCode: 42, wantErr: types.ErrProviderDenied},
		{name: "not found maps not found", exitCode: 44, wantErr: types.ErrProviderNotFound},
		{name: "not regular file maps denied", exitCode: 45, wantErr: types.ErrProviderDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.mapFileExitCode(tt.exitCode, "stderr", "download", "foo.txt")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestMapDockerErrConnectivityFallback(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "cannot connect daemon", err: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")},
		{name: "error during connect", err: errors.New("error during connect: Get \"http://%2Fvar%2Frun%2Fdocker.sock/_ping\": dial unix /var/run/docker.sock: connect: connection refused")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapDockerErr(tt.err)
			if !errors.Is(got, types.ErrProviderUnavailable) {
				t.Fatalf("expected ErrProviderUnavailable, got %v", got)
			}
			if got.Error() == types.ErrProviderUnavailable.Error() {
				t.Fatalf("expected wrapped provider error details, got %q", got.Error())
			}
		})
	}
}
