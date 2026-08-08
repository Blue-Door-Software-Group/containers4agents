package policy

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const DefaultProfileName = "prod-strict"

type ResourcePolicy struct {
	Version       string                    `yaml:"version"`
	ActiveProfile string                    `yaml:"active_profile"`
	Capacity      CapacityPolicy            `yaml:"capacity"`
	Profiles      map[string]RuntimeProfile `yaml:"profiles"`
}

type CapacityPolicy struct {
	ActiveExecLimit       int `yaml:"active_exec_limit"`
	RunningContainerLimit int `yaml:"running_container_limit"`
	TotalContainerLimit   int `yaml:"total_container_limit"`
}

type RuntimeProfile struct {
	CPU struct {
		MaxCores float64 `yaml:"max_cores"`
	} `yaml:"cpu"`
	Memory struct {
		MaxBytes int64 `yaml:"max_bytes"`
	} `yaml:"memory"`
	Disk struct {
		WorkspaceMaxBytes int64 `yaml:"workspace_max_bytes"`
		TmpMaxBytes       int64 `yaml:"tmp_max_bytes"`
		FileCountMax      int64 `yaml:"file_count_max"`
	} `yaml:"disk"`
	Process struct {
		PIDsMax int `yaml:"pids_max"`
	} `yaml:"process"`
	Exec struct {
		TimeoutDefaultSeconds     int    `yaml:"timeout_default_seconds"`
		TimeoutMaxSeconds         int    `yaml:"timeout_max_seconds"`
		StdoutMaxBytes            int    `yaml:"stdout_max_bytes"`
		StderrMaxBytes            int    `yaml:"stderr_max_bytes"`
		MaxConcurrentPerContainer int    `yaml:"max_concurrent_per_container"`
		User                      string `yaml:"user"`
	} `yaml:"exec"`
	Files struct {
		MaxUploadBytes   int `yaml:"max_upload_bytes"`
		MaxDownloadBytes int `yaml:"max_download_bytes"`
	} `yaml:"files"`
	Security struct {
		DropAllCapabilities bool   `yaml:"drop_all_capabilities"`
		NoNewPrivileges     bool   `yaml:"no_new_privileges"`
		ReadOnlyRootFS      bool   `yaml:"read_only_root_fs"`
		SeccompProfilePath  string `yaml:"seccomp_profile_path"`
	} `yaml:"security"`
}

var ErrInvalidPolicy = errors.New("invalid resource policy")

func Defaults() ResourcePolicy {
	p := ResourcePolicy{
		Version:       "v1",
		ActiveProfile: DefaultProfileName,
		Capacity: CapacityPolicy{
			ActiveExecLimit:       10,
			RunningContainerLimit: 30,
			TotalContainerLimit:   100,
		},
		Profiles: map[string]RuntimeProfile{},
	}

	profile := RuntimeProfile{}
	profile.CPU.MaxCores = 2.0
	profile.Memory.MaxBytes = 3 * 1024 * 1024 * 1024
	profile.Disk.WorkspaceMaxBytes = 10 * 1024 * 1024 * 1024
	profile.Disk.TmpMaxBytes = 2 * 1024 * 1024 * 1024
	profile.Disk.FileCountMax = 100000
	profile.Process.PIDsMax = 256
	profile.Exec.TimeoutDefaultSeconds = 120
	profile.Exec.TimeoutMaxSeconds = 300
	profile.Exec.StdoutMaxBytes = 4 * 1024 * 1024
	profile.Exec.StderrMaxBytes = 4 * 1024 * 1024
	profile.Exec.MaxConcurrentPerContainer = 1
	profile.Exec.User = "65534:65534"
	profile.Files.MaxUploadBytes = 64 * 1024 * 1024
	profile.Files.MaxDownloadBytes = 64 * 1024 * 1024
	profile.Security.DropAllCapabilities = true
	profile.Security.NoNewPrivileges = true
	profile.Security.ReadOnlyRootFS = false
	p.Profiles[DefaultProfileName] = profile
	return p
}

func Load(resourcePolicyPath string) (ResourcePolicy, string, error) {
	defaults := Defaults()
	if resourcePolicyPath == "" {
		return defaults, "defaults", nil
	}

	raw, err := os.ReadFile(resourcePolicyPath)
	if err != nil {
		return defaults, "defaults", nil
	}

	var loaded ResourcePolicy
	if err := yaml.Unmarshal(raw, &loaded); err != nil {
		return ResourcePolicy{}, "", fmt.Errorf("%w: parse resource policy file: %v", ErrInvalidPolicy, err)
	}
	if err := Validate(loaded); err != nil {
		return ResourcePolicy{}, "", err
	}
	return loaded, "file", nil
}

func Validate(p ResourcePolicy) error {
	if p.Version == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidPolicy)
	}
	if p.ActiveProfile == "" {
		return fmt.Errorf("%w: active_profile is required", ErrInvalidPolicy)
	}
	if p.Capacity.ActiveExecLimit <= 0 || p.Capacity.RunningContainerLimit <= 0 || p.Capacity.TotalContainerLimit <= 0 {
		return fmt.Errorf("%w: capacity limits must be greater than zero", ErrInvalidPolicy)
	}
	if p.Capacity.ActiveExecLimit > p.Capacity.RunningContainerLimit || p.Capacity.RunningContainerLimit > p.Capacity.TotalContainerLimit {
		return fmt.Errorf("%w: capacity ordering must satisfy active <= running <= total", ErrInvalidPolicy)
	}
	if len(p.Profiles) == 0 {
		return fmt.Errorf("%w: at least one profile is required", ErrInvalidPolicy)
	}
	profile, ok := p.Profiles[p.ActiveProfile]
	if !ok {
		return fmt.Errorf("%w: active_profile %q not found", ErrInvalidPolicy, p.ActiveProfile)
	}
	if profile.CPU.MaxCores <= 0 {
		return fmt.Errorf("%w: cpu.max_cores must be greater than zero", ErrInvalidPolicy)
	}
	if profile.Memory.MaxBytes <= 0 {
		return fmt.Errorf("%w: memory.max_bytes must be greater than zero", ErrInvalidPolicy)
	}
	if profile.Disk.WorkspaceMaxBytes <= 0 || profile.Disk.TmpMaxBytes <= 0 || profile.Disk.FileCountMax <= 0 {
		return fmt.Errorf("%w: disk limits must be greater than zero", ErrInvalidPolicy)
	}
	if profile.Process.PIDsMax <= 0 {
		return fmt.Errorf("%w: process.pids_max must be greater than zero", ErrInvalidPolicy)
	}
	if profile.Exec.TimeoutDefaultSeconds <= 0 || profile.Exec.TimeoutMaxSeconds <= 0 {
		return fmt.Errorf("%w: exec timeouts must be greater than zero", ErrInvalidPolicy)
	}
	if profile.Exec.TimeoutDefaultSeconds > profile.Exec.TimeoutMaxSeconds {
		return fmt.Errorf("%w: exec.timeout_default_seconds must be <= exec.timeout_max_seconds", ErrInvalidPolicy)
	}
	if profile.Exec.StdoutMaxBytes <= 0 || profile.Exec.StderrMaxBytes <= 0 || profile.Exec.MaxConcurrentPerContainer <= 0 {
		return fmt.Errorf("%w: exec limits must be greater than zero", ErrInvalidPolicy)
	}
	if profile.Files.MaxUploadBytes <= 0 || profile.Files.MaxDownloadBytes <= 0 {
		return fmt.Errorf("%w: file transfer limits must be greater than zero", ErrInvalidPolicy)
	}
	return nil
}
