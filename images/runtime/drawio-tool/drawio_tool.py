#!/usr/bin/env python3
"""drawio-tool: inspect, validate, (de)compress, lay out and convert draw.io files.

Every command prints exactly one JSON object on stdout: {"ok": true, ...} on
success, {"ok": false, "error": "..."} with a non-zero exit code otherwise.
The contract is documented in docs/DIAGRAM_TOOLS.md.
"""

import argparse
import base64
import html
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import zlib
from urllib.parse import quote, unquote
import xml.etree.ElementTree as ET

try:  # defusedxml is installed in the runtime image; fall back for local tests.
    from defusedxml.ElementTree import fromstring as parse_xml
except ImportError:  # pragma: no cover
    parse_xml = ET.fromstring

# browser.py does `import drawio_tool`; make that the running module, not a second copy.
sys.modules.setdefault("drawio_tool", sys.modules[__name__])

VERSION = "1"
EXIT_OK, EXIT_FAILED, EXIT_USAGE, EXIT_UNAVAILABLE = 0, 1, 2, 3
BROWSER_COMMANDS = ("render", "from-mermaid", "from-vsdx")


class ToolError(Exception):
    def __init__(self, message, exit_code=EXIT_FAILED):
        super().__init__(message)
        self.exit_code = exit_code


# --- compressed page form ----------------------------------------------------

def inflate_text(text):
    """draw.io compressed page text -> XML (base64 -> raw inflate -> URI decode)."""
    try:
        raw = zlib.decompress(base64.b64decode(text.strip(), validate=False), -15)
    except (ValueError, zlib.error) as e:
        raise ToolError(f"cannot decompress page: {e}")
    return unquote(raw.decode("utf-8"))


def deflate_text(xml):
    """XML -> draw.io compressed page text (encodeURIComponent -> raw deflate -> base64)."""
    c = zlib.compressobj(9, zlib.DEFLATED, -15)
    raw = c.compress(quote(xml, safe="!*'()~").encode("ascii")) + c.flush()
    return base64.b64encode(raw).decode("ascii")


# --- document model ----------------------------------------------------------

class Page:
    """One <diagram> of an <mxfile> (or a bare <mxGraphModel> file)."""

    def __init__(self, index, diagram, model, compressed):
        self.index, self.diagram, self.model, self.compressed = index, diagram, model, compressed

    @property
    def id(self):
        return self.diagram.get("id") if self.diagram is not None else None

    @property
    def name(self):
        return self.diagram.get("name") if self.diagram is not None else None

    def expand(self):
        """Store this page uncompressed in the document (no-op if it already is)."""
        if self.compressed:
            self.diagram.text = None
            self.diagram.append(self.model)
            self.compressed = False

    def info(self):
        return {"index": self.index, "id": self.id, "name": self.name, "compressed": self.compressed}


class Document:
    def __init__(self, root, pages):
        self.root, self.pages = root, pages

    @classmethod
    def parse(cls, data):
        try:
            root = parse_xml(data)
        except ET.ParseError as e:
            raise ToolError(f"invalid XML: {e}")
        except Exception as e:  # defusedxml raises its own errors for entities/DTDs
            raise ToolError(f"rejected XML: {e}")
        if root.tag == "mxGraphModel":
            return cls(root, [Page(0, None, root, False)])
        if root.tag != "mxfile":
            raise ToolError(f"not a draw.io file (root element <{root.tag}>)")
        pages = []
        for i, diagram in enumerate(root.findall("diagram")):
            model = diagram.find("mxGraphModel")
            compressed = model is None and bool((diagram.text or "").strip())
            if compressed:
                try:
                    model = parse_xml(inflate_text(diagram.text))
                except ET.ParseError as e:
                    raise ToolError(f"page {i}: invalid compressed XML: {e}")
            pages.append(Page(i, diagram, model, compressed))
        return cls(root, pages)

    @classmethod
    def load(cls, path):
        try:
            with open(path, "rb") as f:
                return cls.parse(f.read())
        except OSError as e:
            raise ToolError(f"cannot read {path}: {e.strerror}")

    def page(self, selector):
        """Find a page by id, then name, then 0-based index. None selects the first page."""
        if not self.pages:
            raise ToolError("file has no pages")
        if selector is None:
            return self.pages[0]
        for attr in ("id", "name"):
            for p in self.pages:
                if getattr(p, attr) == selector:
                    return p
        if selector.isdigit() and int(selector) < len(self.pages):
            return self.pages[int(selector)]
        raise ToolError(f"no page matches {selector!r}")

    def tostring(self):
        return ET.tostring(self.root, encoding="unicode")

    def save(self, path):
        write_atomic(path, self.tostring().encode("utf-8"))


