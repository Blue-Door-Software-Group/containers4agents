package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

// specProvider records the spec of the sandbox it creates.
type specProvider struct {
	fakeProvider
	spec *types.SandboxSpec
}

func (p specProvider) CreateSandbox(_ context.Context, spec types.SandboxSpec) (types.ProviderContainer, error) {
	*p.spec = spec
	return types.ProviderContainer{ProviderRef: "provider-1", State: StateRunning}, nil
}

// memRepo is a repository holding rows in a map, enough for create.
func memRepo(rows map[string]*repository.Container, created *repository.CreateParams) fakeRepo {
	return fakeRepo{
		createFn: func(_ context.Context, p repository.CreateParams) (*repository.Container, error) {
			if r, ok := rows[p.ExternalID]; ok && !(p.ReplaceTerminal && (r.State == StatePruned || r.State == StateFailed)) {
				return nil, repository.ErrExternalIDTaken
			}
			*created = p
			row := &repository.Container{ExternalID: p.ExternalID, State: StateCreating, Profile: p.Profile, ImageVersion: p.ImageVersion, ImageName: p.ImageName, LifecyclePolicy: p.LifecyclePolicy, StoragePolicy: p.StoragePolicy}
			rows[p.ExternalID] = row
			return row, nil
		},
		getFn: func(_ context.Context, id string) (*repository.Container, error) {
			if r, ok := rows[id]; ok {
				return r, nil
			}
			return nil, repository.ErrNotFound
		},
		updateIfCurrent: func(_ context.Context, p repository.UpdateStateParams, _ string) (*repository.Container, error) {
			r := rows[p.ExternalID]
			r.State = p.State
			r.IdleStopDeadlineAt = p.IdleStopDeadlineAt
			return r, nil
		},
	}
}

func imagesConfig() config.Config {
	base := config.Config{DefaultProfile: "prod-strict", ActiveImageVersion: "v1", DockerImage: "alpine:3.20",
		IdleStopTimeout: 20 * time.Minute, StoppedRetentionTimeout: 24 * time.Hour, ContainerMemoryMaxBytes: 1 << 30}
	dev := base
	dev.DefaultProfile, dev.DockerImage, dev.IdleStopTimeout, dev.ContainerMemoryMaxBytes = "dev-egress", "c4a-swarm-dev:local", 6*time.Hour, 8<<30
	cfg := base
	cfg.DefaultImageName = "runtime"
	cfg.Images = map[string]config.Image{
		"runtime":   {Name: "runtime", Network: types.NetworkNone, Settings: base},
		"swarm-dev": {Name: "swarm-dev", Network: types.NetworkEgress, Settings: dev},
	}
	return cfg
}

func TestCreateContainer_ImageAndExternalID(t *testing.T) {
	rows := map[string]*repository.Container{}
	var created repository.CreateParams
	var spec types.SandboxSpec
	svc := NewService(memRepo(rows, &created), specProvider{spec: &spec}, imagesConfig())

	before := time.Now().UTC()
	view, err := svc.CreateContainer(context.Background(), CreateOptions{Image: "swarm-dev", ExternalID: "swarm-u-p"})
	if err != nil {
		t.Fatal(err)
	}
	if view.ExternalID != "swarm-u-p" || view.ImageName != "swarm-dev" || view.Network != types.NetworkEgress || view.Existed {
		t.Fatalf("view = %+v", view)
	}
	if created.ImageName != "swarm-dev" || created.Profile != "dev-egress" || !created.ReplaceTerminal {
		t.Fatalf("create params = %+v", created)
	}
	if spec.Image != "c4a-swarm-dev:local" || spec.Network != types.NetworkEgress || spec.Runtime.MemoryMaxBytes != 8<<30 {
		t.Fatalf("sandbox spec = %+v", spec)
	}
	if d := view.IdleStopDeadlineAt.Sub(before); d < 5*time.Hour {
		t.Fatalf("idle deadline from the image's 6h, got %v", d)
	}

	// The same id again: a conflict with the existing container, or it back with ReturnExisting.
	_, err = svc.CreateContainer(context.Background(), CreateOptions{Image: "swarm-dev", ExternalID: "swarm-u-p"})
	var exists *ExistsError
	if !errors.As(err, &exists) || exists.View.ExternalID != "swarm-u-p" {
		t.Fatalf("want ExistsError, got %v", err)
	}
	again, err := svc.CreateContainer(context.Background(), CreateOptions{ExternalID: "swarm-u-p", ReturnExisting: true})
	if err != nil || !again.Existed || again.ImageName != "swarm-dev" {
		t.Fatalf("return existing: %+v %v", again, err)
	}

	// A pruned container's id is reused.
	rows["swarm-u-p"].State = StatePruned
	reused, err := svc.CreateContainer(context.Background(), CreateOptions{Image: "swarm-dev", ExternalID: "swarm-u-p"})
	if err != nil || reused.Existed {
		t.Fatalf("reuse pruned id: %+v %v", reused, err)
	}
}

