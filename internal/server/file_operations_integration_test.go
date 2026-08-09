//go:build integration

package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestFileRoundTripManifestAndDelete(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	textBody := []byte("hello from integration file test")
	uploadFile(t, env.httpServer.URL, containerID, "docs/readme.txt", textBody, http.StatusCreated)

	binaryBody := []byte{0x00, 0x01, 0x7f, 0x80, 0xff, 0x10}
	uploadFile(t, env.httpServer.URL, containerID, "data/blob.bin", binaryBody, http.StatusCreated)

	downloadedText := downloadFile(t, env.httpServer.URL, containerID, "docs/readme.txt", http.StatusOK)
	if !bytes.Equal(downloadedText, textBody) {
		t.Fatalf("text round trip mismatch: got %q want %q", string(downloadedText), string(textBody))
	}

	downloadedBinary := downloadFile(t, env.httpServer.URL, containerID, "data/blob.bin", http.StatusOK)
	if !bytes.Equal(downloadedBinary, binaryBody) {
		t.Fatalf("binary round trip mismatch: got %v want %v", downloadedBinary, binaryBody)
	}

	manifestReq, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID+"/files/manifest", nil)
	manifestReq.Header.Set("Authorization", "Bearer integration-key")
	manifestResp, err := http.DefaultClient.Do(manifestReq)
	if err != nil {
		t.Fatalf("manifest request failed: %v", err)
	}
	defer manifestResp.Body.Close()
	if manifestResp.StatusCode != http.StatusOK {
		t.Fatalf("expected manifest status 200, got %d", manifestResp.StatusCode)
	}

	var payload struct {
		Entries []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Hash *struct {
				Algorithm string `json:"algorithm"`
				Value     string `json:"value"`
			} `json:"hash,omitempty"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(manifestResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode manifest response: %v", err)
	}

	expectedTextHash := sha256.Sum256(textBody)
	expectedTextHashHex := hex.EncodeToString(expectedTextHash[:])
	foundTextHash := false
	for _, entry := range payload.Entries {
		if entry.Path == "docs/readme.txt" {
			foundTextHash = true
			if entry.Type != "file" {
				t.Fatalf("expected docs/readme.txt type file, got %s", entry.Type)
			}
			if entry.Hash == nil || entry.Hash.Algorithm != "sha256" || entry.Hash.Value != expectedTextHashHex {
				t.Fatalf("unexpected hash for docs/readme.txt: %+v", entry.Hash)
			}
		}
	}
	if !foundTextHash {
		t.Fatal("expected docs/readme.txt in manifest")
	}

	deleteReq, _ := http.NewRequest(http.MethodDelete, env.httpServer.URL+"/v1/containers/"+containerID+"/files/docs/readme.txt", nil)
	deleteReq.Header.Set("Authorization", "Bearer integration-key")
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	_ = deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected delete status 204, got %d", deleteResp.StatusCode)
	}

	downloadAfterDeleteReq, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID+"/files/docs/readme.txt", nil)
	downloadAfterDeleteReq.Header.Set("Authorization", "Bearer integration-key")
	downloadAfterDeleteResp, err := http.DefaultClient.Do(downloadAfterDeleteReq)
	if err != nil {
		t.Fatalf("download after delete request failed: %v", err)
	}
	_ = downloadAfterDeleteResp.Body.Close()
	if downloadAfterDeleteResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", downloadAfterDeleteResp.StatusCode)
	}
}

func TestFileTraversalAndSymlinkEscapeRejected(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	uploadTraversalReq, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/%2e%2e/etc/passwd", bytes.NewReader([]byte("bad")))
	uploadTraversalReq.Header.Set("Authorization", "Bearer integration-key")
	uploadTraversalResp, err := http.DefaultClient.Do(uploadTraversalReq)
	if err != nil {
		t.Fatalf("upload traversal request failed: %v", err)
	}
	_ = uploadTraversalResp.Body.Close()
	if uploadTraversalResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal path, got %d", uploadTraversalResp.StatusCode)
	}

	execResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","ln -sf /etc/passwd /workspace/outside-link"]}`)
	if execResp.StatusCode != http.StatusOK {
		t.Fatalf("expected exec status 200 to create symlink, got %d", execResp.StatusCode)
	}
	_ = execResp.Body.Close()

	downloadSymlinkReq, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID+"/files/outside-link", nil)
	downloadSymlinkReq.Header.Set("Authorization", "Bearer integration-key")
	downloadSymlinkResp, err := http.DefaultClient.Do(downloadSymlinkReq)
	if err != nil {
		t.Fatalf("download symlink request failed: %v", err)
	}
	_ = downloadSymlinkResp.Body.Close()
	if downloadSymlinkResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for symlink escape attempt, got %d", downloadSymlinkResp.StatusCode)
	}
}

func TestFileOperationsAllowedForStoppedButNotPrunedContainers(t *testing.T) {
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

	stoppedContent := []byte("editable while stopped")
	uploadFile(t, env.httpServer.URL, containerID, "stopped/notes.txt", stoppedContent, http.StatusCreated)
	downloaded := downloadFile(t, env.httpServer.URL, containerID, "stopped/notes.txt", http.StatusOK)
	if !bytes.Equal(downloaded, stoppedContent) {
		t.Fatalf("stopped-state round trip mismatch: got %q want %q", string(downloaded), string(stoppedContent))
	}

	pruneReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/prune", nil)
	pruneReq.Header.Set("Authorization", "Bearer integration-key")
	pruneResp, err := http.DefaultClient.Do(pruneReq)
	if err != nil {
		t.Fatalf("prune request failed: %v", err)
	}
	_ = pruneResp.Body.Close()
	if pruneResp.StatusCode != http.StatusOK {
		t.Fatalf("expected prune status 200, got %d", pruneResp.StatusCode)
	}

	uploadAfterPruneReq, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/stopped/notes.txt", bytes.NewReader([]byte("x")))
	uploadAfterPruneReq.Header.Set("Authorization", "Bearer integration-key")
	uploadAfterPruneResp, err := http.DefaultClient.Do(uploadAfterPruneReq)
	if err != nil {
		t.Fatalf("upload after prune request failed: %v", err)
	}
	_ = uploadAfterPruneResp.Body.Close()
	if uploadAfterPruneResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected conflict 409 after prune, got %d", uploadAfterPruneResp.StatusCode)
	}
}

func uploadFile(t *testing.T, baseURL, containerID, path string, content []byte, expectedStatus int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, baseURL+"/v1/containers/"+containerID+"/files/"+path, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("create upload request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer integration-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run upload request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		t.Fatalf("expected upload status %d, got %d", expectedStatus, resp.StatusCode)
	}
}

func downloadFile(t *testing.T, baseURL, containerID, path string, expectedStatus int) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/containers/"+containerID+"/files/"+path, nil)
	if err != nil {
		t.Fatalf("create download request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer integration-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run download request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		t.Fatalf("expected download status %d, got %d", expectedStatus, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read download response body: %v", err)
	}
	return body
}