def write_atomic(path, data):
    directory = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".drawio-tool-")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise


STYLE_NAME = re.compile(r"^[A-Za-z_][\w.:\-]*$")


def parse_style(style):
    """'rounded=1;swimlane;' -> ({'rounded': '1', 'swimlane': None}, [bad tokens])."""
    values, bad = {}, []
    for token in (style or "").split(";"):
        if token == "":
            continue
        key, sep, value = token.partition("=")
        if not STYLE_NAME.match(key):
            bad.append(token)
        else:
            values[key] = value if sep else None
    return values, bad


class Cell:
    """An mxCell, possibly wrapped in <object>/<UserObject> that carries id and label."""

    def __init__(self, outer):
        self.outer = outer
        self.cell = outer if outer.tag == "mxCell" else outer.find("mxCell")
        if self.cell is None:
            self.cell = outer
        self.id = outer.get("id")
        self.parent = self.cell.get("parent")
        self.source = self.cell.get("source")
        self.target = self.cell.get("target")
        self.is_vertex = self.cell.get("vertex") == "1"
        self.is_edge = self.cell.get("edge") == "1"
        self.style_text = self.cell.get("style") or ""
        self.style, self.bad_style = parse_style(self.style_text)
        self.geometry = self.cell.find("mxGeometry")

    @property
    def raw_label(self):
        return self.outer.get("label") if self.outer is not self.cell else self.cell.get("value")

    @property
    def label(self):
        value = self.raw_label or ""
        if self.style.get("html") == "1":
            value = re.sub(r"<br\s*/?>|</(div|p|li|tr|h\d)>", "\n", value, flags=re.I)
            value = html.unescape(re.sub(r"<[^>]*>", "", value))
        lines = (" ".join(line.split()) for line in value.splitlines())
        return "\n".join(line for line in lines if line)

    @property
    def relative(self):
        return self.geometry is not None and self.geometry.get("relative") == "1"

    def rect(self):
        g = self.geometry
        if g is None:
            return {"x": 0, "y": 0, "width": 0, "height": 0}
        return {k: num(g.get(k, "0")) for k in ("x", "y", "width", "height")}

    def points(self):
        if self.geometry is None:
            return []
        arr = self.geometry.find("Array[@as='points']")
        return [[num(p.get("x", "0")), num(p.get("y", "0"))] for p in (arr if arr is not None else [])]

    @property
    def container_style(self):
        """Styled as a container whose children we may lay out (not a table/stack etc.)."""
        s = self.style
        container = s.get("container") == "1" or "swimlane" in s or s.get("shape") == "swimlane" or "group" in s
        return container and "childLayout" not in s and s.get("shape") not in ("table", "tableRow")

    @property
    def shape(self):
        if "shape" in self.style:  # inline stencils carry their whole XML; just say "stencil"
            return self.style["shape"].split("(", 1)[0]
        bare = [k for k, v in self.style.items() if v is None]
        return bare[0] if bare else None


def num(text):
    try:
        v = float(text)
    except (TypeError, ValueError):
        return 0
    return int(v) if v.is_integer() else round(v, 2)


def fmt(v):
    return str(int(round(v)))


