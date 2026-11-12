import hashlib
from types import SimpleNamespace
import pytest
from venue_generator.pdf_profiles import extract_profiled_pdf

class FakePDF:
    def __init__(self,page):self.pages=[page]
    def __enter__(self):return self
    def __exit__(self,*args):pass

@pytest.fixture
def vector_fixture(tmp_path,monkeypatch):
    source=tmp_path/"source.pdf";source.write_bytes(b"private test fixture")
    words=[dict(text="1",x0=0,x1=3,top=12,bottom=17),dict(text="7",x0=12,x1=17,top=12,bottom=17)]
    page=SimpleNamespace(rects=[dict(x0=10,x1=20,top=10,bottom=20)],curves=[],extract_words=lambda **kwargs:words)
    monkeypatch.setattr("venue_generator.pdf_profiles.pdfplumber.open",lambda path:FakePDF(page))
    region=dict(section="Hall",shape_kinds=["rects"],width_range=[9,11],height_range=[9,11],seat_roi=[9,9,21,21],row_label_roi=[0,9,5,21],row_label_tolerance=2)
    return dict(source=str(source),source_sha256=hashlib.sha256(source.read_bytes()).hexdigest(),regions=[region]),page,words

def test_vector_primitive_and_printed_identity(vector_fixture):
    profile,page,words=vector_fixture
    model=extract_profiled_pdf(profile)
    assert model["validation"]["status"]=="SOURCE_COVERAGE_PASS"
    assert [(s["row_label"],s["seat_number"]) for s in model["seats"]]==[("1","7")]
    assert model["seats"][0]["source_coordinate"]==dict(x=15,y=15)

@pytest.mark.parametrize("missing",["seat_number","row","shape"])
def test_vector_missing_evidence_cannot_pass(vector_fixture,missing):
    profile,page,words=vector_fixture
    if missing=="seat_number":words.pop()
    elif missing=="row":words.pop(0)
    else:page.rects=[]
    model=extract_profiled_pdf(profile)
    assert model["validation"]["status"]=="INCOMPLETE" and model["quarantine"]

def test_vector_overlapping_regions_rejected(vector_fixture):
    profile,page,words=vector_fixture;profile["regions"]*=2
    with pytest.raises(ValueError,match="overlapping regions"):extract_profiled_pdf(profile)

def test_vector_source_hash_required(vector_fixture):
    profile,page,words=vector_fixture;profile["source_sha256"]="changed"
    with pytest.raises(ValueError,match="hash"):extract_profiled_pdf(profile)

def test_vector_nested_matching_shapes_rejected(vector_fixture):
    profile,page,words=vector_fixture;page.rects.append(dict(x0=10,x1=19,top=10,bottom=20))
    with pytest.raises(ValueError,match="multiple source shapes"):extract_profiled_pdf(profile)
