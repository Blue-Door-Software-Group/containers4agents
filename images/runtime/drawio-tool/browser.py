"""Browser-backed drawio-tool commands: render, from-mermaid, from-vsdx.

Runs the draw.io web app (static files under $DRAWIO_WEBAPP) in headless
Chromium through Playwright. Every request is answered from local files under
a made-up origin; anything else is aborted, so this works without a network.
"""

import base64
import glob
import importlib.util
import mimetypes
import os
import tempfile
from urllib.parse import urlsplit, unquote

import drawio_tool as dt

WEBAPP = os.environ.get("DRAWIO_WEBAPP", "/opt/drawio")
ORIGIN = "https://drawio.invalid"  # never resolved: every request is routed below
EMBED_URL = f"{ORIGIN}/index.html?embed=1&proto=json&spin=0&stealth=1&offline=1&pwa=0&lang=en&noExitBtn=1"

HOST_PAGE = """<!doctype html><html><body style="margin:0">
<iframe id="f" style="width:1400px;height:1000px;border:0"></iframe>
<script>
const frame = document.getElementById('f');
const replies = [];
let waiter = null;
addEventListener('message', e => {
  if (e.source !== frame.contentWindow || typeof e.data !== 'string') return;
  try { replies.push(JSON.parse(e.data)); } catch (err) { return; }
  if (waiter) waiter();
});
// Resolves with the next draw.io message whose event is one of `events`.
window.waitFor = (events, ms) => new Promise((resolve, reject) => {
  const timer = setTimeout(() => { waiter = null; reject(new Error('draw.io did not answer (' + events + ')')); }, ms);
  waiter = () => {
    const i = replies.findIndex(m => events.includes(m.event));
    if (i >= 0) { clearTimeout(timer); waiter = null; resolve(replies.splice(i, 1)[0]); }
  };
  waiter();
});
window.call = (msg, events, ms) => {
  const reply = waitFor(events, ms);
  frame.contentWindow.postMessage(JSON.stringify(msg), '*');
  return reply;
};
</script></body></html>"""


def status():
    if importlib.util.find_spec("playwright") is None:
        return False, "Playwright is not installed"
    if not os.path.isfile(os.path.join(WEBAPP, "index.html")):
        return False, f"draw.io web app not found at {WEBAPP}"
    browsers = os.environ.get("PLAYWRIGHT_BROWSERS_PATH", os.path.expanduser("~/.cache/ms-playwright"))
    if not glob.glob(os.path.join(browsers, "chromium*")):
        return False, f"no Playwright Chromium under {browsers}"
    return True, None


class DrawioBrowser:
    """Headless Chromium with the draw.io app served from local files."""

    def __init__(self, timeout):
        self.timeout_ms = int(timeout * 1000)

    def __enter__(self):
        from playwright.sync_api import sync_playwright

        # Chromium wants a writable HOME; exec users (e.g. nobody) may not have one.
        self.home = tempfile.TemporaryDirectory(prefix="drawio-tool-")
        env = dict(os.environ, HOME=self.home.name)
        self.pw = sync_playwright().start()
        self.browser = self.pw.chromium.launch(
            env=env, args=["--disable-dev-shm-usage", "--disable-gpu", "--no-first-run"])
        self.context = self.browser.new_context(service_workers="block", viewport={"width": 1400, "height": 1000})
        self.context.set_default_timeout(self.timeout_ms)
        self.context.route("**/*", self._serve)
        self.page = self.context.new_page()
        self.errors = []
        self.page.on("pageerror", lambda e: self.errors.append(str(e)))
        return self

    def __exit__(self, *exc):
        for close in (self.context.close, self.browser.close, self.pw.stop, self.home.cleanup):
            try:
                close()
            except Exception:
                pass

    def _serve(self, route):
        url = urlsplit(route.request.url)
        if f"{url.scheme}://{url.netloc}" != ORIGIN:
            return route.abort("blockedbyclient")
        if url.path == "/__host.html":
            return route.fulfill(body=HOST_PAGE, content_type="text/html")
        path = os.path.realpath(os.path.join(WEBAPP, unquote(url.path).lstrip("/")))
        if not path.startswith(os.path.realpath(WEBAPP) + os.sep) or not os.path.isfile(path):
            return route.fulfill(status=404, body="not found")
        ctype = mimetypes.guess_type(path)[0] or "application/octet-stream"
        return route.fulfill(path=path, content_type=ctype)

    def embed(self):
        """Open draw.io in embed mode inside the host page and wait for its init."""
        self.page.goto(f"{ORIGIN}/__host.html")
        self.page.evaluate("url => { document.getElementById('f').src = url; }", EMBED_URL)
        self.page.evaluate("ms => waitFor(['init'], ms)", self.timeout_ms)

    def call(self, message, events):
        reply = self.page.evaluate("([m, e, ms]) => call(m, e, ms)", [message, events, self.timeout_ms])
        if reply.get("error"):
            raise dt.ToolError(f"draw.io: {reply['error']}")
        return reply