class Graph:
    """Cells of one page plus lookup tables."""

    def __init__(self, page):
        root = page.model.find("root") if page.model is not None else None
        self.cells = [Cell(el) for el in (root if root is not None else [])]
        self.by_id = {}
        for c in self.cells:
            self.by_id.setdefault(c.id, c)
        self.children = {}
        for c in self.cells:
            self.children.setdefault(c.parent, []).append(c)
        self.root_ids = {c.id for c in self.cells if c.parent is None and c.id is not None}
        self.layer_ids = {c.id for c in self.cells if c.parent in self.root_ids}

    def kind(self, c):
        if c.id in self.root_ids:
            return "root"
        if c.id in self.layer_ids:
            return "layer"
        if c.is_edge:
            return "edge"
        if c.is_vertex:
            kids = self.children.get(c.id, [])
            if c.container_style or any(k.is_vertex and not k.relative for k in kids):
                return "container"
            return "vertex"
        return "cell"


# --- commands ----------------------------------------------------------------

def cmd_inspect(args):
    doc = Document.load(args.input)
    pages = []
    for page in doc.pages:
        g = Graph(page)
        elements = []
        for c in g.cells:
            kind = g.kind(c)
            if kind in ("root", "layer"):
                continue
            e = {"id": c.id, "kind": kind, "label": c.label, "parent": c.parent}
            if c.shape:
                e["shape"] = c.shape
            if kind == "edge":
                e["source"], e["target"] = c.source, c.target
                pts = c.points()
                if pts:
                    e["points"] = pts
            else:
                e["geometry"] = c.rect()
                if c.relative:
                    e["geometry"]["relative"] = True
            elements.append(e)
        layers = [{"id": c.id, "name": c.label} for c in g.cells if c.id in g.layer_ids]
        pages.append(dict(page.info(), layers=layers, elements=elements))
    return {"pages": pages}


def validate_doc(doc):
    problems = []
    for page in doc.pages:
        def add(code, message, cell_id=None):
            problems.append({"page": page.index, "id": cell_id, "code": code, "message": message})

        if page.model is None:
            add("empty-page", "page has no mxGraphModel")
            continue
        g = Graph(page)
        seen = set()
        for c in g.cells:
            if c.id is None:
                add("missing-id", f"<{c.outer.tag}> has no id")
            elif c.id in seen:
                add("duplicate-id", f"id {c.id!r} is used more than once", c.id)
            seen.add(c.id)
        if "0" not in g.by_id or g.by_id["0"].parent is not None:
            add("missing-root", "no root cell with id \"0\" and no parent")
        if not g.layer_ids:
            add("missing-layer", "no layer cell (e.g. id \"1\" with parent \"0\")")
        for c in g.cells:
            if c.parent is None and c.id not in g.root_ids:
                continue
            if c.parent is None and len(g.root_ids) > 1:
                add("missing-parent", "more than one cell without a parent", c.id)
            elif c.parent is not None and c.parent not in g.by_id:
                add("missing-parent", f"parent {c.parent!r} does not exist", c.id)
            for end in ("source", "target"):
                ref = getattr(c, end)
                if ref is not None and ref not in g.by_id:
                    add(f"dangling-{end}", f"{end} {ref!r} does not exist", c.id)
            if c.bad_style:
                add("malformed-style", f"bad style token(s): {'; '.join(c.bad_style)}", c.id)
            if c.is_vertex and c.is_edge:
                add("vertex-and-edge", "cell is marked both vertex and edge", c.id)
            seen_parents, p = set(), c.parent
            while p is not None and p in g.by_id and p not in seen_parents:
                seen_parents.add(p)
                p = g.by_id[p].parent
            if p is not None and p in seen_parents:
                add("parent-cycle", "cell's parent chain loops", c.id)
    return problems


def cmd_validate(args):
    try:
        doc = Document.load(args.input)
    except ToolError as e:
        if str(e).startswith("cannot read"):
            raise
        return {"valid": False, "problems": [{"page": None, "id": None, "code": "parse", "message": str(e)}]}
    problems = validate_doc(doc)
    return {"valid": not problems, "problems": problems}


def cmd_inflate(args):
    doc = Document.load(args.input)
    changed = 0
    for page in doc.pages:
        if page.compressed:
            page.expand()
            changed += 1
    if doc.root.get("compressed") is not None:
        doc.root.set("compressed", "false")
    doc.save(args.output)
    return {"pages": len(doc.pages), "changed": changed}


