//go:build integration

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
)

func TestIsolationRunsExecAsNonRootAndBlocksPrivilegedMount(t *testing.T) {
	env := setupIntegrationEnv(t, func(cfg *config.Config) {
		cfg.ContainerDropAllCapabilities = true
		cfg.ContainerNoNewPrivileges = true
		cfg.ContainerExecUser = "65534:65534"
	})
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	uidResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","id -u"]}`)
	if uidResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for id -u, got %d", uidResp.StatusCode)
	}
	var uidPayload map[string]any
	if err := json.NewDecoder(uidResp.Body).Decode(&uidPayload); err != nil {
		t.Fatalf("decode uid payload: %v", err)
	}
	_ = uidResp.Body.Close()
	if uidPayload["stdout"] == "0\n" {
		t.Fatalf("expected non-root exec user, got stdout=%q", uidPayload["stdout"])
	}

	sandboxResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","mkdir -p /tmp/mnt && mount -t tmpfs none /tmp/mnt"]}`)
	if sandboxResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for mount probe, got %d", sandboxResp.StatusCode)
	}
	var sandboxPayload map[string]any
	if err := json.NewDecoder(sandboxResp.Body).Decode(&sandboxPayload); err != nil {
		t.Fatalf("decode sandbox payload: %v", err)
	}
	_ = sandboxResp.Body.Close()
	status, _ := sandboxPayload["status"].(string)
	if status != "sandbox_violation" && status != "user_error" {
		t.Fatalf("expected sandbox_violation or user_error, got %q", status)
	}
}

func TestLimitsRejectWorkspaceQuotaOverflowUpload(t *testing.T) {
	env := setupIntegrationEnv(t, func(cfg *config.Config) {
		cfg.WorkspaceDiskQuotaBytes = 1024
		cfg.FileMaxUploadBytes = 8192
		cfg.ContainerFileCountMax = 1000
	})
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	body := bytes.Repeat([]byte("a"), 2048)
	uploadReq, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/too-big.txt", bytes.NewReader(body))
	uploadReq.Header.Set("Authorization", "Bearer integration-key")
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("upload request failed: %v", err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for quota overflow, got %d", uploadResp.StatusCode)
	}
}
