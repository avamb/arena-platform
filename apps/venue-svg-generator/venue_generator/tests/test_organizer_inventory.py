import copy,xml.etree.ElementTree as ET
import pytest
from venue_generator.organizer_inventory import apply_numbering,append_unavailable
from bil24_svg_exporter.core import SVG,INK

def fixture():
    model={"source":{"sha256":"a"*64},"seats":[{"seat_key":str(i),"section_id":"Hall","row_label":"1","seat_number":None,"source_coordinate":{"x":50+i*40,"y":100},"source_appearance":"gray" if i==0 else "colored"} for i in range(2)]}
    decision={"schema":"organizer-numbering-decision-v1","source_sha256":"a"*64,"evidence_id":"owner","original_answer":"Number from right; gray unavailable","confirmed":True,"event_intent":"carry_source_sales","gray_bookable":False,"numbering":{"start":1,"count_per_row":2,"direction":"right_to_left","across_aisle":True}}
    return model,decision

def test_numbering_includes_unavailable_positions_without_mutating_source():
    model,decision=fixture();result=apply_numbering(model,decision)
    assert [s["seat_number"] for s in result["seats"]]==["2","1"]
    assert [s["status"] for s in result["seats"]]==["UNAVAILABLE","VALID"]
    assert all(s["seat_number"] is None for s in model["seats"])

@pytest.mark.parametrize("field,value",[("confirmed",False),("source_sha256","b"*64),("gray_bookable",True),("original_answer","")])
def test_missing_owner_authority_cannot_activate_inventory(field,value):
    model,decision=fixture();decision[field]=value
    with pytest.raises(ValueError):apply_numbering(model,decision)

def test_incomplete_source_row_is_not_filled():
    model,decision=fixture();model["seats"].pop()
    with pytest.raises(ValueError,match="never fill"):apply_numbering(model,decision)

def test_existing_numbers_not_overwritten():
    model,decision=fixture();model["seats"][0]["seat_number"]="source"
    with pytest.raises(ValueError,match="overwritten"):apply_numbering(model,decision)

def test_duplicate_source_coordinate_rejected():
    model,decision=fixture();model["seats"][1]["source_coordinate"]=copy.deepcopy(model["seats"][0]["source_coordinate"])
    with pytest.raises(ValueError):apply_numbering(model,decision)

def test_unavailable_labels_painted_after_shapes_and_never_functional(monkeypatch):
    model,decision=fixture();decision["output_purpose"]="static_preview_only";model=apply_numbering(model,decision);ns="{"+SVG+"}"
    root=ET.Element(ns+"svg");group=ET.SubElement(root,ns+"g",{"{"+INK+"}label":"#Hall"});ET.SubElement(group,ns+"title").text="1"
    circle=ET.SubElement(group,ns+"circle",{"cx":"90","cy":"100","r":"5"});ET.SubElement(circle,ns+"title").text="1"
    profile={"seat_radius":5,"width":200,"height":600};positions={s["seat_key"]:(s["source_coordinate"]["x"],100) for s in model["seats"]}
    # Production SVG has much more shared grammar than this small synthetic fixture;
    # byte budget is separately exercised by the real source integration.
    monkeypatch.setattr("venue_generator.organizer_inventory.check_input_budget",lambda *args:{})
    raw,report=append_unavailable(ET.tostring(root),model,profile,positions)
    decor=ET.fromstring(raw).find(ns+"g[@id='UnavailableSeats']")
    assert len(list(decor.iter(ns+"rect")))==1 and not list(decor.iter(ns+"circle")) and decor.find(ns+"title") is None
    assert list(decor)[-1].tag==ns+"g" and list(decor)[-1].find(ns+"text").text=="2"
    assert report["unavailable_places"][0]["number"]=="2"


def test_organizer_decision_requires_actual_source_digest():
    model,decision=fixture();model["source"]["sha256"]="not-a-hash";decision["source_sha256"]="not-a-hash"
    with pytest.raises(ValueError,match="digest"):apply_numbering(model,decision)


def test_unanswered_event_intent_requires_clarification():
    model,decision=fixture();decision.pop("event_intent")
    with pytest.raises(ValueError,match="Clarify event intent"):apply_numbering(model,decision)

def test_clean_new_event_keeps_all_identities_and_source_sale_evidence():
    model,decision=fixture();decision["event_intent"]="new_event"
    result=apply_numbering(model,decision)
    assert [s["seat_number"] for s in result["seats"]]==["2","1"]
    assert all(s["status"]=="VALID" and s["target_event_availability"]=="available" for s in result["seats"])
    assert result["seats"][0]["source_event_state"]=="sold"

def test_sales_carry_cannot_be_faked_as_static_decor():
    model,decision=fixture();model=apply_numbering(model,decision)
    with pytest.raises(ValueError,match="not editable"):append_unavailable(b"",model,{}, {})
