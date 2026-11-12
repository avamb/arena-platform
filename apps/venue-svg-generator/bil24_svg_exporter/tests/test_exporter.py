import copy
import json
import xml.etree.ElementTree as ET
import pytest
from bil24_svg_exporter.core import render, digest, SVG, INK
from bil24_svg_exporter.cli import main

@pytest.fixture
def inputs():
    model={"schema_version":"seating-json-v1","source_only":True,"reference_svg_used":False,
        "source":{"sha256":"a"*64},"validation":{"status":"QUARANTINE"},"seats":[
        {"seat_key":"a","row_label":"1","seat_number":2,"section_id":None,"status":"QUARANTINE","source_coordinate":{"x":200,"y":300}},
        {"seat_key":"b","row_label":"1","seat_number":1,"section_id":None,"status":"QUARANTINE","source_coordinate":{"x":240,"y":300}}]}
    profile={"schema":"bil24-presentation-v1","test_only":True,"name":"Example",
        "section_label":"Parter","section_evidence":"Owner configuration","width":1200,"height":1100,
        "seat_radius":11,"transform":{"scale_x":1,"scale_y":1,"translate_x":0,"translate_y":0},
        "stage":{"x":300,"y":60,"width":600,"height":140,"label":"STAGE","evidence":"Owner decoration"},
        "category":{"label":"TEST_ONLY","color":"#ffff00","price_text":"%price%"},
        "section_y":245,"row_label_left":140,"row_label_right":1060}
    review={"schema":"bil24-export-review-v1","model_sha256":digest(model),"source_sha256":"a"*64,
        "verdict":"PASS","evidence":"Independent review","approved_identities":[["1","2"],["1","1"]]}
    return model,profile,review

def test_one_document_preserves_seats_and_source(inputs):
    model,profile,review=inputs
    before=copy.deepcopy(model)
    data,report,provenance=render(*inputs)
    root=ET.fromstring(data)
    assert model==before and report["seats"]==2
    assert root.attrib["width"]=="1200" and root.attrib["viewBox"]=="0 0 1200 1100"
    assert root.find("{*}g[@id='PriceCategory']") is not None
    assert root.find("{*}g[@id='PriceCategory']/{*}text[@id='PriceCategoryText']/{*}tspan").text=="%price%"
    assert root.find("{*}g[@id='Legend']") is not None
    assert provenance["input_validation"]["status"]=="QUARANTINE"
    assert provenance["bil24_import"]=="NOT_TESTED"
    seats=root.findall(".//{*}circle[@data-source-key]")
    assert len(seats)==2 and [s.findtext("{*}title") for s in seats]==["2","1"]
    assert all(s.attrib["stroke-width"]=="3" for s in seats)
    assert len(root.findall(".//{*}circle"))==7 # four status swatches plus one category

@pytest.mark.parametrize("defect",["no_review","hash","missing_identity","unknown_number","duplicate","unknown_status","commercial","overlap","outside","nan","canvas"])
def test_fail_closed(inputs,defect):
    model,profile,review=inputs
    if defect=="no_review": review={}
    elif defect=="hash": review["model_sha256"]="b"*64
    elif defect=="missing_identity": review["approved_identities"].pop()
    elif defect=="unknown_number": model["seats"][0]["seat_number"]=None
    elif defect=="duplicate": model["seats"][1]["seat_number"]=2
    elif defect=="unknown_status": model["seats"][0]["status"]="UNKNOWN"
    elif defect=="commercial": profile["category"]["price_text"]="EUR 60"
    elif defect=="overlap": model["seats"][1]["source_coordinate"]["x"]=210
    elif defect=="outside": profile["transform"]["translate_x"]=-500
    elif defect=="nan": profile["seat_radius"]=float("nan")
    elif defect=="canvas": profile["width"]=2001
    if defect in {"unknown_number","duplicate","unknown_status","overlap"}: review["model_sha256"]=digest(model)
    with pytest.raises(ValueError): render(model,profile,review)