func TestCreateContainer_DefaultsAndUnknownImage(t *testing.T) {
	rows := map[string]*repository.Container{}
	var created repository.CreateParams
	var spec types.SandboxSpec
	svc := NewService(memRepo(rows, &created), specProvider{spec: &spec}, imagesConfig())

	view, err := svc.CreateContainer(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ExternalID) != 36 || created.ReplaceTerminal || spec.Image != "alpine:3.20" || spec.Network != types.NetworkNone || view.ImageName != "runtime" {
		t.Fatalf("default create: view %+v params %+v spec %+v", view, created, spec)
	}
	if _, err := svc.CreateContainer(context.Background(), CreateOptions{Image: "nope"}); !errors.Is(err, ErrUnknownImage) {
		t.Fatalf("want ErrUnknownImage, got %v", err)
	}
}

// Without a registry (today's deployments), a create is exactly as before: no image name stored.
func TestCreateContainer_NoRegistryStoresNoImageName(t *testing.T) {
	rows := map[string]*repository.Container{}
	var created repository.CreateParams
	var spec types.SandboxSpec
	cfg := config.Config{DefaultProfile: "prod-strict", ActiveImageVersion: "v1", DockerImage: "alpine:3.20", IdleStopTimeout: time.Minute}
	svc := NewService(memRepo(rows, &created), specProvider{spec: &spec}, cfg)
	view, err := svc.CreateContainer(context.Background(), CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if created.ImageName != "" || spec.Image != "alpine:3.20" || view.Network != types.NetworkNone {
		t.Fatalf("params %+v spec %+v view %+v", created, spec, view)
	}
}

// prunedProvider records the sandboxes it prunes.
type prunedProvider struct {
	specProvider
	pruned *[]string
}

func (p prunedProvider) PruneSandbox(_ context.Context, ref string) error {
	*p.pruned = append(*p.pruned, ref)
	return nil
}

// Reusing a failed container's id removes what the provider still has of it first.
func TestCreateContainer_ReusingAFailedIDRemovesItsSandbox(t *testing.T) {
	ref := "docker-old"
	rows := map[string]*repository.Container{"swarm-u-p": {ExternalID: "swarm-u-p", State: StateFailed, ProviderRef: &ref}}
	var created repository.CreateParams
	var spec types.SandboxSpec
	var pruned []string
	svc := NewService(memRepo(rows, &created), prunedProvider{specProvider: specProvider{spec: &spec}, pruned: &pruned}, imagesConfig())
	if _, err := svc.CreateContainer(context.Background(), CreateOptions{Image: "swarm-dev", ExternalID: "swarm-u-p"}); err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0] != "docker-old" {
		t.Fatalf("pruned %v", pruned)
	}
}

// policyConfig is imagesConfig with swarm-dev allowing stop-only and persistent storage.
func policyConfig() config.Config {
	cfg := imagesConfig()
	dev := cfg.Images["swarm-dev"]
	dev.AllowStopOnly, dev.AllowPersistent = true, true
	cfg.Images["swarm-dev"] = dev
	return cfg
}

