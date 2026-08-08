package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFallsBackToDefaultsWhenPathMissing(t *testing.T) {
	p, source, err := Load("/tmp/does-not-exist-c4a-resource-policy.yaml")
	if err != nil {
		t.Fatalf("expected default fallback, got error: %v", err)
	}
	if source != "defaults" {
		t.Fatalf("expected defaults source, got %q", source)
	}
	if p.ActiveProfile != DefaultProfileName {
		t.Fatalf("expected active profile %q, got %q", DefaultProfileName, p.ActiveProfile)
	}
}

func TestLoadFromValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	policyPath := filepath.Join(tmpDir, "resource-policy.yaml")

	content := `
version: v1
active_profile: prod-strict
capacity:
  active_exec_limit: 10
  running_container_limit: 30
  total_container_limit: 100
profiles:
  prod-strict:
    cpu:
      max_cores: 2.0
    memory:
      max_bytes: 3221225472
    disk:
      workspace_max_bytes: 10737418240
      tmp_max_bytes: 2147483648
      file_count_max: 100000
    process:
      pids_max: 256
    exec:
      timeout_default_seconds: 120
      timeout_max_seconds: 300
      stdout_max_bytes: 4194304
      stderr_max_bytes: 4194304
      max_concurrent_per_container: 1
      user: "65534:65534"
    files:
      max_upload_bytes: 67108864
      max_download_bytes: 67108864
    security:
      drop_all_capabilities: true
      no_new_privileges: true
      read_only_root_fs: false
`
	if err := os.WriteFile(policyPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	p, source, err := Load(policyPath)
	if err != nil {
		t.Fatalf("expected successful load, got: %v", err)
	}
	if source != "file" {
		t.Fatalf("expected source file, got %q", source)
	}
	if p.Capacity.RunningContainerLimit != 30 {
		t.Fatalf("expected running container limit 30, got %d", p.Capacity.RunningContainerLimit)
	}
}

func TestLoadRejectsInvalidFile(t *testing.T) {
	tmpDir := t.TempDir()
	policyPath := filepath.Join(tmpDir, "resource-policy.yaml")

	invalidContent := `
version: v1
active_profile: prod-strict
capacity:
  active_exec_limit: 50
  running_container_limit: 10
  total_container_limit: 100
profiles:
  prod-strict:
    cpu:
      max_cores: 0
`
	if err := os.WriteFile(policyPath, []byte(invalidContent), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	_, _, err := Load(policyPath)
	if err == nil {
		t.Fatal("expected validation error for invalid policy file")
	}
}