def cli_fixture(inputs):
    model,profile,review=copy.deepcopy(inputs)
    model["seats"]=[{"seat_key":f"test-{row}-{number}","row_label":str(row),"seat_number":number,
        "section_id":None,"status":"QUARANTINE","source_coordinate":{"x":200+(number-1)*30,"y":300+(row-1)*40}}
        for row in range(1,6) for number in range(1,21)]
    review["model_sha256"]=digest(model)
    review["approved_identities"]=[[s["row_label"],str(s["seat_number"])] for s in model["seats"]]
    return model,profile,review

def test_cli_fresh_outputs_and_failure(inputs,tmp_path):
    inputs=cli_fixture(inputs)
    paths=[]
    for name,value in zip(["model","profile","review"],inputs):
        p=tmp_path/(name+".json");p.write_text(json.dumps(value),encoding="utf-8");paths.append(p)
    def args(out): return [str(paths[0]),"--profile",str(paths[1]),"--review",str(paths[2]),"--output-dir",str(out)]
    out=tmp_path/"run"
    assert main(args(out))==0
    svg=(out/"authoring.svg").read_bytes()
    assert main(args(out))==1 and (out/"authoring.svg").read_bytes()==svg
    bad=tmp_path/"bad"
    paths[2].write_text("{}",encoding="utf-8")
    assert main(args(bad))==1 and not bad.exists()

def test_deterministic_and_reversible(inputs):
    first=render(*inputs)
    assert first==render(*inputs)
    assert first[1]["coordinate_mismatches"]==0

@pytest.mark.parametrize("malformed",[[],None,"wrong"])
def test_object_contract(inputs,malformed):
    with pytest.raises(ValueError): render(malformed,inputs[1],inputs[2])

def test_decor_must_not_overlap_seats(inputs):
    model,profile,review=inputs
    profile["stage"].update({"x":190,"y":290,"width":100,"height":100})
    with pytest.raises(ValueError,match="stage"): render(model,profile,review)

def test_visible_rows_must_not_overlap_seats(inputs):
    inputs[1]["row_label_left"]=200
    with pytest.raises(ValueError,match="Row labels"): render(*inputs)

def test_uniform_rows_preserve_ids_and_make_equal_gaps(inputs):
    model,profile,review=inputs
    before=copy.deepcopy(model)
    profile["seat_stroke_width"]=1
    profile["layout"]={"mode":"uniform_rows","seat_pitch":32,"contiguous_rows_confirmed":True,"evidence":"Source inspected"}
    data,report,provenance=render(model,profile,review)
    seats=ET.fromstring(data).findall(".//{*}circle[@data-source-key]")
    assert [s.findtext("{*}title") for s in seats]==["2","1"]
    assert [float(s.attrib["cx"]) for s in seats]==[204,236]
    assert all(s.attrib["stroke-width"]=="1" for s in seats)
    assert report["row_visible_gaps"]["1"]=={"minimum":9,"maximum":9}
    assert model==before
    assert provenance["presentation_layout"]["mode"]=="uniform_rows"

@pytest.mark.parametrize("defect",["gap","unconfirmed","ambiguous","wrong_mode"])
def test_uniform_layout_refuses_unconfirmed_structure(inputs,defect):
    model,profile,review=inputs
    profile["layout"]={"mode":"uniform_rows","seat_pitch":32,"contiguous_rows_confirmed":True,"evidence":"Source inspected"}
    if defect=="gap": model["gaps"]=[{"cell":"aisle"}]
    elif defect=="unconfirmed": profile["layout"]["contiguous_rows_confirmed"]=False
    elif defect=="ambiguous": model["seats"][1]["source_coordinate"]["x"]=200
    else: profile["layout"]["mode"]="guess"
    review["model_sha256"]=digest(model)
    with pytest.raises(ValueError): render(model,profile,review)

