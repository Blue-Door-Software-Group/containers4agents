package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/idempotency"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
)

// optsContainerService records the create options and answers per test.
type optsContainerService struct {
	loggingContainerService
	got    *lifecycle.CreateOptions
	answer func(lifecycle.CreateOptions) (*lifecycle.ContainerView, error)
}

func (s optsContainerService) CreateContainer(_ context.Context, opts lifecycle.CreateOptions) (*lifecycle.ContainerView, error) {
	*s.got = opts
	return s.answer(opts)
}

func view(id string, existed bool) *lifecycle.ContainerView {
	now := time.Now()
	return &lifecycle.ContainerView{ExternalID: id, State: "running", Profile: "dev-egress", ImageVersion: "v1",
		ImageName: "swarm-dev", Network: "egress", CreatedAt: now, UpdatedAt: now, Existed: existed}
}

func postCreate(t *testing.T, h *ContainerHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/containers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	return rec
}

func TestCreate_Body(t *testing.T) {
	var got lifecycle.CreateOptions
	svc := optsContainerService{got: &got, answer: func(o lifecycle.CreateOptions) (*lifecycle.ContainerView, error) {
		return view(o.ExternalID, o.ReturnExisting), nil
	}}
	h := NewContainerHandler(svc)

	// No body: today's create.
	rec := postCreate(t, h, "")
	if rec.Code != http.StatusCreated || got != (lifecycle.CreateOptions{}) {
		t.Fatalf("empty body: %d %+v", rec.Code, got)
	}

	rec = postCreate(t, h, `{"image":"swarm-dev","external_id":"swarm-u-p","if_exists":"return"}`)
	if rec.Code != http.StatusOK || got != (lifecycle.CreateOptions{Image: "swarm-dev", ExternalID: "swarm-u-p", ReturnExisting: true}) {
		t.Fatalf("existing: %d %+v", rec.Code, got)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["id"] != "swarm-u-p" || resp["image"] != "swarm-dev" || resp["network"] != "egress" {
		t.Fatalf("response %v", resp)
	}

	for _, bad := range []string{`{"external_id":"has space"}`, `{"if_exists":"replace"}`, `{"network":"egress"}`, `not json`} {
		if rec := postCreate(t, h, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", bad, rec.Code)
		}
	}
}

func TestCreate_ExistsAndUnknownImage(t *testing.T) {
	var got lifecycle.CreateOptions
	h := NewContainerHandler(optsContainerService{got: &got, answer: func(o lifecycle.CreateOptions) (*lifecycle.ContainerView, error) {
		if o.Image == "nope" {
			return nil, lifecycle.ErrUnknownImage
		}
		return nil, &lifecycle.ExistsError{View: view(o.ExternalID, false)}
	}})
	rec := postCreate(t, h, `{"external_id":"swarm-u-p"}`)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusConflict || resp["code"] != "container_exists" || resp["container"].(map[string]any)["id"] != "swarm-u-p" {
		t.Fatalf("exists: %d %v", rec.Code, resp)
	}
	rec = postCreate(t, h, `{"image":"nope"}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusBadRequest || resp["code"] != "unknown_image" {
		t.Fatalf("unknown image: %d %v", rec.Code, resp)
	}
}

// The idempotency hash of a create without a body is what it was before bodies existed, so stored
// keys replay as they did; a body is part of the hash.
func TestCreate_IdempotencyHashKeepsTheEmptyBodyHash(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/containers", strings.NewReader(""))
	_, parts, msg := parseCreateRequest(req)
	if msg != "" || len(parts) != 0 {
		t.Fatalf("empty body parts %v %q", parts, msg)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/containers", strings.NewReader(`{}`))
	if _, parts, _ = parseCreateRequest(req); len(parts) != 0 {
		t.Fatalf("{} body parts %v", parts)
	}
	a := httptest.NewRequest(http.MethodPost, "/v1/containers", strings.NewReader(`{"external_id":"x","image":"swarm-dev"}`))
	b := httptest.NewRequest(http.MethodPost, "/v1/containers", strings.NewReader(`{ "image":"swarm-dev", "external_id":"x" }`))
	_, pa, _ := parseCreateRequest(a)
	_, pb, _ := parseCreateRequest(b)
	if len(pa) != 1 || pa[0] != pb[0] {
		t.Fatalf("canonical bodies differ: %v %v", pa, pb)
	}
	if idempotency.Hash("POST", "/v1/containers", pa[0]) == idempotency.Hash("POST", "/v1/containers") {
		t.Fatal("a body must change the hash")
	}
}

func TestValidateExecEnv(t *testing.T) {
	if msg := validateExecEnv(map[string]string{"GIT_TOKEN": "x", "_X1": ""}); msg != "" {
		t.Fatal(msg)
	}
	for _, env := range []map[string]string{{"lower": "x"}, {"A-B": "x"}, {"A": "x\x00y"}, {"A": strings.Repeat("x", 70<<10)}} {
		if validateExecEnv(env) == "" {
			t.Fatalf("want refusal for %v", env)
		}
	}
	many := map[string]string{}
	for i := 0; i < 65; i++ {
		many["K"+strings.Repeat("A", i)] = "v"
	}
	if validateExecEnv(many) == "" {
		t.Fatal("want refusal for too many entries")
	}
}