def cmd_deflate(args):
    doc = Document.load(args.input)
    if doc.root.tag == "mxGraphModel":  # wrap a bare model so it can hold compressed text
        mxfile = ET.Element("mxfile")
        ET.SubElement(mxfile, "diagram", id="page-1", name="Page-1").append(doc.root)
        doc = Document.parse(ET.tostring(mxfile))
    changed = 0
    for page in doc.pages:
        if not page.compressed and page.model is not None:
            page.diagram.remove(page.model)
            page.model.tail = None
            page.diagram.text = deflate_text(ET.tostring(page.model, encoding="unicode"))
            changed += 1
    if doc.root.get("compressed") is not None:
        doc.root.set("compressed", "true")
    doc.save(args.output)
    return {"pages": len(doc.pages), "changed": changed}


# --- layout --------------------------------------------------------------------

DEFAULT_SIZE = (120.0, 60.0)
CLUSTER_PAD = 16  # points between a container's border and its children
OFFSET = 20  # top-left margin of the laid-out drawing


def build_dot(g, direction):
    """Return (dot source, node name -> cell, cluster name -> cell, skipped edge count)."""
    nodes, clusters = {}, {}
    owner = {}  # laid-out cell id -> graphviz name
    first_node = {}  # cluster name -> a node inside it (edge anchor for lhead/ltail)
    lines = [
        "digraph G {",
        f'  graph [rankdir={direction}, nodesep=0.6, ranksep=0.8, compound=true];',
        '  node [shape=box, fixedsize=true, label=""];',
    ]

    def add(cell, indent):
        kids = [k for k in g.children.get(cell.id, []) if k.is_vertex and not k.relative]
        if cell.container_style and kids:
            name = f"cluster_{len(clusters)}"
            clusters[name] = cell
            owner[cell.id] = name
            pad = CLUSTER_PAD
            header = num(cell.style.get("startSize", "23")) if "swimlane" in cell.style or cell.style.get("shape") == "swimlane" else 0
            if header and cell.style.get("horizontal") == "0":
                pad, header = pad + header, 0  # header on the left: margin on every side
            label = ('<<TABLE BORDER="0" CELLPADDING="0" CELLSPACING="0"><TR><TD FIXEDSIZE="TRUE" '
                     f'WIDTH="1" HEIGHT="{max(int(header), 1)}"></TD></TR></TABLE>>') if header else '""'
            lines.append(f"{indent}subgraph {name} {{")
            lines.append(f"{indent}  graph [margin={pad}, label={label}, labelloc=t];")
            for k in kids:
                add(k, indent + "  ")
            lines.append(f"{indent}}}")
            for k in kids:
                first = first_node.get(owner[k.id], owner[k.id])
                if first in nodes:
                    first_node.setdefault(name, first)
        else:
            name = f"n{len(nodes)}"
            nodes[name] = cell
            owner[cell.id] = name
            r = cell.rect()
            w, h = (r["width"] or DEFAULT_SIZE[0]), (r["height"] or DEFAULT_SIZE[1])
            lines.append(f"{indent}{name} [width={w / 72:.4f}, height={h / 72:.4f}];")

    for c in g.cells:
        if c.parent in g.layer_ids and c.is_vertex and not c.relative:
            add(c, "  ")

    def resolve(cell_id):
        seen = set()
        while cell_id is not None and cell_id not in owner and cell_id not in seen:
            seen.add(cell_id)
            cell_id = g.by_id[cell_id].parent if cell_id in g.by_id else None
        return owner.get(cell_id)

    def ancestors(name):
        out, cell = set(), (nodes.get(name) or clusters.get(name))
        while cell is not None and cell.parent in owner:
            out.add(owner[cell.parent])
            cell = g.by_id[cell.parent]
        return out

    edges, skipped = [], 0
    for c in g.cells:
        if not c.is_edge:
            continue
        s, t = resolve(c.source), resolve(c.target)
        if s is None or t is None:
            skipped += 1
            continue
        edges.append(c)
        if s == t or s in ancestors(t) or t in ancestors(s):
            continue  # self loop or edge into its own container: nothing to lay out
        attrs = []
        if s in clusters:
            attrs.append(f"ltail={s}")
            s = first_node.get(s)
        if t in clusters:
            attrs.append(f"lhead={t}")
            t = first_node.get(t)
        if s is None or t is None:
            continue
        lines.append(f"  {s} -> {t} [{', '.join(attrs)}];")
    lines.append("}")
    return "\n".join(lines), nodes, clusters, edges, skipped


