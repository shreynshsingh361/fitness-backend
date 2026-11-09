"""A tiny reader for the first sheet of an .xlsx file, so no extra package is needed.

An .xlsx file is a zip of XML. Only what the food import needs is handled: text and numbers.
"""
import re
import zipfile

import xml.etree.ElementTree as ET
NS = {"m": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}

def col_index(ref):
    letters = re.match(r"[A-Z]+", ref).group(0)
    n = 0
    for ch in letters:
        n = n * 26 + ord(ch) - 64
    return n - 1

def rows(path):
    """Yields each row of the first sheet as a list; empty cells are None."""
    z = zipfile.ZipFile(path)
    shared = []
    if "xl/sharedStrings.xml" in z.namelist():
        for si in ET.fromstring(z.read("xl/sharedStrings.xml")).findall("m:si", NS):
            shared.append("".join(t.text or "" for t in si.iter("{%s}t" % NS["m"])))
    sheet = ET.fromstring(z.read("xl/worksheets/sheet1.xml"))
    for r in sheet.find("m:sheetData", NS).findall("m:row", NS):
        out = {}
        for c in r.findall("m:c", NS):
            v = c.find("m:v", NS)
            if v is None:
                continue
            val = shared[int(v.text)] if c.get("t") == "s" else v.text
            out[col_index(c.get("r"))] = val
        yield [out.get(i) for i in range(max(out) + 1)] if out else []
