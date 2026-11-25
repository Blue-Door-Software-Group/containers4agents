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
			row := &repository.Container{ExternalID: p.ExternalID, State: StateCreating, Profile: p.Profile, ImageVersion: p.ImageVersion, ImageName: p.ImageName}
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