def run_dot(source):
    dot = shutil.which("dot")
    if dot is None:
        raise ToolError("layout needs Graphviz 'dot', which is not installed", EXIT_UNAVAILABLE)
    proc = subprocess.run([dot, "-Tjson"], input=source, capture_output=True, text=True, timeout=120)
    if proc.returncode != 0:
        raise ToolError(f"graphviz failed: {proc.stderr.strip()[:500]}")
    return json.loads(proc.stdout)


def set_geometry(cell, values):
    g = cell.geometry
    if g is None:
        g = ET.SubElement(cell.cell, "mxGeometry", {"as": "geometry"})
        cell.geometry = g
    for k, v in values.items():
        g.set(k, fmt(v))


# Where a connector is pinned to its shapes. Fixed for the old positions (draw.io's Mermaid import
# sets them for the direction it drew), so a layout that moves the shapes drops them, as it drops
# waypoints, and the editor picks the sides again.
CONNECTION_POINT_KEYS = ("exitX", "exitY", "exitDx", "exitDy", "exitPerimeter",
                         "entryX", "entryY", "entryDx", "entryDy", "entryPerimeter")


def without_style_keys(style, keys):
    """style with the given keys removed, everything else kept as written."""
    parts = [p for p in style.split(";") if p and p.split("=", 1)[0].strip() not in keys]
    return ";".join(parts) + (";" if parts and style.endswith(";") else "")


def layout_page(page, direction="TB"):
    g = Graph(page)
    source, nodes, clusters, edges, skipped = build_dot(g, direction)
    result = {"vertices": len(nodes), "containers": len(clusters), "edges": len(edges),
              "skipped_edges": skipped, "cleared_waypoints": 0, "cleared_connection_points": 0}
    if not nodes:
        return result
    out = run_dot(source)
    height = float(out["bb"].split(",")[3])
    absolute = {}  # graphviz name -> (x, y, w, h), y down, drawing coordinates
    for obj in out.get("objects", []):
        name = obj.get("name")
        if name in clusters:
            x1, y1, x2, y2 = (float(v) for v in obj["bb"].split(","))
            absolute[name] = (x1 + OFFSET, height - y2 + OFFSET, x2 - x1, y2 - y1)
        elif name in nodes:
            cx, cy = (float(v) for v in obj["pos"].split(","))
            w, h = float(obj["width"]) * 72, float(obj["height"]) * 72
            absolute[name] = (cx - w / 2 + OFFSET, height - cy - h / 2 + OFFSET, w, h)
    owner = {cell.id: name for name, cell in list(nodes.items()) + list(clusters.items())}
    for name, cell in list(nodes.items()) + list(clusters.items()):
        x, y, w, h = absolute[name]
        parent = owner.get(cell.parent)
        if parent is not None:  # children of a container are relative to it
            x, y = x - absolute[parent][0], y - absolute[parent][1]
        values = {"x": x, "y": y}
        if name in clusters:
            values.update(width=w, height=h)
        elif cell.geometry is None or not cell.rect()["width"]:
            values.update(width=w, height=h)
        set_geometry(cell, values)
    for e in edges:
        arr = e.geometry.find("Array[@as='points']") if e.geometry is not None else None
        if arr is not None:
            e.geometry.remove(arr)
            result["cleared_waypoints"] += 1
        style = e.cell.get("style") or ""
        unpinned = without_style_keys(style, CONNECTION_POINT_KEYS)
        if unpinned != style:
            e.cell.set("style", unpinned)
            result["cleared_connection_points"] += 1
    return result


