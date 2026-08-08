package docker

import (
	"errors"
	"testing"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

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
