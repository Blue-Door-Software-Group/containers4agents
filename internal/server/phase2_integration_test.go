//go:build integration

package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPhase2StartAndPruneLifecycle(t *testing.T) {
	env := setupPhase1IntegrationEnv(t)
	defer env.close()

	createReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	createReq.Header.Set("Authorization", "Bearer integration-key")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d", createResp.StatusCode)
	}

	var created map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	containerID, _ := created["id"].(string)
	if containerID == "" {
		t.Fatal("expected created container id")
	}

	stopReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/stop", nil)
	stopReq.Header.Set("Authorization", "Bearer integration-key")
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatalf("stop request failed: %v", err)
	}
	defer stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d", stopResp.StatusCode)
	}

	startReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/start", nil)
	startReq.Header.Set("Authorization", "Bearer integration-key")
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatalf("start request failed: %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("expected start status 200, got %d", startResp.StatusCode)
	}

	statusReq, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID, nil)
	statusReq.Header.Set("Authorization", "Bearer integration-key")
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatalf("status request failed: %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", statusResp.StatusCode)
	}

	var statusPayload map[string]any
	if err := json.NewDecoder(statusResp.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if statusPayload["state"] != "running" {
		t.Fatalf("expected running state after start, got %v", statusPayload["state"])
	}

	pruneReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/prune", nil)
	pruneReq.Header.Set("Authorization", "Bearer integration-key")
	pruneResp, err := http.DefaultClient.Do(pruneReq)
	if err != nil {
		t.Fatalf("prune request failed: %v", err)
	}
	defer pruneResp.Body.Close()
	if pruneResp.StatusCode != http.StatusOK {
		t.Fatalf("expected prune status 200, got %d", pruneResp.StatusCode)
	}

	statusReq2, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID, nil)
	statusReq2.Header.Set("Authorization", "Bearer integration-key")
	statusResp2, err := http.DefaultClient.Do(statusReq2)
	if err != nil {
		t.Fatalf("status request after prune failed: %v", err)
	}
	defer statusResp2.Body.Close()
	if statusResp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 after prune, got %d", statusResp2.StatusCode)
	}
	var statusPayload2 map[string]any
	if err := json.NewDecoder(statusResp2.Body).Decode(&statusPayload2); err != nil {
		t.Fatalf("decode status after prune: %v", err)
	}
	if statusPayload2["state"] != "pruned" {
		t.Fatalf("expected pruned state, got %v", statusPayload2["state"])
	}
}

func TestPhase2PrunedContainerStateConflict(t *testing.T) {
	env := setupPhase1IntegrationEnv(t)
	defer env.close()

	createReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	createReq.Header.Set("Authorization", "Bearer integration-key")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	defer createResp.Body.Close()

	var created map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	containerID, _ := created["id"].(string)
	if containerID == "" {
		t.Fatal("expected created container id")
	}

	pruneReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/prune", nil)
	pruneReq.Header.Set("Authorization", "Bearer integration-key")
	pruneResp, err := http.DefaultClient.Do(pruneReq)
	if err != nil {
		t.Fatalf("prune request failed: %v", err)
	}
	_ = pruneResp.Body.Close()

	startReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/start", nil)
	startReq.Header.Set("Authorization", "Bearer integration-key")
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatalf("start request failed: %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected state conflict 409 when starting pruned container, got %d", startResp.StatusCode)
	}
}
