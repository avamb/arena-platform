from pdf_seating_adapter import extract_pages, render_diagnostic
from pdf_seating_adapter.tests.test_codex_acceptance import pg
import xml.etree.ElementTree as ET

def w(text,x=2,y=2):
    return {"text":text,"x0":x,"x1":x+5,"top":y,"bottom":y+5}

def test_exact_source_header_and_decor_only(tmp_path):
    p=pg();p["words"]=[w("FLOOR"),w("Stage",50,2)]
    m=extract_pages([p],"h",section_terms=["FLOOR"],decor_terms=["Stage"])
    assert m["sections"][0]["label"]=="FLOOR"
    assert all(s["section_id"]=="section-page-1" and s["row_label"] is None for s in m["seats"])
    assert m["summary"]["unknown_section_count"]==0
    assert m["decor"][0]["label"]=="Stage"
    out=tmp_path/"a.svg";render_diagnostic(m,out)
    root=ET.parse(out).getroot()
    assert "Stage" in [e.text for e in root.iter()]
    assert all(e.get("data-sellable")=="false" for e in root.iter() if e.get("data-seat-number") is not None)

def test_absent_conflicting_or_seat_contained_headers_are_not_assigned():
    for words in ([],[w("FLOOR"),w("BALCONY",50)], [w("FLOOR",12,12)]):
        p=pg();p["words"]=words
        m=extract_pages([p],"h",section_terms=["FLOOR","BALCONY"])
        assert not m["sections"]
        assert all(s["section_id"] is None for s in m["seats"])

def test_no_terms_means_no_automatic_section_inference():
    p=pg();p["words"]=[w("FLOOR")]
    assert not extract_pages([p],"h")["sections"]

def test_source_line_coordinates_preserved():
    p=pg();p["lines"]=[{"points":[[1,1],[90,1]]}]
    m=extract_pages([p],"h")
    assert m["decor"][0]["points"]==[[1,1],[90,1]]