def test_shared_style_is_generic_and_does_not_change_inputs(inputs):
    from bil24_svg_exporter.profiles import uniform_contiguous_rows
    model,profile,review=inputs
    before=copy.deepcopy(profile)
    configured=uniform_contiguous_rows(profile,"Source inspected: no aisles")
    assert profile==before
    assert configured["layout"]["seat_pitch"]==32
    data,report,_=render(model,configured,review)
    assert report["row_visible_gaps"]["1"]=={"minimum":9,"maximum":9}
    assert all(s.attrib["stroke-width"]=="1" for s in ET.fromstring(data).findall(".//{*}circle[@data-source-key]"))
    wider=uniform_contiguous_rows(profile,"Source inspected",visible_gap=12,stroke_width=0.5)
    assert wider["layout"]["seat_pitch"]==34.5
    with pytest.raises(ValueError): uniform_contiguous_rows(profile,"")

def test_cli_enables_shared_style_without_editing_profile(inputs,tmp_path):
    inputs=cli_fixture(inputs)
    paths=[]
    for name,value in zip(["model","profile","review"],inputs):
        p=tmp_path/(name+".json");p.write_text(json.dumps(value),encoding="utf-8");paths.append(p)
    original_profile=paths[1].read_bytes()
    out=tmp_path/"uniform-run"
    args=[str(paths[0]),"--profile",str(paths[1]),"--review",str(paths[2]),"--output-dir",str(out),
          "--uniform-rows-evidence","Verified source has contiguous rows"]
    assert main(args)==0
    summary=json.loads((out/"validation-summary.json").read_text(encoding="utf-8"))
    assert summary["row_visible_gaps"]["1"]=={"minimum":9,"maximum":9}
    assert paths[1].read_bytes()==original_profile

def test_arena_row_structure_and_service_label_isolation(inputs):
    data,report,_=render(*inputs)
    root=ET.fromstring(data)
    rows=[g for g in root.findall("{*}g") if g.attrib.get("{"+INK+"}label")=="#Parter"]
    assert all(all(ch.tag in {"{"+SVG+"}title","{"+SVG+"}circle"} for ch in row) for row in rows)
    assert len(root.findall("{*}g[@id='SeatNumberLabels']/{*}text"))==2
    assert root.find("{*}g[@id='PriceCategory']/{*}text[@id='PriceCategoryText']") is not None
    assert root.find("{*}g[@id='Legend']/{*}text[@id='LegendText']") is not None
    # Match actual ImportSVG discovery: # labels outside service subtrees are rows.
    discovered=[]
    def walk(node):
        if node.attrib.get("id") in {"PriceCategory","Legend"}: return
        if node.attrib.get("{"+INK+"}label","").startswith("#"):
            discovered.append(node);return
        for child in node: walk(child)
    walk(root)
    assert discovered==rows

def test_seat_numbers_paint_after_circles_in_standalone_svg(inputs):
    data,_,_=render(*inputs)
    children=list(ET.fromstring(data))
    number_index=next(i for i,n in enumerate(children) if n.attrib.get("id")=="SeatNumberLabels")
    row_indices=[i for i,n in enumerate(children) if n.attrib.get("{"+INK+"}label")=="#Parter"]
    assert number_index>max(row_indices)

def test_bil24_legend_matches_accepted_service_representation(inputs):
    data,_,_=render(*inputs)
    root=ET.fromstring(data)
    legend=root.find("{*}g[@id='Legend']")
    assert [n.attrib.get("id") for n in list(legend)[:4]]==["Sold","None","Reserved","MyTickets"]
    assert legend.find("{*}text[@id='LegendText']").attrib["{"+INK+"}label"]=="#LegendText"
    for circle in legend.findall("{*}circle"):
        style=dict(declaration.split(":",1) for declaration in circle.attrib["style"].split(";"))
        assert style["fill"]==circle.attrib["fill"]
        assert style["stroke-width"]==circle.attrib["stroke-width"]
    for circle in root.findall(".//{*}circle"):
        assert "fill:"+circle.attrib["fill"] in circle.attrib["style"]