def cmd_layout(args):
    doc = Document.load(args.input)
    page = doc.page(args.page)
    if page.model is None:
        raise ToolError("page has no diagram content")
    page.expand()
    result = layout_page(page, args.direction)
    doc.save(args.output)
    return dict(result, page=page.info())


# --- capabilities and browser-backed commands ------------------------------------

def browser_status():
    """(available, reason). The browser module lives next to this file."""
    try:
        import browser
    except ImportError as e:
        return False, f"browser support not installed: {e}"
    return browser.status()


def cmd_capabilities(args):
    commands = {name: True for name in ("capabilities", "inspect", "validate", "inflate", "deflate")}
    unavailable = {}
    commands["layout"] = shutil.which("dot") is not None
    if not commands["layout"]:
        unavailable["layout"] = "Graphviz 'dot' is not installed"
    ok, reason = browser_status()
    for name in BROWSER_COMMANDS:
        commands[name] = ok
        if not ok:
            unavailable[name] = reason
    return {"version": VERSION, "commands": commands, "unavailable": unavailable,
            "render_formats": ["png", "svg", "pdf"] if ok else []}


def browser_command(name):
    def run(args):
        ok, reason = browser_status()
        if not ok:
            raise ToolError(f"{name} is not available in this image: {reason}", EXIT_UNAVAILABLE)
        import browser
        return getattr(browser, name.replace("-", "_"))(args)
    return run


# --- CLI ---------------------------------------------------------------------------

class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise ToolError(f"usage: {message}", EXIT_USAGE)


def build_parser():
    p = Parser(prog="drawio-tool", description="draw.io diagram helper; prints one JSON object.")
    sub = p.add_subparsers(dest="command", parser_class=Parser)
    sub.required = True

    def command(name, fn, *positionals, help=None):
        sp = sub.add_parser(name, help=help)
        for arg in positionals:
            sp.add_argument(arg)
        sp.set_defaults(fn=fn)
        return sp

    command("capabilities", cmd_capabilities, help="list commands available in this image")
    command("inspect", cmd_inspect, "input", help="outline pages and elements")
    command("validate", cmd_validate, "input", help="report structural problems")
    command("inflate", cmd_inflate, "input", "output", help="store every page uncompressed")
    command("deflate", cmd_deflate, "input", "output", help="store every page compressed")
    sp = command("layout", cmd_layout, "input", "output", help="lay out one page with Graphviz")
    sp.add_argument("--direction", choices=["TB", "LR", "BT", "RL"], default="TB")
    sp.add_argument("--page")
    sp = command("render", browser_command("render"), "input", "output", help="render a page to PNG/SVG/PDF")
    sp.add_argument("--format", choices=["png", "svg", "pdf"])
    sp.add_argument("--page")
    sp.add_argument("--scale", type=float, default=1.0)
    sp.add_argument("--border", type=int, default=10)
    sp.add_argument("--background", help="CSS colour, default transparent (white for pdf)")
    command("from-mermaid", browser_command("from-mermaid"), "input", "output",
            help="convert Mermaid to native draw.io shapes")
    command("from-vsdx", browser_command("from-vsdx"), "input", "output", help="convert Visio .vsdx to .drawio")
    for name in BROWSER_COMMANDS:
        sub.choices[name].add_argument("--timeout", type=float, default=90, help="seconds")
    return p


def main(argv=None):
    try:
        args = build_parser().parse_args(argv)
        result = args.fn(args)
        code = EXIT_OK
        out = dict({"ok": True}, **result)
    except ToolError as e:
        code, out = e.exit_code, {"ok": False, "error": str(e)}
    except subprocess.TimeoutExpired:
        code, out = EXIT_FAILED, {"ok": False, "error": "timed out"}
    except Exception as e:  # never print a traceback instead of JSON
        code, out = EXIT_FAILED, {"ok": False, "error": f"{type(e).__name__}: {e}"}
    sys.stdout.write(json.dumps(out) + "\n")
    return code


if __name__ == "__main__":
    sys.exit(main())
