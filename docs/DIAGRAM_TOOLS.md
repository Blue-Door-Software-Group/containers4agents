# Diagram tools (`drawio-tool`)

`/usr/local/bin/drawio-tool` is a plain CLI baked into the sandbox images for
working with draw.io files (`.drawio`, the `mxfile` XML). Callers run it
through the container exec API; there is no server and nothing runs between
calls. Source: `images/runtime/drawio-tool/`.

| Image | Commands |
| --- | --- |
| `containers4agents-runtime` (default) | `capabilities`, `inspect`, `validate`, `inflate`, `deflate`, `layout` |
| `containers4agents-runtime-diagrams` (optional) | the above plus `render`, `from-mermaid`, `from-vsdx` |

Call `capabilities` first and offer only the commands it reports as available.

## Contract

Every command prints **exactly one JSON object** on stdout and nothing else:
`{"ok": true, ...}` on success, `{"ok": false, "error": "<message>"}` on
failure. Exit codes:

| Code | Meaning |
| --- | --- |
| 0 | success (`validate` also exits 0 when it finds problems) |
| 1 | the command failed (unreadable file, invalid XML, Graphviz/draw.io error, timeout, ...) |
| 2 | usage error (unknown command, missing or bad arguments) |
| 3 | the command is not available in this image |

`IN` and `OUT` are file paths; `OUT` may equal `IN`. Output files are written
atomically and, unless stated otherwise, as **uncompressed** XML.
Pages are selected with `--page ID|NAME|INDEX`: page id first, then name,
then 0-based index; the default is the first page.

### `capabilities`

```json
{"ok": true, "version": "1",
 "commands": {"capabilities": true, "inspect": true, "validate": true, "inflate": true,
              "deflate": true, "layout": true, "render": false, "from-mermaid": false, "from-vsdx": false},
 "unavailable": {"render": "Playwright is not installed (use the runtime-diagrams image)", "...": "..."},
 "render_formats": []}
```

`render_formats` is `["png", "svg", "pdf"]` when `render` is available.

### `inspect IN`

A JSON outline of every page.

```json
{"ok": true, "pages": [{
  "index": 0, "id": "p1", "name": "Flow", "compressed": false,
  "layers": [{"id": "1", "name": ""}],
  "elements": [
    {"id": "lane", "kind": "container", "label": "Backend", "parent": "1", "shape": "swimlane",
     "geometry": {"x": 340, "y": 28, "width": 300, "height": 150}},
    {"id": "api", "kind": "vertex", "label": "API", "parent": "lane",
     "geometry": {"x": 16, "y": 74, "width": 120, "height": 60}},
    {"id": "e1", "kind": "edge", "label": "", "parent": "1", "source": "start", "target": "check",
     "points": [[300, 300]]},
    {"id": "e1label", "kind": "vertex", "label": "go", "parent": "e1", "shape": "edgeLabel",
     "geometry": {"x": -0.2, "y": 0, "width": 0, "height": 0, "relative": true}}
  ]}]}
```

- `kind`: `vertex`, `container` (styled `container=1`/`swimlane`/`group`, or has
  child vertices), `edge`, or `cell` (neither). The root cell and layers are not
  listed as elements; layers are in `layers`.
- `label`: plain text; HTML labels (`html=1`) are stripped, `<br>`/blocks become `\n`.
  Labels of `<object>`/`<UserObject>` wrappers are used.
- `shape`: the `shape=` style value, or the first bare style token (`ellipse`,
  `swimlane`, ...); inline stencils report `stencil`. Omitted if none.
- `geometry` is relative to the parent (container children are relative to the
  container). `relative: true` marks edge labels and other relatively placed cells.
- Edges have `source`/`target` (may be `null`) and, if set, waypoints in `points`.

### `validate IN`

```json
{"ok": true, "valid": false, "problems": [
  {"page": 0, "id": "a", "code": "duplicate-id", "message": "id 'a' is used more than once"}]}
```

Problem codes: `parse` (not XML or not a draw.io file; `page` is `null`),
`empty-page`, `missing-id`, `duplicate-id`, `missing-root` (no cell `0` without a
parent), `missing-layer` (no cell whose parent is the root, e.g. `1`),
`missing-parent`, `parent-cycle`, `dangling-source`, `dangling-target`,
`malformed-style` (a `key=value;` token with an empty or invalid key),
`vertex-and-edge`. A missing input file is an error (`ok: false`, exit 1).

### `inflate IN OUT` / `deflate IN OUT`

Convert every page between plain XML and draw.io's compressed page form
(`base64(raw-deflate(encodeURIComponent(xml)))` as the `<diagram>` text).
`deflate` wraps a bare `<mxGraphModel>` file in an `<mxfile>`.

```json
{"ok": true, "pages": 2, "changed": 2}
```

### `layout IN OUT [--direction TB|LR|BT|RL] [--page P]`

