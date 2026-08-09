//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/ent/containermetadata"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/reconcile"
)

func TestIdempotencyReplaysCreateResponseForSameKey(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	req1, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	req1.Header.Set("Authorization", "Bearer integration-key")
	req1.Header.Set("Idempotency-Key", "create-key")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first create request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected first create status 201, got %d", resp1.StatusCode)
	}
	var firstPayload map[string]any
	if err := json.NewDecoder(resp1.Body).Decode(&firstPayload); err != nil {
		t.Fatalf("decode first create response: %v", err)
	}

	req2, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	req2.Header.Set("Authorization", "Bearer integration-key")
	req2.Header.Set("Idempotency-Key", "create-key")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("replay create request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("expected replay create status 201, got %d", resp2.StatusCode)
	}
	var replayPayload map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&replayPayload); err != nil {
		t.Fatalf("decode replay create response: %v", err)
	}
	if replayPayload["id"] != firstPayload["id"] {
		t.Fatalf("expected replayed id %v, got %v", firstPayload["id"], replayPayload["id"])
	}
}

func TestIdempotencyUploadReplayAndCapacityAdmission(t *testing.T) {
	env := setupIntegrationEnv(t, func(cfg *config.Config) {
		cfg.RunningContainerLimit = 1
		cfg.TotalContainerLimit = 1
	})
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	uploadReq1, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/idempotent.txt", bytes.NewReader([]byte("hello")))
	uploadReq1.Header.Set("Authorization", "Bearer integration-key")
	uploadReq1.Header.Set("Idempotency-Key", "upload-key")
	uploadResp1, err := http.DefaultClient.Do(uploadReq1)
	if err != nil {
		t.Fatalf("first upload request failed: %v", err)
	}
	_ = uploadResp1.Body.Close()
	if uploadResp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected first upload status 201, got %d", uploadResp1.StatusCode)
	}

	uploadReq2, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/idempotent.txt", bytes.NewReader([]byte("hello")))
	uploadReq2.Header.Set("Authorization", "Bearer integration-key")
	uploadReq2.Header.Set("Idempotency-Key", "upload-key")
	uploadResp2, err := http.DefaultClient.Do(uploadReq2)
	if err != nil {
		t.Fatalf("replay upload request failed: %v", err)
	}
	_ = uploadResp2.Body.Close()
	if uploadResp2.StatusCode != http.StatusCreated {
		t.Fatalf("expected replay upload status 201, got %d", uploadResp2.StatusCode)
	}

	createReq2, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	createReq2.Header.Set("Authorization", "Bearer integration-key")
	createResp2, err := http.DefaultClient.Do(createReq2)
	if err != nil {
		t.Fatalf("second create request failed: %v", err)
	}
	defer createResp2.Body.Close()
	if createResp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected capacity error 429, got %d", createResp2.StatusCode)
	}
}

func TestIdempotencyUploadRejectsBodyMismatch(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	key := "upload-mismatch-key"
	firstReq, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/body.txt", bytes.NewReader([]byte("hello")))
	firstReq.Header.Set("Authorization", "Bearer integration-key")
	firstReq.Header.Set("Idempotency-Key", key)
	firstResp, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first upload request failed: %v", err)
	}
	_ = firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected first upload status 201, got %d", firstResp.StatusCode)
	}

	secondReq, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/body.txt", bytes.NewReader([]byte("different")))
	secondReq.Header.Set("Authorization", "Bearer integration-key")
	secondReq.Header.Set("Idempotency-Key", key)
	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second upload request failed: %v", err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected idempotency mismatch conflict 409, got %d", secondResp.StatusCode)
	}
}

func TestReconcileBackfillsLegacyStoppedAndPrunesWhenExpired(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	stopReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/stop", nil)
	stopReq.Header.Set("Authorization", "Bearer integration-key")
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatalf("stop request failed: %v", err)
	}
	_ = stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d", stopResp.StatusCode)
	}

	repo := repository.NewEntContainerRepository(env.repoClient.Ent)
	lifecycleSvc := lifecycle.NewService(repo, env.provider, env.cfg)

	now := time.Now().UTC()
	_, err = env.repoClient.Ent.ContainerMetadata.
		Update().
		Where(containermetadata.ExternalIDEQ(containerID)).
		SetState(lifecycle.StateStopped).
		SetLastActivityAt(now).
		ClearRetentionPruneDeadlineAt().
		ClearStoppedAt().
		Save(context.Background())
	if err != nil {
		t.Fatalf("prepare legacy stopped row for backfill: %v", err)
	}

	backfillWorker := reconcile.NewWorker(
		env.repoClient.DB,
		repo,
		lifecycleSvc,
		obslog.New("info"),
		true,
		false,
		100*time.Millisecond,
		50*time.Millisecond,
		20*time.Minute,
		time.Hour,
		64001,
	)
	backfillCtx, backfillCancel := context.WithCancel(context.Background())
	go backfillWorker.Run(backfillCtx)
	time.Sleep(350 * time.Millisecond)
	backfillCancel()

	backfilled, err := repo.GetByExternalID(context.Background(), containerID)
	if err != nil {
		t.Fatalf("load backfilled row: %v", err)
	}
	if backfilled.RetentionPruneDeadlineAt == nil {
		t.Fatal("expected retention prune deadline to be backfilled for legacy stopped row")
	}
	if backfilled.StoppedAt == nil {
		t.Fatal("expected stopped_at to be backfilled for legacy stopped row")
	}

	oldActivity := time.Now().UTC().Add(-3 * time.Second)
	_, err = env.repoClient.Ent.ContainerMetadata.
		Update().
		Where(containermetadata.ExternalIDEQ(containerID)).
		SetState(lifecycle.StateStopped).
		SetLastActivityAt(oldActivity).
		ClearRetentionPruneDeadlineAt().
		ClearStoppedAt().
		Save(context.Background())
	if err != nil {
		t.Fatalf("prepare legacy stopped row for prune: %v", err)
	}

	pruneWorker := reconcile.NewWorker(
		env.repoClient.DB,
		repo,
		lifecycleSvc,
		obslog.New("info"),
		true,
		false,
		100*time.Millisecond,
		50*time.Millisecond,
		20*time.Minute,
		time.Second,
		64001,
	)
	pruneCtx, pruneCancel := context.WithCancel(context.Background())
	defer pruneCancel()
	go pruneWorker.Run(pruneCtx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, getErr := repo.GetByExternalID(context.Background(), containerID)
		if getErr == nil && row.State == lifecycle.StatePruned {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	finalRow, _ := repo.GetByExternalID(context.Background(), containerID)
	if finalRow == nil {
		t.Fatal("expected final row to exist")
	}
	t.Fatalf("expected container to be pruned after expired backfilled deadline, got state=%s", finalRow.State)
}
