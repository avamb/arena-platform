from pathlib import Path
from types import SimpleNamespace
import hashlib,json,copy
import pytest
from venue_generator.table_pipeline import extract_pdf,generate,main

def sample(tmp_path,monkeypatch,orphan=False):
    source=tmp_path/"source.pdf";source.write_bytes(b"synthetic locally mocked PDF")
    words=[{"text":"7","x0":-15,"x1":-11,"top":3,"bottom":8},
           {"text":"2","x0":5,"x1":9,"top":3,"bottom":8},
           {"text":"8","x0":20,"x1":24,"top":3,"bottom":8}]
    if orphan:words.append({"text":"9","x0":40,"x1":44,"top":3,"bottom":8})
    page=SimpleNamespace(rects=[{"x0":0,"x1":30,"top":0,"bottom":12},{"x0":15,"x1":16,"top":0,"bottom":12}],extract_words=lambda **kw:copy.deepcopy(words))
    class Document:
        pages=[page]
        def __enter__(self):return self
        def __exit__(self,*args):pass
    monkeypatch.setattr("venue_generator.table_pipeline.pdfplumber.open",lambda _:Document())
    return {"source":str(source),"source_sha256":hashlib.sha256(source.read_bytes()).hexdigest(),"section_label":"Hall",
            "cell_parameters":{"container_edges":True},"row_label_roi":[-20,0,-10,12],"seat_roi":[0,0,50,12],"row_label_tolerance":2,
            "source_review":{"verdict":"PASS","source_sha256":hashlib.sha256(source.read_bytes()).hexdigest(),"evidence":"test glyphs"}}

def test_glyphs_preserved_no_sequence_filling(tmp_path,monkeypatch):
    model=extract_pdf(sample(tmp_path,monkeypatch))
    assert [s["seat_number"] for s in model["seats"]]==["2","8"]
    assert {s["row_label"] for s in model["seats"]}=={"7"}
    assert model["validation"]["status"]=="SOURCE_COVERAGE_PASS"

def test_orphan_number_blocks_export(tmp_path,monkeypatch):
    profile=sample(tmp_path,monkeypatch,True);out=tmp_path/"output"
    assert generate(profile,out)["status"]=="INCOMPLETE"
    assert not list(out.glob("*.svg"))

def test_missing_semantic_review_blocks_export(tmp_path,monkeypatch):
    profile=sample(tmp_path,monkeypatch);out=tmp_path/"output"
    assert generate(profile,out)["status"]=="INCOMPLETE"
    assert not list(out.glob("*.svg"))
    model=json.loads((out/"seating.json").read_text())
    assert all(seat["status"]=="QUARANTINE" for seat in model["seats"])

def test_existing_package_not_overwritten_on_failure(tmp_path,monkeypatch):
    profile=sample(tmp_path,monkeypatch);file=tmp_path/"profile.json";file.write_text(json.dumps(profile))
    out=tmp_path/"output";out.mkdir();old=out/"validation-summary.json";old.write_text("original")
    assert main([str(file),"--output-dir",str(out)])==1
    assert old.read_text()=="original"


def raster_sample(tmp_path,blank=False):
    import cv2,numpy as np
    image=np.full((70,100,3),255,dtype=np.uint8)
    if not blank:
        cv2.putText(image,"2",(12,30),cv2.FONT_HERSHEY_SIMPLEX,.6,(0,0,0),1)
        cv2.putText(image,"8",(52,30),cv2.FONT_HERSHEY_SIMPLEX,.6,(0,0,0),1)
    source=tmp_path/"source.png";cv2.imwrite(str(source),image)
    sha=hashlib.sha256(source.read_bytes()).hexdigest()
    return {"source":str(source),"source_sha256":sha,"section_label":"Hall",
            "printed_cell_annotation":{"source_sha256":sha,"review_verdict":"PASS","evidence":"independent synthetic source glyph review",
                "columns":[[5,40],[45,80]],"rows":[{"label":"7","top":5,"bottom":50,"printed_numbers":["2","8"]}]}}

