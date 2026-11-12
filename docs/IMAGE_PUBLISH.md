# Image Build and Publish

## Images

- Control plane: `ghcr.io/blue-door-software-group/containers4agents`
- Runtime sandbox: `ghcr.io/blue-door-software-group/containers4agents-runtime`
- Optional runtime sandbox with diagram rendering/conversion (`images/runtime-diagrams/`,
  `FROM` the runtime image, adds Chromium and the draw.io app): not published yet;
  build it locally (see `docs/DIAGRAM_TOOLS.md`). A publish workflow has to build or
  wait for the matching runtime image first, so it is not a copy of the runtime one.

## Tags

- Control plane: `latest`, `sha-*`
- Runtime sandbox: `latest`, `sha-*`, `v1`

## Workflows

- `publish-control-plane-ghcr.yml`
- `publish-runtime-ghcr.yml`
- `pr-validation.yml` (also smoke-tests `drawio-tool` in the runtime image)
- `diagrams-image-validation.yml` (builds and smoke-tests the diagrams image when
  `images/runtime/**` or `images/runtime-diagrams/**` change)

All publish workflows use:

- `contents: read`
- `packages: write`
- `GITHUB_TOKEN` for GHCR login

## Deployment Follow-up

This phase only adds image build/publish and CI validation. Lab deployment wiring (compose stack updates, scrape integration, and rollout automation) is handled in the next deployment-focused pass.
