# Image Build and Publish

## Images

- Control plane: `ghcr.io/blue-door-software-group/containers4agents`
- Runtime sandbox: `ghcr.io/blue-door-software-group/containers4agents-runtime`
- Optional runtime sandbox with diagram rendering/conversion:
  `ghcr.io/blue-door-software-group/containers4agents-runtime-diagrams`
  (`images/runtime-diagrams/`, `FROM` the runtime image, adds Chromium and the draw.io
  app; see `docs/DIAGRAM_TOOLS.md`). A deployment uses it by setting
  `C4A_DOCKER_IMAGE` to it in place of the runtime image.

## Tags

- Control plane: `latest`, `sha-*`
- Runtime sandbox: `latest`, `sha-*`, `v1`
- Runtime-diagrams sandbox: `latest`, `sha-*`, `v1`

## Workflows

- `publish-control-plane-ghcr.yml`
- `publish-runtime-ghcr.yml`
- `publish-runtime-diagrams-ghcr.yml` (builds the runtime image, then the diagrams
  image `FROM` it in the same job, smoke-tests it, and pushes only the diagrams image)
- `pr-validation.yml` (also smoke-tests `drawio-tool` in the runtime image)
- `diagrams-image-validation.yml` (builds and smoke-tests the diagrams image when
  `images/runtime/**` or `images/runtime-diagrams/**` change)

All publish workflows use:

- `contents: read`
- `packages: write`
- `GITHUB_TOKEN` for GHCR login

## Deployment Follow-up

This phase only adds image build/publish and CI validation. Lab deployment wiring (compose stack updates, scrape integration, and rollout automation) is handled in the next deployment-focused pass.
