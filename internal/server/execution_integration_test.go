//go:build integration

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExecutionCommandMatrix(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	okResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","echo hello"]}`)
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for ok command, got %d", okResp.StatusCode)
	}
	var okPayload map[string]any
	if err := json.NewDecoder(okResp.Body).Decode(&okPayload); err != nil {
		t.Fatalf("decode ok payload: %v", err)
	}
	_ = okResp.Body.Close()
	if okPayload["status"] != "ok" {
		t.Fatalf("expected ok status, got %v", okPayload["status"])
	}

	userErrResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","exit 9"]}`)
	if userErrResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for user error command, got %d", userErrResp.StatusCode)
	}
	var userErrPayload map[string]any
	if err := json.NewDecoder(userErrResp.Body).Decode(&userErrPayload); err != nil {
		t.Fatalf("decode user_error payload: %v", err)
	}
	_ = userErrResp.Body.Close()
	if userErrPayload["status"] != "user_error" {
		t.Fatalf("expected user_error status, got %v", userErrPayload["status"])
	}
}

func TestExecutionTimeoutAndTruncation(t *testing.T) {
	env := setupIntegrationEnv(t, nil)
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	timeoutResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","sleep 10"]}`)
	if timeoutResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for timeout command, got %d", timeoutResp.StatusCode)
	}
	var timeoutPayload map[string]any
	if err := json.NewDecoder(timeoutResp.Body).Decode(&timeoutPayload); err != nil {
		t.Fatalf("decode timeout payload: %v", err)
	}
	_ = timeoutResp.Body.Close()
	if timeoutPayload["status"] != "timeout" {
		t.Fatalf("expected timeout status, got %v", timeoutPayload["status"])
	}

	truncResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","yes out | head -c 9000"]}`)
	if truncResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for truncation command, got %d", truncResp.StatusCode)
	}
	var truncPayload map[string]any
	if err := json.NewDecoder(truncResp.Body).Decode(&truncPayload); err != nil {
		t.Fatalf("decode truncation payload: %v", err)
	}
	_ = truncResp.Body.Close()
	if truncPayload["truncated_stdout"] != true {
		t.Fatalf("expected truncated_stdout true, got %v", truncPayload["truncated_stdout"])
	}
}

func TestExecutionStoppedContainerAndConcurrentConflict(t *testing.T) {
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

	conflictResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["echo","should-fail"]}`)
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected conflict status 409 on stopped container, got %d", conflictResp.StatusCode)
	}
	_ = conflictResp.Body.Close()

	startReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/start", nil)
	startReq.Header.Set("Authorization", "Bearer integration-key")
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatalf("start request failed: %v", err)
	}
	_ = startResp.Body.Close()

	firstReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/exec", strings.NewReader(`{"command":["sh","-lc","sleep 1"]}`))
	firstReq.Header.Set("Authorization", "Bearer integration-key")
	firstReq.Header.Set("Content-Type", "application/json")

	firstDone := make(chan error, 1)
	go func() {
		resp, reqErr := http.DefaultClient.Do(firstReq)
		if reqErr != nil {
			firstDone <- reqErr
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			firstDone <- &unexpectedStatusError{got: resp.StatusCode, want: http.StatusOK}
			return
		}
		firstDone <- nil
	}()

	conflictObserved := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		secondResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["echo","second"]}`)
		if secondResp.StatusCode == http.StatusConflict {
			conflictObserved = true
			_ = secondResp.Body.Close()
			break
		}
		_ = secondResp.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}

	if !conflictObserved {
		t.Fatal("expected to observe at least one 409 response while first exec was in flight")
	}

	if err := <-firstDone; err != nil {
		t.Fatalf("first exec request failed: %v", err)
	}
}

type unexpectedStatusError struct {
	got  int
	want int
}

func (e *unexpectedStatusError) Error() string {
	return "unexpected status code"
}