Lays out one page with Graphviz `dot` (default direction `TB`). Containers
(swimlanes, `container=1`, groups) become clusters and are resized around their
children, leaving room for a swimlane's header (`startSize`, top or left).
Writes only `mxGeometry` `x`/`y` (plus `width`/`height` of containers and of
vertices that had no size) and removes the waypoints (`<Array as="points">`)
of edges between laid-out cells. Every id, label, style and unknown
element/attribute is kept. Left alone: edge labels and other relative cells,
and the children of cells that manage their own layout (`childLayout=...`,
tables) or are not containers. Edges whose ends are missing are counted in
`skipped_edges`. The laid-out page is stored uncompressed; other pages are
untouched.

```json
{"ok": true, "vertices": 5, "containers": 1, "edges": 5, "skipped_edges": 0,
 "cleared_waypoints": 1, "page": {"index": 0, "id": "p1", "name": "Flow", "compressed": false}}
```

### `render IN OUT [--format png|svg|pdf] [--page P] [--scale S] [--border PX] [--background COLOR]` (diagrams image)

Renders one page with the draw.io app. The format defaults to `OUT`'s
extension. `--scale` defaults to 1, `--border` to 10 px. The background is
transparent by default (white for PDF). PDF is a single page sized to the
drawing, printed from draw.io's SVG with Chromium's PDF printer (vector text).

```json
{"ok": true, "format": "png", "bytes": 28254, "page": {"index": 0, "id": "p1", "name": "Flow", "compressed": false}}
```

### `from-mermaid IN.mmd OUT.drawio` (diagrams image)

Converts Mermaid source into **native draw.io shapes** (draw.io's own Mermaid
converter, not an embedded image). Fails if the diagram type can only be
converted to an image or the source does not parse (`"error": "draw.io: ..."`).

```json
{"ok": true, "pages": 1, "vertices": 4}
```

### `from-vsdx IN.vsdx OUT.drawio` (diagrams image)

Converts a Visio file with draw.io's in-browser Visio importer (`.vsdx`; also
`.vssx` stencils and legacy binary `.vsd` by file extension). Same output shape
as `from-mermaid`.

`render`, `from-mermaid` and `from-vsdx` take `--timeout SECONDS` (default 90)
and need about 2 s each, most of it starting Chromium.

## Images

**Default runtime image** (`images/runtime/`): adds Debian `graphviz` (2.43)
and the script: 1.525 GB -> 1.573 GB (+48 MB, nearly all Graphviz and its
libraries), measured locally with Docker 29, linux/amd64.

**Diagrams image** (`images/runtime-diagrams/`, `FROM` the runtime image):

- draw.io web app from the pinned `draw.war` (v32.6.10, SHA-256 checked at
  build time) under `/opt/drawio`, without the Java parts, source maps,
  unminified sources and the integration bundle (86 MB).
- Playwright for Python (pinned) and its headless Chromium shell under
  `/opt/ms-playwright`, plus only the shared libraries that shell needs.
- `DRAWIO_WEBAPP=/opt/drawio`, `PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright`.
- Measured the same way: 2.133 GB, i.e. +560 MB over the runtime image (Chromium 283 MB, Playwright with its Node.js
  driver 165 MB, draw.io 86 MB, libraries ~25 MB).

No network is needed at run time. The app is loaded under a made-up origin
(`https://drawio.invalid`) whose requests are all answered from
`/opt/drawio`; every other request is aborted, service workers are blocked,
and draw.io runs with `offline=1&stealth=1` and no export server. Chromium runs
with Playwright's defaults (no Chromium sandbox), a throwaway `HOME` and
`--disable-dev-shm-usage`, so it works as uid 65534 with all capabilities
dropped, `no-new-privileges` and a `noexec` `/tmp`.

Build locally from the repository root:

```bash
docker build -f images/runtime/Dockerfile -t c4a-runtime:dev .
docker build -f images/runtime-diagrams/Dockerfile --build-arg RUNTIME_IMAGE=c4a-runtime:dev \
  -t c4a-runtime-diagrams:dev .
```

## Tests

- Unit tests (no Docker; layout tests are skipped without `dot`):
  `python3 -m unittest discover -s images/runtime/drawio-tool/tests`
- Image smoke tests, run under the sandbox restrictions (no network, uid 65534,
  no capabilities):
  `images/runtime/drawio-tool/tests/docker-smoke.sh c4a-runtime:dev` and
  `images/runtime/drawio-tool/tests/docker-smoke.sh c4a-runtime-diagrams:dev --diagrams`.
  The diagrams run converts a Mermaid flowchart, renders PNG/SVG/PDF and
  imports a small generated `.vsdx` (`tests/fixtures/make_vsdx.py`).

## Licenses

- `drawio-tool`: this repository's license (Apache-2.0).
- Graphviz: EPL-1.0 (Debian package; `/usr/share/doc/graphviz/copyright`). It
  is called as a separate program. `graphviz2drawio` (GPL) is not used.
- draw.io: Apache-2.0, shipped as `/opt/drawio/LICENSE` (also
  `/usr/share/doc/drawio/LICENSE`), copied from the v32.6.10 tag
  (`images/runtime-diagrams/DRAWIO-LICENSE`).
- Playwright: Apache-2.0 (in its Python package metadata). Chromium headless
  shell: BSD-3-Clause and third-party notices, shipped by Playwright as
  `LICENSE.headless_shell` next to the binary.
