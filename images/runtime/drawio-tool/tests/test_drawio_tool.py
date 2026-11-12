"""Unit tests for drawio-tool. Run: python3 -m unittest discover -s images/runtime/drawio-tool/tests

Layout tests need Graphviz `dot` on PATH and are skipped without it.
"""

import contextlib
import importlib.util
import io
import json
import os
import shutil
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(HERE))
import drawio_tool as dt  # noqa: E402

SAMPLE = os.path.join(HERE, "fixtures", "sample.drawio")

# Graph.compress() output of draw.io 32.6.10 for REFERENCE_XML.
REFERENCE_XML = ('<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="a" '
                 'value="Héllo &amp; ü" vertex="1" parent="1"><mxGeometry x="10" y="20" width="80" '
                 'height="40" as="geometry"/></mxCell></root></mxGraphModel>')
REFERENCE_COMPRESSED = ("jVC7DsIwDPwa7yFBCEYaoCx8RKRaTaWEVCFA+vektUvFUInB0vnO5xco7XMdTW9voUEH6gxKxxASIZ81Ogd"
                        "SdA2oE0gpSoC8rKibSRW9iXhP/xgMGV7GPZGYK2gFx4NzAUbvzvgeVDXiwlea6zEmzKszJ4oH1hg8pjiUktlA"
                        "K4mBUt5QvLsmWaL2TFnsWss9t8yZB+Xtt+9yXgF84Zwun5y0n0d/AA==")


def run(*argv):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = dt.main(list(argv))
    return code, json.loads(out.getvalue())


def write(directory, name, text):
    path = os.path.join(directory, name)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    return path


def model_xml(page):
    page.model.tail = None
    return ET.tostring(page.model, encoding="unicode")


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)

    def path(self, name):
        return os.path.join(self.tmp, name)


class InspectTest(Base):
    def test_outline(self):
        code, out = run("inspect", SAMPLE)
        self.assertEqual(code, 0)
        self.assertTrue(out["ok"])
        self.assertEqual([p["name"] for p in out["pages"]], ["Flow", "Notes"])
        els = {e["id"]: e for e in out["pages"][0]["elements"]}
        self.assertEqual(els["check"]["label"], "Check\ninput")  # html stripped, <object> wrapper
        self.assertEqual(els["check"]["kind"], "vertex")
        self.assertEqual(els["lane"]["kind"], "container")
        self.assertEqual(els["api"]["parent"], "lane")
        self.assertEqual(els["db"]["shape"], "cylinder3")
        self.assertEqual((els["e1"]["source"], els["e1"]["target"]), ("start", "check"))
        self.assertEqual(els["e1"]["points"], [[300, 300]])
        self.assertTrue(els["e1label"]["geometry"]["relative"])
        self.assertEqual(out["pages"][0]["layers"], [{"id": "1", "name": ""}])

    def test_missing_file(self):
        code, out = run("inspect", self.path("nope.drawio"))
        self.assertEqual(code, 1)
        self.assertFalse(out["ok"])
        self.assertIn("cannot read", out["error"])


class ValidateTest(Base):
    def test_sample_is_valid(self):
        self.assertEqual(run("validate", SAMPLE), (0, {"ok": True, "valid": True, "problems": []}))

    def test_problems(self):
        path = write(self.tmp, "bad.drawio", """<mxGraphModel><root>
            <mxCell id="x"/>
            <mxCell id="a" vertex="1" parent="ghost" style="rounded=1;=oops;bad key=1"/>
            <mxCell id="a" vertex="1" parent="ghost"/>
            <mxCell id="e" edge="1" parent="ghost" source="a" target="missing"/>
        </root></mxGraphModel>""")
        code, out = run("validate", path)
        self.assertEqual(code, 0)
        self.assertFalse(out["valid"])
        codes = {p["code"] for p in out["problems"]}
        self.assertEqual(codes, {"duplicate-id", "missing-root", "missing-layer", "malformed-style",
                                 "missing-parent", "dangling-target"})

    def test_not_xml(self):
        code, out = run("validate", write(self.tmp, "x.drawio", "<mxfile><diagram>"))
        self.assertEqual(code, 0)
        self.assertEqual(out["problems"][0]["code"], "parse")


class CompressionTest(Base):
    def test_matches_drawio(self):
        self.assertEqual(dt.inflate_text(REFERENCE_COMPRESSED), REFERENCE_XML)
        self.assertEqual(dt.inflate_text(dt.deflate_text(REFERENCE_XML)), REFERENCE_XML)

    def test_round_trip(self):
        packed, unpacked = self.path("packed.drawio"), self.path("unpacked.drawio")
        self.assertEqual(run("deflate", SAMPLE, packed)[1], {"ok": True, "pages": 2, "changed": 2})
        doc = dt.Document.load(packed)
        self.assertTrue(all(p.compressed for p in doc.pages))
        self.assertIsNone(doc.root.find("diagram/mxGraphModel"))
        self.assertEqual(run("inflate", packed, unpacked)[1], {"ok": True, "pages": 2, "changed": 2})
        original, restored = dt.Document.load(SAMPLE), dt.Document.load(unpacked)
        for a, b in zip(original.pages, restored.pages):
            self.assertFalse(b.compressed)
            self.assertEqual(model_xml(a), model_xml(b))
        self.assertEqual(run("inspect", packed)[1]["pages"][0]["elements"],
                         run("inspect", SAMPLE)[1]["pages"][0]["elements"])


