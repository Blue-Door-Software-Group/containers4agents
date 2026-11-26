package policy

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultProfileName = "prod-strict"

type ResourcePolicy struct {
	Version       string                    `yaml:"version"`
	ActiveProfile string                    `yaml:"active_profile"`
	Capacity      CapacityPolicy            `yaml:"capacity"`
	Profiles      map[string]RuntimeProfile `yaml:"profiles"`
	// DefaultImage names the image a create without one gets; empty means ImplicitImageName.
	DefaultImage string `yaml:"default_image"`
	// Images are the container images callers may choose by name. Without any, the server has
	// one implicit image (ImplicitImageName) built from C4A_DOCKER_IMAGE and the active profile,
	// which is the behaviour before images existed.
	Images map[string]ImageSpec `yaml:"images"`
}

// ImplicitImageName is the image every container gets when none is chosen and no registry says
// otherwise: C4A_DOCKER_IMAGE under the active profile, with no network.
const ImplicitImageName = "runtime"

// Network modes an image may have. A caller chooses an image, never a network: egress is the
// operator's decision, made here.
const (
	NetworkNone   = "none"
	NetworkEgress = "egress"
)

// ImageSpec is one registered image.
type ImageSpec struct {
	// Ref is the image reference; ${VAR} is expanded from the environment (e.g. ${C4A_DOCKER_IMAGE}).
	Ref string `yaml:"ref"`
	// Profile names the runtime profile (limits, exec, files, security); empty means the active one.
	Profile string `yaml:"profile"`
	// Network is none (the default) or egress (outbound only, on a dedicated bridge network).
	Network string `yaml:"network"`
	// IdleStopTimeout and StoppedRetentionTimeout override the global lifecycle times
	// (Go durations such as "6h"); empty keeps C4A_IDLE_STOP_TIMEOUT / C4A_STOPPED_RETENTION_TIMEOUT.
	IdleStopTimeout         string `yaml:"idle_stop_timeout"`
	StoppedRetentionTimeout string `yaml:"stopped_retention_timeout"`
	// AllowStopOnly lets a create ask for "lifecycle": "stop_only" (never pruned by the
	// reconciler); AllowPersistent for "storage": "persistent" (/workspace on a named volume).
	AllowStopOnly   bool `yaml:"allow_stop_only"`
	AllowPersistent bool `yaml:"allow_persistent"`
}

// imageNamePattern is what a registered image name may look like.
var imageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

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
	if err := validateProfile(profile); err != nil {
		return err
	}
	return validateImages(p)
}

// validateImages checks the image registry: names, refs, networks, lifecycle times, and that
// each image's profile exists and is valid (not only the active one).
func validateImages(p ResourcePolicy) error {
	for name, img := range p.Images {
		if !imageNamePattern.MatchString(name) {
			return fmt.Errorf("%w: image name %q is invalid", ErrInvalidPolicy, name)
		}
		if os.ExpandEnv(img.Ref) == "" {
			return fmt.Errorf("%w: image %q: ref is required", ErrInvalidPolicy, name)
		}
		switch img.Network {
		case "", NetworkNone, NetworkEgress:
		default:
			return fmt.Errorf("%w: image %q: network must be %q or %q", ErrInvalidPolicy, name, NetworkNone, NetworkEgress)
		}
		for field, v := range map[string]string{"idle_stop_timeout": img.IdleStopTimeout, "stopped_retention_timeout": img.StoppedRetentionTimeout} {
			if v == "" {
				continue
			}
			if d, err := time.ParseDuration(v); err != nil || d <= 0 {
				return fmt.Errorf("%w: image %q: %s must be a positive duration", ErrInvalidPolicy, name, field)
			}
		}
		profileName := img.Profile
		if profileName == "" {
			profileName = p.ActiveProfile
		}
		profile, ok := p.Profiles[profileName]
		if !ok {
			return fmt.Errorf("%w: image %q: profile %q not found", ErrInvalidPolicy, name, profileName)
		}
		if err := validateProfile(profile); err != nil {
			return fmt.Errorf("image %q (profile %q): %w", name, profileName, err)
		}
	}
	if p.DefaultImage != "" && p.DefaultImage != ImplicitImageName {
		if _, ok := p.Images[p.DefaultImage]; !ok {
			return fmt.Errorf("%w: default_image %q is not a registered image", ErrInvalidPolicy, p.DefaultImage)
		}
	}
	return nil
}

// validateProfile checks one runtime profile's limits.
func validateProfile(profile RuntimeProfile) error {
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
