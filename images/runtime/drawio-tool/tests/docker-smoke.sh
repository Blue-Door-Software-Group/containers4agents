#!/usr/bin/env bash
# Run smoke.sh in IMAGE under the restrictions the control plane gives sandboxes:
# no network, uid 65534, no capabilities, no-new-privileges, noexec /tmp.
# Usage: docker-smoke.sh IMAGE
set -euo pipefail
image=$1; shift
docker run --rm --network none --user 65534:65534 --cap-drop ALL \
  --security-opt no-new-privileges --memory 3g --pids-limit 256 \
  --tmpfs /tmp:rw,noexec,nosuid,size=1g \
  "$image" bash /usr/local/lib/drawio-tool/tests/smoke.sh "$@"