def test_missing_bil24_inline_legend_fill_rejected(inputs):
    from bil24_svg_exporter.core import validate
    data,_,_=render(*inputs)
    root=ET.fromstring(data)
    root.find("{*}g[@id='Legend']/{*}circle[@id='None']").attrib.pop("style")
    with pytest.raises(ValueError,match="inline fill"):
        validate(ET.tostring(root),inputs[0],inputs[1])

def test_compact_export_preserves_functional_identity_and_external_trace(inputs):
    from bil24_svg_exporter.compact import compact_authoring
    from bil24_svg_exporter.core import validate
    data,_,_=render(*inputs)
    compact,mapping=compact_authoring(data)
    assert len(compact)<len(data)
    assert validate(compact,inputs[0],inputs[1])["seats"]==2
    assert [m["source_key"] for m in mapping]==["a","b"]
    assert mapping[0]["source_coordinate"]=={"x":200,"y":300}
    root=ET.fromstring(compact)
    row=[n for n in root.findall("{*}g") if n.attrib.get("{"+INK+"}label")=="#Parter"][0]
    assert [n.findtext("{*}title") for n in row.findall("{*}circle")]==["2","1"]
    assert all(not any(k.startswith("data-") for k in n.attrib) for n in root.iter())
    assert compact==compact_authoring(data)[0]

def test_compact_size_gate_before_publication(inputs,tmp_path):
    from bil24_svg_exporter.compact import check_input_budget
    assert check_input_budget(b"x"*500,2)["input_bytes_per_seat"]==250
    with pytest.raises(ValueError): check_input_budget(b"x"*501,2)
    paths=[]
    for name,value in zip(["model","profile","review"],inputs):
        path=tmp_path/(name+".json");path.write_text(json.dumps(value),encoding="utf-8");paths.append(path)
    out=tmp_path/"too-large"
    assert main([str(paths[0]),"--profile",str(paths[1]),"--review",str(paths[2]),"--output-dir",str(out)])==1
    assert not out.exists()


def test_unpriced_category_slots(inputs):
    from bil24_svg_exporter.compact import compact_authoring
    from bil24_svg_exporter.core import validate
    model,profile,review=inputs
    profile.update(category_template="bil24-unpriced-slots-v1",category_template_evidence="Owner-approved unpriced template",default_category="Slot3")
    profile["category_palette"]=[{"label":f"Slot{i}","color":f"#{i:06x}","price_text":"%price%"} for i in range(1,16)]
    before=copy.deepcopy(model)
    raw,summary,provenance=render(*inputs)
    compact,mapping=compact_authoring(raw)
    report=validate(compact,model,profile)
    assert report["categories"]==15 and report["category_seat_counts"]["Slot3"]==2
    assert sum(report["category_seat_counts"].values())==2 and not report["prices_assigned"]
    assert model==before and len(mapping)==2
    assert provenance["category_template"]["prices_assigned"] is False
    profile["category_palette"][0]["price_text"]="0"
    with pytest.raises(ValueError,match="unpriced"): render(*inputs)

@pytest.mark.parametrize("defect",["duplicate_color","duplicate_name","missing_default","too_many","price_field"])
def test_invalid_category_palette(inputs,defect):
    model,profile,review=inputs
    profile.update(category_template="bil24-unpriced-slots-v1",category_template_evidence="Owner",default_category="First")
    profile["category_palette"]=[{"label":"First","color":"#ff0000","price_text":"%price%"},{"label":"Second","color":"#ffff00","price_text":"%price%"}]
    if defect=="duplicate_color": profile["category_palette"][1]["color"]="#ff0000"
    if defect=="duplicate_name": profile["category_palette"][1]["label"]="First"
    if defect=="missing_default": profile["default_category"]="Unknown"
    if defect=="too_many": profile["category_palette"]*=8
    if defect=="price_field": profile["category_palette"][0]["price"]=0
    with pytest.raises(ValueError): render(*inputs)