def data_uri_bytes(uri):
    header, _, payload = uri.partition(",")
    return base64.b64decode(payload) if header.endswith(";base64") else unquote(payload).encode("utf-8")


def single_page_xml(path, selector):
    """The selected page as an uncompressed one-page mxfile."""
    doc = dt.Document.load(path)
    page = doc.page(selector)
    if page.model is None:
        raise dt.ToolError("page has no diagram content")
    page.expand()
    for other in doc.pages:
        if other is not page and other.diagram is not None:
            doc.root.remove(other.diagram)
    return doc.tostring(), page


def normalized(xml):
    """Uncompressed draw.io XML, with the number of vertices on all pages."""
    doc = dt.Document.parse(xml.encode("utf-8"))
    for page in doc.pages:
        page.expand()
    if doc.root.get("compressed") is not None:
        doc.root.set("compressed", "false")
    vertices = [c for p in doc.pages if p.model is not None for c in dt.Graph(p).cells if c.is_vertex]
    return doc, vertices


def render(args):
    fmt = args.format or os.path.splitext(args.output)[1].lstrip(".").lower()
    if fmt not in ("png", "svg", "pdf"):
        raise dt.ToolError("use --format png|svg|pdf (or an output name ending in one)", dt.EXIT_USAGE)
    xml, page = single_page_xml(args.input, args.page)
    background = args.background or ("#ffffff" if fmt == "pdf" else None)
    with DrawioBrowser(args.timeout) as b:
        b.embed()
        b.call({"action": "load", "xml": xml, "autosave": 0}, ["load"])
        message = {"action": "export", "format": "png" if fmt == "png" else "svg",
                   "scale": args.scale, "border": args.border, "asText": True}
        if background:
            message["background"] = background
        reply = b.call(message, ["export"])
        if fmt == "png":
            data = data_uri_bytes(reply["data"])
        elif fmt == "svg":
            data = reply["data"].encode("utf-8")
        else:
            data = svg_to_pdf(b, reply["data"])
    dt.write_atomic(args.output, data)
    return {"format": fmt, "bytes": len(data), "page": page.info()}


def svg_to_pdf(b, svg):
    """Print the SVG with Chromium's own PDF printer, one page sized to the drawing."""
    pdf_page = b.context.new_page()
    pdf_page.set_content("<!doctype html><html><head><style>@page{margin:0} body{margin:0} "
                         "svg{display:block}</style></head><body>" + svg + "</body></html>")
    size = pdf_page.evaluate("""() => { const r = document.querySelector('svg').getBoundingClientRect();
        return [Math.ceil(r.width), Math.ceil(r.height)]; }""")
    width, height = max(size[0], 1), max(size[1], 1)
    pdf = pdf_page.pdf(width=f"{width}px", height=f"{height}px", print_background=True, page_ranges="1")
    pdf_page.close()
    return pdf


def from_mermaid(args):
    try:
        with open(args.input, encoding="utf-8") as f:
            source = f.read()
    except OSError as e:
        raise dt.ToolError(f"cannot read {args.input}: {e.strerror}")
    with DrawioBrowser(args.timeout) as b:
        b.embed()
        reply = b.call({"action": "load", "autosave": 0,
                        "descriptor": {"format": "mermaid", "data": source}}, ["load"])
    if not reply.get("xml"):
        raise dt.ToolError("draw.io returned no diagram for this Mermaid source")
    doc, vertices = normalized(reply["xml"])
    native = [v for v in vertices if v.shape != "image" and "mermaidData" not in v.style]
    if not native:
        raise dt.ToolError("Mermaid was not converted to native shapes (unsupported diagram type?)")
    doc.save(args.output)
    return {"pages": len(doc.pages), "vertices": len(vertices)}


VSDX_IMPORT = """async ([b64, name]) => {
  const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
  const file = new File([bytes], name, {type: 'application/octet-stream'});
  return await new Promise((resolve, reject) => doImport(null, resolve,
      e => reject(new Error((e && e.message) || String(e || 'import failed'))), file));
}"""


def from_vsdx(args):
    try:
        with open(args.input, "rb") as f:
            data = f.read()
    except OSError as e:
        raise dt.ToolError(f"cannot read {args.input}: {e.strerror}")
    name = os.path.basename(args.input) if args.input.lower().endswith((".vsdx", ".vssx")) else "input.vsdx"
    with DrawioBrowser(args.timeout) as b:
        # draw.io's standalone importer page; doImport() wraps EditorUi.importVisio.
        b.page.goto(f"{ORIGIN}/vsdxImporter.html")
        try:
            xml = b.page.evaluate(VSDX_IMPORT, [base64.b64encode(data).decode("ascii"), name])
        except Exception as e:
            raise dt.ToolError(f"Visio import failed: {str(e).splitlines()[0]}")
    if not xml:
        raise dt.ToolError("Visio import returned nothing")
    doc, vertices = normalized(xml)
    doc.save(args.output)
    return {"pages": len(doc.pages), "vertices": len(vertices)}
