import json
from pathlib import Path
import pytest
from pdf_seating_adapter import extract_pages
from pdf_seating_adapter.cli import main

def pg(number=1):
    return {"page_number":number,"width":100,"height":100,
            "rects":[{"x0":10+i*20,"x1":20+i*20,"top":10+i*.75,"bottom":20+i*.75} for i in range(3)], "words":[]}

def test_page_numbers_are_unique_and_positive():
    for pages in ([pg(1),pg(1)],[pg(0)],[pg(-1)]):
        with pytest.raises(ValueError): extract_pages(pages,"hash")

def test_band_tolerance_does_not_chain():
    model=extract_pages([pg()],"hash")
    assert [r["seat_count"] for r in model["rows"]]==[2,1]

def test_tied_mode_retains_candidate_evidence():
    p=pg(); p["rects"]=p["rects"][:2]+[{"x0":60+i*10,"x1":65+i*10,"top":10,"bottom":15} for i in range(2)]
    model=extract_pages([p],"hash")
    assert len(model["rejected_cells"])==4
    assert not model["seats"]
    assert all("evidence" in r for r in model["rejected_cells"])

def test_corrupt_cli_reports_failure(tmp_path):
    source=tmp_path/"bad.pdf"; source.write_bytes(b"not a pdf")
    out=tmp_path/"run"
    assert main([str(source),"--output-dir",str(out)])==1
    assert json.loads((out/"validation-summary.json").read_text())["status"]=="FAIL"
    assert json.loads((out/"manifest.json").read_text())["status"]=="INCOMPLETE"
    assert json.loads((out/"provenance.json").read_text())["source_only"] is True
    assert not (out/"diagnostic.svg").exists()

def test_existing_directory_never_overwritten(tmp_path):
    out=tmp_path/"existing"; out.mkdir(); marker=out/"user.txt"; marker.write_text("keep")
    assert main(["missing.pdf","--output-dir",str(out)])==1
    assert marker.read_text()=="keep"
    assert list(out.iterdir())==[marker]


def test_diagnostic_has_white_background(tmp_path):
    import xml.etree.ElementTree as ET
    from pdf_seating_adapter import render_diagnostic
    model=extract_pages([pg()],"hash")
    out=tmp_path/"diag.svg"
    render_diagnostic(model,out)
    root=ET.parse(out).getroot()
    bg=next(e for e in root.iter() if e.get("data-role")=="background")
    assert bg.get("fill")=="white"
    assert bg.get("data-seat-number") is None
