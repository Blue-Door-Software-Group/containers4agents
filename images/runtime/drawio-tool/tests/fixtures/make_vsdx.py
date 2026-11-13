"""Write a tiny, hand-made Visio .vsdx: two labelled boxes joined by a connector.

Usage: python3 make_vsdx.py OUT.vsdx
"""

import sys
import zipfile

NS = 'xmlns="http://schemas.microsoft.com/office/visio/2012/main" ' \
     'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG_REL = "http://schemas.openxmlformats.org/package/2006/relationships"
VISIO_REL = "http://schemas.microsoft.com/visio/2010/relationships"

CONTENT_TYPES = """<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/visio/document.xml" ContentType="application/vnd.ms-visio.drawing.main+xml"/>
<Override PartName="/visio/pages/pages.xml" ContentType="application/vnd.ms-visio.pages+xml"/>
<Override PartName="/visio/pages/page1.xml" ContentType="application/vnd.ms-visio.page+xml"/>
</Types>"""

ROOT_RELS = f"""<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="{PKG_REL}">
<Relationship Id="rId1" Type="{VISIO_REL}/document" Target="visio/document.xml"/>
</Relationships>"""

DOCUMENT = f"""<?xml version="1.0" encoding="UTF-8"?>
<VisioDocument {NS}><DocumentSettings/></VisioDocument>"""

DOCUMENT_RELS = f"""<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="{PKG_REL}">
<Relationship Id="rId1" Type="{VISIO_REL}/pages" Target="pages/pages.xml"/>
</Relationships>"""

PAGES = f"""<?xml version="1.0" encoding="UTF-8"?>
<Pages {NS}>
<Page ID="0" NameU="Page-1" Name="Page-1">
<PageSheet><Cell N="PageWidth" V="8.5"/><Cell N="PageHeight" V="11"/><Cell N="PageScale" V="1"/><Cell N="DrawingScale" V="1"/></PageSheet>
<Rel r:id="rId1"/>
</Page>
</Pages>"""

PAGES_RELS = f"""<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="{PKG_REL}">
<Relationship Id="rId1" Type="{VISIO_REL}/page" Target="page1.xml"/>
</Relationships>"""


def box(shape_id, x, y, text):
    return f"""<Shape ID="{shape_id}" Type="Shape" LineStyle="0" FillStyle="0" TextStyle="0">
<Cell N="PinX" V="{x}"/><Cell N="PinY" V="{y}"/><Cell N="Width" V="1.5"/><Cell N="Height" V="0.75"/>
<Cell N="LocPinX" V="0.75" F="Width*0.5"/><Cell N="LocPinY" V="0.375" F="Height*0.5"/>
<Cell N="FillForegnd" V="#dae8fc"/><Cell N="FillPattern" V="1"/><Cell N="LineColor" V="#000000"/><Cell N="LineWeight" V="0.01"/>
<Section N="Geometry" IX="0">
<Row T="MoveTo" IX="1"><Cell N="X" V="0"/><Cell N="Y" V="0"/></Row>
<Row T="LineTo" IX="2"><Cell N="X" V="1.5"/><Cell N="Y" V="0"/></Row>
<Row T="LineTo" IX="3"><Cell N="X" V="1.5"/><Cell N="Y" V="0.75"/></Row>
<Row T="LineTo" IX="4"><Cell N="X" V="0"/><Cell N="Y" V="0.75"/></Row>
<Row T="LineTo" IX="5"><Cell N="X" V="0"/><Cell N="Y" V="0"/></Row>
</Section>
<Text>{text}</Text>
</Shape>"""


PAGE1 = f"""<?xml version="1.0" encoding="UTF-8"?>
<PageContents {NS}>
<Shapes>
{box(1, 2, 8, "Alpha")}
{box(2, 5, 8, "Beta")}
<Shape ID="3" Type="Shape" LineStyle="0" FillStyle="0" TextStyle="0">
<Cell N="PinX" V="3.5"/><Cell N="PinY" V="8"/><Cell N="Width" V="1.5"/><Cell N="Height" V="0"/>
<Cell N="LocPinX" V="0.75"/><Cell N="LocPinY" V="0"/>
<Cell N="BeginX" V="2.75"/><Cell N="BeginY" V="8"/><Cell N="EndX" V="4.25"/><Cell N="EndY" V="8"/>
<Cell N="LineColor" V="#000000"/><Cell N="LineWeight" V="0.01"/><Cell N="EndArrow" V="4"/><Cell N="ObjType" V="2"/>
<Section N="Geometry" IX="0"><Cell N="NoFill" V="1"/>
<Row T="MoveTo" IX="1"><Cell N="X" V="0"/><Cell N="Y" V="0"/></Row>
<Row T="LineTo" IX="2"><Cell N="X" V="1.5"/><Cell N="Y" V="0"/></Row>
</Section>
</Shape>
</Shapes>
<Connects>
<Connect FromSheet="3" FromCell="BeginX" FromPart="9" ToSheet="1" ToCell="PinX" ToPart="3"/>
<Connect FromSheet="3" FromCell="EndX" FromPart="12" ToSheet="2" ToCell="PinX" ToPart="3"/>
</Connects>
</PageContents>"""


def main(out):
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", CONTENT_TYPES)
        z.writestr("_rels/.rels", ROOT_RELS)
        z.writestr("visio/document.xml", DOCUMENT)
        z.writestr("visio/_rels/document.xml.rels", DOCUMENT_RELS)
        z.writestr("visio/pages/pages.xml", PAGES)
        z.writestr("visio/pages/_rels/pages.xml.rels", PAGES_RELS)
        z.writestr("visio/pages/page1.xml", PAGE1)


if __name__ == "__main__":
    main(sys.argv[1])
