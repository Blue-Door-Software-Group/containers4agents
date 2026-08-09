# containers4agents

`containers4agents` (`c4a`) is a provider-agnostic control-plane service for ephemeral agent sandboxes.

It gives API clients a stable interface to:

- create isolated containers,
- execute bounded shell/Python commands,
- upload/download/manage workspace files,
- stop/resume/prune containers with explicit lifecycle semantics.

The current v1 implementation uses a hardened Docker provider behind an internal provider interface so runtime backends can be replaced later without changing the public API contract.

## Current Status

- authenticated v1 HTTP API,
- PostgreSQL + Ent-backed metadata state,
- Docker provider with lifecycle, exec, and file operations,
- idle-stop and retention-prune lifecycle timers,
- reconciliation worker with distributed leader election (Postgres advisory lock),
- idempotency support for mutating operations,
- structured logs + Prometheus metrics,
- distroless control-plane image + runtime image publish workflows.

## API Surface (v1)

All API routes below require:

`Authorization: Bearer <CONTAINERS4AGENTS_API_KEY>`

Core routes:

- `POST /v1/containers`
- `GET /v1/containers/{id}`
- `POST /v1/containers/{id}/stop`
- `POST /v1/containers/{id}/start`
- `POST /v1/containers/{id}/prune`
- `POST /v1/containers/{id}/exec`
- `GET /v1/containers/{id}/files/manifest`
- `PUT /v1/containers/{id}/files/{path...}`
- `GET /v1/containers/{id}/files/{path...}`
- `DELETE /v1/containers/{id}/files/{path...}`

Unauthenticated operational routes:

- `GET /healthz`
- `GET /metrics`

Important file API detail:

- file paths are route segments, not query params;
- paths are workspace-relative (`hello.txt`, `dir/file.txt`);
- absolute paths like `/workspace/hello.txt` are rejected.

## Quick API Walkthrough

```bash
export C4A_BASE_URL="https://c4a.bdsg.io"
export C4A_API_KEY="c4a_your_key"
export C4A_AUTH_HEADER="Authorization: Bearer ${C4A_API_KEY}"
```

Create container:

```bash
CREATE_RESP="$(curl -sS -X POST "${C4A_BASE_URL}/v1/containers" \
  -H "${C4A_AUTH_HEADER}" \
  -H "Content-Type: application/json")"
echo "$CREATE_RESP" | jq
export C4A_CONTAINER_ID="$(printf '%s' "$CREATE_RESP" | jq -r '.id')"
```

Get status:

```bash
curl -sS "${C4A_BASE_URL}/v1/containers/${C4A_CONTAINER_ID}" \
  -H "${C4A_AUTH_HEADER}" | jq
```

Execute Python to create a file:

```bash
curl -sS -X POST "${C4A_BASE_URL}/v1/containers/${C4A_CONTAINER_ID}/exec" \
  -H "${C4A_AUTH_HEADER}" \
  -H "Content-Type: application/json" \
  -d '{
    "command": [
      "python",
      "-c",
      "from pathlib import Path; Path(\"/workspace/hello.txt\").write_text(\"hello from c4a\\n\", encoding=\"utf-8\"); print(\"wrote /workspace/hello.txt\")"
    ]
  }' | jq
```

List manifest:

```bash
curl -sS "${C4A_BASE_URL}/v1/containers/${C4A_CONTAINER_ID}/files/manifest" \
  -H "${C4A_AUTH_HEADER}" | jq
```

Download file:

```bash
curl -sS "${C4A_BASE_URL}/v1/containers/${C4A_CONTAINER_ID}/files/hello.txt" \
  -H "${C4A_AUTH_HEADER}" \
  -o hello.txt
cat hello.txt
```

## Configuration

Primary config is environment-driven via `internal/config/config.go`.

Common required settings:

- `CONTAINERS4AGENTS_API_KEY` (required when `DEV_MODE=false`)
- `C4A_DB_DSN` (Postgres DSN)
- `C4A_DB_DRIVER` (must be `postgres`)
- `C4A_DOCKER_IMAGE` (runtime image)

Common operational settings:

- `DEV_MODE`
- `AUTO_MIGRATE`
- `C4A_HTTP_LISTEN_ADDR`
- `C4A_LOG_LEVEL`
- `C4A_DOCKER_HOST`
- `C4A_DOCKER_IMAGE_PULL_POLICY` (`always` | `if-not-present` | `never`)
- `C4A_DOCKER_REGISTRY_SERVER`
- `C4A_DOCKER_REGISTRY_USERNAME`
- `C4A_DOCKER_REGISTRY_PASSWORD`
- `C4A_STARTUP_TIMEOUT`
- `C4A_SHUTDOWN_TIMEOUT`

Lifecycle and reconciliation settings:

- `C4A_RECONCILE_ENABLED`
- `C4A_RECONCILE_DISTRIBUTED`
- `C4A_RECONCILE_INTERVAL`
- `C4A_RECONCILE_LOCK_KEY`
- `C4A_RECONCILE_FOLLOWER_RETRY`
- `C4A_IDLE_STOP_TIMEOUT`
- `C4A_STOPPED_RETENTION_TIMEOUT`
- `C4A_IDEMPOTENCY_TTL`

Execution and file limits:

- `C4A_EXEC_TIMEOUT`
- `C4A_EXEC_MAX_STDOUT_BYTES`
- `C4A_EXEC_MAX_STDERR_BYTES`
- `C4A_FILE_MAX_UPLOAD_BYTES`
- `C4A_FILE_MAX_DOWNLOAD_BYTES`
- `C4A_WORKSPACE_DISK_QUOTA_BYTES`

### Resource Policy

Complex sandbox/capacity defaults are sourced from policy defaults with optional file override:

- `C4A_RESOURCE_POLICY_FILE` can point to a policy file.
- if unset, defaults are used.
- if set but invalid, startup fails fast.

## Local Development

Run service directly:

```bash
go run ./cmd/containers4agents
```

Build binary:

```bash
go build ./cmd/containers4agents
```

Run unit tests:

```bash
go test ./...
```

Run integration tests:

```bash
go test -tags integration ./internal/server
```

Integration tests require:

- Docker available,
- Postgres DSN via `C4A_TEST_DB_DSN`.

## Deployment

Control-plane image:

- `ghcr.io/blue-door-software-group/containers4agents`

Runtime image:

- `ghcr.io/blue-door-software-group/containers4agents-runtime`

Tagging and publish workflow details:

- `docs/IMAGE_PUBLISH.md`
- `.github/workflows/publish-control-plane-ghcr.yml`
- `.github/workflows/publish-runtime-ghcr.yml`

## Security and Logging Notes

- API is key-authenticated; use TLS at the edge.
- Agent sandboxes are network-isolated by default profile.
- Commands execute as unprivileged user under sandbox restrictions.
- Production logs are metadata-focused and avoid command/file content.
- `DEV_MODE=true` enables additional diagnostics and should not be used in production.

## Operational Endpoints

- `GET /healthz` for liveness
- `GET /metrics` for Prometheus scraping

## Non-Goals (v1)

- GPU workloads,
- runtime package installation from internet,
- per-tenant quota modeling,
- durable workspace backup/restore,
- model-provider-specific orchestration semantics.
