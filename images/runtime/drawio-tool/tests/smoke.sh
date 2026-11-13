#!/usr/bin/env bash
# Smoke test for drawio-tool, run INSIDE a built image (see docker-smoke.sh).
# Usage: smoke.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
cd "$work"

check() {  # check NAME JQ-FILTER COMMAND... : run, print, assert the JSON
  local name=$1 filter=$2; shift 2
  local out
  out=$("$@") || true
  echo "$name: $out"
  jq -e "$filter" <<<"$out" >/dev/null || { echo "FAILED: $name ($filter)"; exit 1; }
}

python3 -m unittest discover -s "$here" 2>&1 | tail -3

check capabilities '.ok and .commands.layout' drawio-tool capabilities
check inspect '.pages | length == 2' drawio-tool inspect "$here/fixtures/sample.drawio"
check validate '.valid' drawio-tool validate "$here/fixtures/sample.drawio"
check deflate '.changed == 2' drawio-tool deflate "$here/fixtures/sample.drawio" packed.drawio
check inflate '.changed == 2' drawio-tool inflate packed.drawio sample.drawio
check layout '.vertices == 5 and .containers == 1' drawio-tool layout sample.drawio laid-out.drawio --direction LR
check layout-valid '.valid' drawio-tool validate laid-out.drawio

check capabilities-browser '.commands.render and .commands["from-mermaid"] and .commands["from-vsdx"]' \
  drawio-tool capabilities
check from-mermaid '.ok and .vertices >= 4' drawio-tool from-mermaid "$here/fixtures/flow.mmd" flow.drawio
check mermaid-native '[.pages[0].elements[] | select(.kind == "vertex")] | length >= 4' drawio-tool inspect flow.drawio
grep -q '<mxGraphModel' flow.drawio || { echo "FAILED: flow.drawio is not uncompressed XML"; exit 1; }
check render-png '.format == "png" and .bytes > 1000' drawio-tool render laid-out.drawio out.png --scale 2
check render-svg '.format == "svg"' drawio-tool render flow.drawio out.svg
check render-pdf '.format == "pdf"' drawio-tool render packed.drawio out.pdf --page Notes
[[ $(head -c 8 out.png | od -An -tx1 | tr -d ' \n') == 89504e470d0a1a0a ]] || { echo "FAILED: not a PNG"; exit 1; }
grep -q '<svg' out.svg || { echo "FAILED: not an SVG"; exit 1; }
[[ $(head -c 5 out.pdf) == %PDF- ]] || { echo "FAILED: not a PDF"; exit 1; }
python3 "$here/fixtures/make_vsdx.py" tiny.vsdx
check from-vsdx '.ok and .vertices == 2' drawio-tool from-vsdx tiny.vsdx tiny.drawio
check vsdx-content '[.pages[0].elements[] | .label] | index("Alpha") != null' drawio-tool inspect tiny.drawio
check vsdx-edge '[.pages[0].elements[] | select(.kind == "edge")] | length == 1' drawio-tool inspect tiny.drawio
check bad-mermaid '.ok | not' drawio-tool from-mermaid "$here/fixtures/sample.drawio" bad.drawio
echo "smoke test passed"