def test_explicit_presentation_preserves_source_and_requires_evidence(inputs):
    from bil24_svg_exporter.core import _positions
    model,profile,review=inputs
    before=copy.deepcopy(model)
    profile["presentation_overrides"]={"a":{"x":205,"y":301}}
    with pytest.raises(ValueError,match="evidence"):_positions(model,profile)
    profile["presentation_override_evidence"]="Explicit source comment, presentation only"
    assert _positions(model,profile)["a"]==(205,301) and model==before
    profile["presentation_overrides"]["other"]={"x":1,"y":1}
    with pytest.raises(ValueError,match="Unknown source key"):_positions(model,profile)

def test_multi_section_export(inputs):
    from bil24_svg_exporter.multi_section import render_sections
    first=list(cli_fixture(inputs))
    second=copy.deepcopy(first)
    second[1]["section_label"]="Balcony"
    for seat in second[0]["seats"]:
        seat["seat_key"]="balcony-"+seat["seat_key"];seat["source_coordinate"]["y"]+=240
    second[2]["model_sha256"]=digest(second[0])
    data,report,provenance=render_sections([first,second])
    assert report["seats"]==200 and report["sections"]==2 and report["rows"]==10
    assert len(provenance["seat_element_mapping"])==200
    with pytest.raises(ValueError,match="Duplicate section"):render_sections([first,first])


def test_compact_outline_inherits_without_losing_explicit_category_fill(inputs):
    from bil24_svg_exporter.compact import compact_authoring
    data,mapping=compact_authoring(render(*inputs)[0])
    tree=ET.fromstring(data)
    row=next(g for g in tree.findall("{*}g") if g.get("{"+INK+"}label")=="#Parter")
    assert "stroke:#001505" in row.get("style") and "stroke-width:3" in row.get("style")
    assert all(c.get("style")=="fill:#ffff00" for c in row.findall("{*}circle"))


def test_render_regularization_preserves_model_and_validates(inputs):
    from bil24_svg_exporter.core import render,digest
    model,profile,review=inputs
    model["seats"].append({"seat_key":"c","row_label":"1","seat_number":3,"section_id":None,"status":"VALID","source_coordinate":{"x":280,"y":301}})
    review["model_sha256"]=digest(model)
    review["approved_identities"].append(["1","3"])
    profile["geometry_regularization"]={"evidence":"confirmed source straight segment","segments":[{"kind":"line","keys":["a","b","c"]}]}
    before=copy.deepcopy(model)
    _,report,provenance=render(model,profile,review)
    assert model==before and report["seats"]==3
    assert provenance["geometry_regularization"][0]["status"]=="APPLIED"


def test_service_markers_are_decor_not_functional_seats(inputs):
    from bil24_svg_exporter.nonbookable import append_nonbookable
    from bil24_svg_exporter.compact import compact_authoring
    from bil24_svg_exporter.core import validate
    model,profile,review=cli_fixture(inputs)
    raw,_,_=render(model,profile,review);raw,_=compact_authoring(raw)
    decision={"schema":"organizer-decision-v1","status":"NONBOOKABLE_DISPLAY_ALLOWED","role":"service","bookable":False,"visible":True,"source_sha256":model["source"]["sha256"],"region_ids":["A"],"evidence_id":"owner","certainty":"tentative"}
    markers=[{"id":"service-A-1","region_id":"A","seat_number":None,"source_coordinate":{"x":1000,"y":800}}]
    data,report=append_nonbookable(raw,model,profile,markers,decision)
    tree=ET.fromstring(data);group=tree.find("{*}g[@id='ServiceMarkers']")
    assert group is not None and not group.findall(".//{*}circle")
    assert group.find("{*}title") is None and group.get("{"+INK+"}label") is None
    assert validate(data,model,profile)["seats"]==100
    assert report["functional_seats_added"]==0 and report["nonbookable_markers"][0]["bookable"] is False
    bad=copy.deepcopy(decision);bad["bookable"]=True
    with pytest.raises(ValueError):append_nonbookable(raw,model,profile,markers,bad)
    markers[0]["seat_number"]="invented"
    with pytest.raises(ValueError):append_nonbookable(raw,model,profile,markers,decision)


