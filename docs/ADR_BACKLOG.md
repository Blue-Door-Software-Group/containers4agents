# containers4agents ADR Backlog (v1)

This document tracks architecture decisions that must be resolved before or during v1 implementation. Each item includes a recommended default for v1 and the rationale.

## ADR-001 Metadata store and schema strategy

### Decision

Use PostgreSQL as the metadata system of record for v1 container state and lifecycle metadata, with Ent ORM as the schema and query layer.

### Why

- Requires durable state for restart-safe lifecycle and reconciliation behavior.
- Handles concurrent state transitions and query patterns cleanly.
- Fits existing lab operations and observability workflows.
- Ent provides typed schema/query patterns and migration support, reducing control-plane state handling errors.
- Aligns with team familiarity from chat-app Ent usage (`ent/schema`, startup migration flow, repository wrapping).

### Notes

- Keep schema minimal: container identity, state, profile, image version, timestamps, usage summary, deletion retry metadata.
- Keep provider-specific fields out of public API models.
- Keep Ent schemas in `ent/schema` and generated artifacts out of manual edits.
- Guard automatic migrations with explicit environment flags and support non-destructive defaults.

## ADR-002 Container identity model

### Decision

Service-generated opaque container IDs in v1.

### Why

- Prevents coupling to caller-specific ID semantics.
- Simplifies validation and uniqueness guarantees.
- Aligns with provider abstraction and anti-leak requirements.

### Notes

- Consider optional caller-supplied idempotency keys for create requests instead of caller-supplied IDs.

## ADR-003 Idempotency contract

### Decision

Support idempotency keys for create, start/resume, stop, prune, and file upload operations.

### Why

- API clients and orchestration layers will retry frequently under transient failures.
- Reduces duplicate resource creation and lifecycle race conditions.

### Notes

- Use operation-scoped keys and bounded retention window.
- Store request hash with operation result metadata.

## ADR-004 Execution concurrency per container

### Decision

Default to single in-flight execution per container in v1; reject additional concurrent execs with conflict-class response.

### Why

- Simplifies resource accounting, timeout handling, and user outcome semantics.
- Reduces race conditions around shared workspace mutations.

### Notes

- Revisit in v2 if workloads require parallel command streams.

## ADR-005 Exec behavior for stopped containers

### Decision

Do not auto-resume on exec in v1. Return a structured state conflict and require explicit start/resume.

### Why

- Keeps lifecycle transitions explicit and observable.
- Avoids hidden side effects in execution API calls.

### Notes

- If future UX requires convenience behavior, add opt-in flag and explicit telemetry.

## ADR-006 `prod-strict` profile defaults

### Decision

Ship policy-file-backed defaults tuned for initial lab deployment profile:

- Capacity:
  - active exec limit: 10
  - running container limit: 30
  - total container limit: 100
- Per-container:
  - CPU: 2.0 cores hard cap
  - Memory: 3 GiB
  - Workspace disk quota: 10 GiB
  - Tmp disk quota: 2 GiB
  - PID limit: 256
  - Exec timeout: 120 seconds default, 300 seconds max
  - Stdout/stderr caps: 4 MiB per stream
  - Upload/download caps: 64 MiB
  - File count cap: 100000
  - Max concurrent exec per container: 1

### Why

- Balances safety and utility for shell/python tool use cases.
- Limits blast radius under abusive or runaway jobs.

### Notes

- Policy is loaded from `C4A_RESOURCE_POLICY_FILE`; unset/unreadable falls back to compiled defaults.
- Readable-but-invalid policy files fail startup.
- Final limits should be validated with representative agent workloads before GA.

## ADR-007 Docker provider access model in Portainer

### Decision

Use a dedicated Docker endpoint with least-privilege controls where possible; use socket mount only in tightly controlled lab environments.

### Why

- Docker daemon access is highly privileged and expands blast radius.
- Aligns with principle of explicit trust boundaries.

### Notes

- If socket mount is used in lab, isolate control-plane service on internal network and restrict external exposure to Caddy policy.

## ADR-008 Lifecycle timers and reconciliation ownership

### Decision

Persist lifecycle deadlines in metadata store and run reconciliation as first-class service behavior at startup and on interval.

### Why

- Requirements explicitly disallow in-memory-only lifecycle behavior.
- Protects against drift after crashes/restarts.

### Notes

- Keep reconciliation idempotent and instrumented (drift corrected, retries, failures).
