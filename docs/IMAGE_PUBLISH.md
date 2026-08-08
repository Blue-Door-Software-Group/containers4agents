# Image Build and Publish

## Images

- Control plane: `ghcr.io/blue-door-software-group/containers4agents`
- Runtime sandbox: `ghcr.io/blue-door-software-group/containers4agents-runtime`

## Tags

- Control plane: `latest`, `sha-*`
- Runtime sandbox: `latest`, `sha-*`, `v1`

## Workflows

- `publish-control-plane-ghcr.yml`
- `publish-runtime-ghcr.yml`
- `pr-validation.yml`

All publish workflows use:

- `contents: read`
- `packages: write`
- `GITHUB_TOKEN` for GHCR login

## Deployment Follow-up

This phase only adds image build/publish and CI validation. Lab deployment wiring (compose stack updates, scrape integration, and rollout automation) is handled in the next deployment-focused pass.