def test_table_pipeline_exports_separate_section_inventory(inputs,tmp_path,monkeypatch):
    from venue_generator.table_pipeline import generate
    model,presentation,review=cli_fixture(inputs)
    model["validation"]={"status":"SOURCE_COVERAGE_PASS"}
    for i,seat in enumerate(model["seats"]):seat["section_id"]="Parter" if i<50 else "Balcony"
    monkeypatch.setattr("venue_generator.table_pipeline.extract",lambda profile:copy.deepcopy(model))
    profile={"source_review":{"verdict":"PASS","source_sha256":model["source"]["sha256"],"evidence":"Independent synthetic fixture","semantics":{"status":"PASS","unresolved":[]}},
             "presentation":presentation,"section_profiles":{"Balcony":{"section_evidence":"Source section Balcony","section_y":560}}}
    report=generate(profile,tmp_path/"out")
    assert report["seats"]==100 and report["sections"]==2
    tree=ET.fromstring((tmp_path/"out/venue_Arena.svg").read_bytes())
    labels={g.get("{"+INK+"}label") for g in tree.findall("{*}g") if g.find("{*}title") is not None}
    assert labels=={"#Parter","#Balcony"}


def test_explicit_row_label_positions_require_evidence_and_safe_locations(inputs):
    model,profile,review=cli_fixture(inputs)
    from bil24_svg_exporter.core import render,_positions
    coords=_positions(model,profile)
    rows={str(seat["row_label"]) for seat in model["seats"]}
    profile["row_label_positions"]={row:[[profile["row_label_left"],sum(coords[s["seat_key"]][1] for s in model["seats"] if str(s["row_label"])==row)/sum(str(s["row_label"])==row for s in model["seats"])+7],[profile["row_label_right"],sum(coords[s["seat_key"]][1] for s in model["seats"] if str(s["row_label"])==row)/sum(str(s["row_label"])==row for s in model["seats"])+7]] for row in rows}
    with pytest.raises(ValueError,match="evidence"):render(model,profile,review)
    profile["row_label_position_evidence"]="Reviewed source row endpoints"
    render(model,profile,review)
    profile["row_label_positions"][next(iter(rows))][0]=list(next(iter(coords.values())))
    with pytest.raises(ValueError,match="overlaps"):render(model,profile,review)


def test_sales_state_cannot_be_claimed_by_svg_geometry_alone(inputs):
    model,profile,review=cli_fixture(inputs)
    model["organizer_decision"]={"event_intent":"carry_source_sales"}
    review["model_sha256"]=digest(model)
    with pytest.raises(ValueError,match="editable target event-state"):render(model,profile,review)


@pytest.mark.parametrize("sides",[[],["left"],["right"]])
def test_explicit_row_label_visibility_preserves_inventory(inputs,sides):
    model,profile,review=inputs
    profile["visible_row_label_sides"]=sides
    with pytest.raises(ValueError,match="visibility requires evidence"):
        render(model,profile,review)
    profile["row_label_visibility_evidence"]="Adjacent sections share continuous rows: captions at outer ends only."
    raw,_report,_prov=render(model,profile,review)
    tree=ET.fromstring(raw)
    assert len(tree.find("{*}g[@id='RowLabels']"))==len(sides)
    row=tree.find("{*}g[{*}title]")
    assert sorted(c.findtext("{*}title") for c in row.findall("{*}circle"))==["1","2"]

@pytest.mark.parametrize("sides",[["left","left"],["center"],"left",None])
def test_invalid_row_label_visibility_rejected(inputs,sides):
    model,profile,review=inputs
    profile.update(visible_row_label_sides=sides,row_label_visibility_evidence="Reviewed")
    with pytest.raises(ValueError,match="Invalid visible row label sides"):
        render(model,profile,review)