@unittest.skipUnless(shutil.which("dot"), "Graphviz dot not installed")
class LayoutTest(Base):
    def layout(self, *extra, source=SAMPLE):
        out_path = self.path("out.drawio")
        code, out = run("layout", source, out_path, *extra)
        self.assertEqual(code, 0, out)
        return out, dt.Document.load(out_path)

    def cells(self, doc, page=0):
        return {c.id: c for c in dt.Graph(doc.pages[page]).cells}

    def assert_no_overlap(self, cells, ids):
        rects = [cells[i].rect() for i in ids]
        for i, a in enumerate(rects):
            for b in rects[i + 1:]:
                apart = (a["x"] + a["width"] <= b["x"] or b["x"] + b["width"] <= a["x"] or
                         a["y"] + a["height"] <= b["y"] or b["y"] + b["height"] <= a["y"])
                self.assertTrue(apart, f"{a} overlaps {b}")

    def check(self, direction):
        result, doc = self.layout("--direction", direction)
        self.assertEqual((result["vertices"], result["containers"], result["edges"]), (5, 1, 5))
        self.assertEqual(result["cleared_waypoints"], 1)
        before, after = self.cells(dt.Document.load(SAMPLE)), self.cells(doc)
        self.assertEqual(list(before), list(after))  # same ids, same order
        for cid, c in before.items():
            self.assertEqual(c.style_text, after[cid].style_text)
            self.assertEqual(c.raw_label, after[cid].raw_label)
            self.assertEqual((c.parent, c.source, c.target), (after[cid].parent, after[cid].source, after[cid].target))
        for cid in ("start", "check", "lane", "api", "db", "end"):
            self.assertNotEqual(before[cid].rect(), after[cid].rect(), cid)
            r = after[cid].rect()
            self.assertGreaterEqual(min(r["x"], r["y"]), 0, cid)
        self.assert_no_overlap(after, ["start", "check", "lane", "end"])
        self.assert_no_overlap(after, ["api", "db"])
        lane = after["lane"].rect()
        for cid in ("api", "db"):  # children fit inside the lane, below its 30px header
            r = after[cid].rect()
            self.assertGreaterEqual(r["y"], 30)
            self.assertLessEqual(r["x"] + r["width"], lane["width"])
            self.assertLessEqual(r["y"] + r["height"], lane["height"])
        self.assertEqual(after["e1"].points(), [])
        self.assertEqual(after["e1label"].rect(), before["e1label"].rect())  # relative label untouched
        self.assertEqual(after["check"].outer.get("owner"), "ops")  # unknown attributes kept
        return doc

    def test_top_bottom(self):
        doc = self.check("TB")
        cells = self.cells(doc)
        self.assertLess(cells["start"].rect()["y"], cells["check"].rect()["y"])

    def test_left_right(self):
        doc = self.check("LR")
        cells = self.cells(doc)
        self.assertLess(cells["start"].rect()["x"], cells["check"].rect()["x"])

    def test_other_pages_untouched(self):
        _, doc = self.layout()
        self.assertEqual(model_xml(doc.pages[1]), model_xml(dt.Document.load(SAMPLE).pages[1]))

    def test_page_selection_and_compressed_input(self):
        packed = self.path("packed.drawio")
        run("deflate", SAMPLE, packed)
        result, doc = self.layout("--page", "Notes", source=packed)
        self.assertEqual(result["page"]["name"], "Notes")
        self.assertFalse(doc.pages[1].compressed)
        self.assertTrue(doc.pages[0].compressed)
        code, out = run("layout", packed, self.path("x.drawio"), "--page", "nope")
        self.assertEqual(code, 1)
        self.assertIn("no page", out["error"])

    def test_table_children_are_left_alone(self):
        path = write(self.tmp, "t.drawio", """<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>
          <mxCell id="t" style="shape=table;childLayout=tableLayout;container=1;" vertex="1" parent="1">
            <mxGeometry x="5" y="5" width="100" height="60" as="geometry"/></mxCell>
          <mxCell id="r" style="shape=tableRow;" vertex="1" parent="t">
            <mxGeometry x="0" y="0" width="100" height="30" as="geometry"/></mxCell>
          <mxCell id="b" vertex="1" parent="1"><mxGeometry width="40" height="40" as="geometry"/></mxCell>
          <mxCell id="e" edge="1" parent="1" source="r" target="b"><mxGeometry relative="1" as="geometry"/></mxCell>
        </root></mxGraphModel>""")
        result, doc = self.layout(source=path)
        self.assertEqual((result["vertices"], result["containers"], result["edges"]), (2, 0, 1))
        cells = self.cells(doc)
        self.assertEqual(cells["r"].rect(), {"x": 0, "y": 0, "width": 100, "height": 30})
        self.assertEqual(cells["t"].rect()["width"], 100)


class CliTest(Base):
    def test_usage_error(self):
        code, out = run("layout", SAMPLE)
        self.assertEqual(code, 2)
        self.assertFalse(out["ok"])

    def test_capabilities(self):
        code, out = run("capabilities")
        self.assertEqual(code, 0)
        self.assertTrue(out["commands"]["inspect"])
        self.assertEqual(out["commands"]["layout"], bool(shutil.which("dot")))

    @unittest.skipIf(importlib.util.find_spec("playwright"), "browser support is installed")
    def test_browser_commands_unavailable(self):
        code, out = run("render", SAMPLE, self.path("x.png"))
        self.assertEqual(code, 3)
        self.assertIn("not available", out["error"])
        self.assertFalse(run("capabilities")[1]["commands"]["from-mermaid"])


if __name__ == "__main__":
    unittest.main()
