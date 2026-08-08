package docker

import "testing"

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