func TestCreateContainer_PoliciesNeedTheImagesConsent(t *testing.T) {
	rows := map[string]*repository.Container{}
	var created repository.CreateParams
	var spec types.SandboxSpec
	svc := NewService(memRepo(rows, &created), specProvider{spec: &spec}, policyConfig())

	for _, opts := range []CreateOptions{
		{Lifecycle: LifecycleStopOnly},             // the default image does not allow it
		{Storage: StoragePersistent},               // nor this
		{Image: "swarm-dev", Lifecycle: "forever"}, // not a policy
		{Image: "swarm-dev", Storage: "nfs"},       // not a policy
	} {
		if _, err := svc.CreateContainer(context.Background(), opts); !errors.Is(err, ErrPolicyNotAllowed) {
			t.Fatalf("%+v: want ErrPolicyNotAllowed, got %v", opts, err)
		}
	}
	view, err := svc.CreateContainer(context.Background(), CreateOptions{Image: "swarm-dev", ExternalID: "swarm-u-p", Lifecycle: LifecycleStopOnly, Storage: StoragePersistent})
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Persistent || created.LifecyclePolicy != LifecycleStopOnly || created.StoragePolicy != StoragePersistent {
		t.Fatalf("spec %+v params %+v", spec, created)
	}
	if view.Lifecycle != LifecycleStopOnly || view.Storage != StoragePersistent {
		t.Fatalf("view %+v", view)
	}
	// The defaults stay the defaults.
	if _, err := svc.CreateContainer(context.Background(), CreateOptions{}); err != nil || spec.Persistent || created.LifecyclePolicy != "" {
		t.Fatalf("default create: %v %+v %+v", err, spec, created)
	}
}

// purgeProvider records volume removals.
type purgeProvider struct {
	fakeProvider
	purged *[]string
}

func (p purgeProvider) RemoveWorkspaceVolume(_ context.Context, id string) error {
	*p.purged = append(*p.purged, id)
	return nil
}

func TestStopAndPrune_StopOnlyAndPurge(t *testing.T) {
	ref := "docker-1"
	rows := map[string]*repository.Container{
		"keep": {ExternalID: "keep", State: StateRunning, ProviderRef: &ref, LifecyclePolicy: LifecycleStopOnly, StoragePolicy: StoragePersistent},
		"tmp":  {ExternalID: "tmp", State: StateRunning, ProviderRef: &ref},
	}
	var created repository.CreateParams
	var purged []string
	repo := memRepo(rows, &created)
	var lastStop repository.UpdateStateParams
	repo.updateIfCurrent = func(_ context.Context, p repository.UpdateStateParams, _ string) (*repository.Container, error) {
		r := rows[p.ExternalID]
		r.State = p.State
		if p.State == StateStopped {
			lastStop = p
		}
		return r, nil
	}
	svc := NewService(repo, purgeProvider{purged: &purged}, policyConfig())

	if _, err := svc.StopContainer(context.Background(), "keep"); err != nil {
		t.Fatal(err)
	}
	if lastStop.RetentionPruneDeadlineAt != nil || !lastStop.ClearRetentionPruneDeadline {
		t.Fatalf("a stop-only container gets no retention deadline: %+v", lastStop)
	}
	if _, err := svc.StopContainer(context.Background(), "tmp"); err != nil {
		t.Fatal(err)
	}
	if lastStop.RetentionPruneDeadlineAt == nil {
		t.Fatal("a default container keeps its retention deadline")
	}

	rows["keep"].State, rows["tmp"].State = StateStopped, StateStopped
	if _, err := svc.PruneContainer(context.Background(), "keep"); err != nil {
		t.Fatal(err)
	}
	if len(purged) != 0 {
		t.Fatal("a plain prune keeps the volume")
	}
	rows["keep"].State = StateStopped
	if _, err := svc.PruneContainerWith(context.Background(), "keep", PruneOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PruneContainerWith(context.Background(), "tmp", PruneOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || purged[0] != "keep" {
		t.Fatalf("purge removes only a persistent container's volume: %v", purged)
	}
}