def test_raster_annotation_is_honest_and_source_bound(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    model=extract_raster(raster_sample(tmp_path))
    assert [s["seat_number"] for s in model["seats"]]==["2","8"]
    assert all(s["provenance"]["automatic_ocr_verified"] is False for s in model["seats"])
    assert model["validation"]["status"]=="SOURCE_COVERAGE_PASS"

def test_empty_raster_cells_not_published_as_labelled_seats(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    model=extract_raster(raster_sample(tmp_path,True))
    assert model["validation"]["status"]=="INCOMPLETE" and not model["seats"]

def test_changed_source_or_missing_annotation_review_rejected(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    profile=raster_sample(tmp_path);profile["printed_cell_annotation"]["source_sha256"]="0"*64
    with pytest.raises(ValueError):extract_raster(profile)


def test_reviewed_short_row_must_keep_its_outer_source_edge(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    profile=raster_sample(tmp_path)
    import cv2
    image=cv2.imread(profile["source"])
    cv2.putText(image,"8",(52,60),cv2.FONT_HERSHEY_SIMPLEX,.6,(0,0,0),1)
    cv2.imwrite(profile["source"],image)
    sha=hashlib.sha256(Path(profile["source"]).read_bytes()).hexdigest()
    profile["source_sha256"]=sha
    annotation=profile["printed_cell_annotation"]
    annotation["source_sha256"]=sha
    annotation["rows"]=[
        {"label":"15","top":5,"bottom":35,"printed_numbers":["2","8"]},
        {"label":"16","top":35,"bottom":69,"printed_numbers":["8"],"columns":[[45,80]]},
    ]
    annotation["row_edge_alignment"]= [{"row_label":"16","reference_row_label":"15","anchor":"outer_right","target_count":1}]
    assert extract_raster(profile)["validation"]["status"]=="SOURCE_COVERAGE_PASS"
    annotation["rows"][1]["columns"]=[[5,40]]
    with pytest.raises(ValueError,match="outer edge"):
        extract_raster(profile)


def header_sample(tmp_path,colour=(255,255,0)):
    import cv2,numpy as np
    image=np.full((100,70,3),255,dtype=np.uint8)
    cv2.rectangle(image,(10,50),(40,80),colour,-1)
    cv2.putText(image,"7",(17,30),cv2.FONT_HERSHEY_SIMPLEX,.6,(0,0,0),1)
    source=tmp_path/"headers.png";cv2.imwrite(str(source),image)
    sha=hashlib.sha256(source.read_bytes()).hexdigest()
    return dict(source=str(source),source_sha256=sha,section_label="B",printed_cell_annotation=dict(source_sha256=sha,review_verdict="PASS",evidence="Reviewed source column",column_header_evidence="Printed7 above cyan column",columns=[[10,40]],rows=[dict(label="B2",top=50,bottom=80,printed_numbers=["7"],identity_mode="column_header",number_header_bboxes=[[10,5,40,40]])]))

def test_column_header_rule_preserves_identity_and_evidence(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    model=extract_raster(header_sample(tmp_path))
    assert model["validation"]["status"]=="SOURCE_COVERAGE_PASS"
    assert model["seats"][0]["seat_number"]=="7"
    assert model["seats"][0]["provenance"]["number_header_bbox"]==[10,5,40,40]
    assert model["seats"][0]["provenance"]["identity_origin"]=="independently_reviewed_source_column_header"

@pytest.mark.parametrize("colour",[(255,255,255),(255,0,255)])
def test_white_or_magenta_not_cyan_seats(tmp_path,colour):
    from venue_generator.table_pipeline import extract_raster
    model=extract_raster(header_sample(tmp_path,colour))
    assert model["validation"]["status"]=="INCOMPLETE" and not model["seats"]

def test_column_header_requires_review_and_alignment(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    profile=header_sample(tmp_path);profile["printed_cell_annotation"].pop("column_header_evidence")
    with pytest.raises(ValueError,match="evidence"):extract_raster(profile)
    profile=header_sample(tmp_path);profile["printed_cell_annotation"]["rows"][0]["number_header_bboxes"]=[[20,5,50,40]]
    with pytest.raises(ValueError,match="align"):extract_raster(profile)


@pytest.mark.parametrize("value",[True,0,231,255,float("nan")])
def test_source_ink_threshold_cannot_accept_white_background(tmp_path,value):
    from venue_generator.table_pipeline import extract_raster
    profile=raster_sample(tmp_path,blank=True);profile["printed_cell_annotation"]["ink_threshold"]=value
    with pytest.raises(ValueError,match="threshold"):extract_raster(profile)

def test_reviewed_gray_ink_does_not_approve_white_blanks(tmp_path):
    from venue_generator.table_pipeline import extract_raster
    profile=raster_sample(tmp_path,blank=True);profile["printed_cell_annotation"]["ink_threshold"]=220
    assert extract_raster(profile)["validation"]["status"]=="INCOMPLETE"
