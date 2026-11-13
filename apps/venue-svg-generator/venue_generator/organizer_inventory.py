"""Apply an explicit source-bound organizer rule; never guess missing places."""
import copy,hashlib,math,re
import xml.etree.ElementTree as ET
from bil24_svg_exporter.core import SVG,digest,_number
from bil24_svg_exporter.compact import check_input_budget


def apply_numbering(model,decision):
    if decision.get("schema")!="organizer-numbering-decision-v1" or decision.get("source_sha256")!=model["source"]["sha256"] or not decision.get("evidence_id") or not decision.get("original_answer") or decision.get("confirmed") is not True:
        raise ValueError("Explicit source-bound owner decision required")
    if not re.fullmatch(r"[0-9a-f]{64}",str(decision.get("source_sha256",""))) or not isinstance(decision.get("original_answer"),str):raise ValueError("Source digest and original owner text required")
    intent=decision.get("event_intent")
    if intent not in ("new_event","carry_source_sales"):raise ValueError("Clarify event intent: clean new event or carry source sales")
    rule=decision.get("numbering",{})
    if rule.get("direction") not in ("right_to_left","left_to_right") or rule.get("across_aisle") is not True or rule.get("start")!=1 or isinstance(rule.get("start"),bool):raise ValueError("Unsupported numbering rule")
    count=rule.get("count_per_row")
    if isinstance(count,bool) or not isinstance(count,int) or count<1:raise ValueError("Positive measured row count required")
    if decision.get("gray_bookable") is not False:raise ValueError("Gray availability needs explicit negative instruction")
    result=copy.deepcopy(model);rows={};keys=set();coordinates=set()
    for seat in result["seats"]:
        if seat.get("seat_number") is not None:raise ValueError("Existing source number cannot be overwritten")
        key=seat.get("seat_key");point=seat["source_coordinate"];xy=(_number(point["x"]),_number(point["y"]))
        if not key or key in keys or xy in coordinates or seat.get("source_appearance") not in ("gray","colored"):raise ValueError("Ambiguous source candidate")
        keys.add(key);coordinates.add(xy)
        if not seat.get("row_label") or not seat.get("section_id"):raise ValueError("Source row and section scope required")
        rows.setdefault((seat["section_id"],str(seat["row_label"])),[]).append(seat)
    if not rows:raise ValueError("No measured source rows")
    for row,seats in rows.items():
        if len(seats)!=count:raise ValueError("Source count differs from approved rule; never fill missing places")
        if len({s["source_coordinate"]["x"] for s in seats})!=len(seats):raise ValueError("Ambiguous horizontal seat order")
        for n,seat in enumerate(sorted(seats,key=lambda s:s["source_coordinate"]["x"],reverse=rule["direction"]=="right_to_left"),1):
            source_sold=seat["source_appearance"]=="gray"
            seat.update(seat_number=str(n),status="UNAVAILABLE" if source_sold and intent=="carry_source_sales" else "VALID",source_event_state="sold" if source_sold else "not_marked_sold",target_event_availability="blocked" if source_sold and intent=="carry_source_sales" else "available")
            seat.setdefault("provenance",{}).update(identity_origin="owner_confirmed_direction_start_and_row_scope",evidence_id=decision["evidence_id"],source_fact_verified=False,automatic_ocr_verified=False)
    result["organizer_decision"]=copy.deepcopy(decision)
    return result


def append_unavailable(svg,model,profile,positions):
    if model.get("organizer_decision",{}).get("output_purpose")!="static_preview_only":
        raise ValueError("Decor-only unavailable seats are not editable event inventory; target event-state import is required")
    # Recompute the organizer decision rather than trusting externally marked status.
    candidates=copy.deepcopy(model)
    for s in candidates["seats"]:s["seat_number"]=None
    verified=apply_numbering(candidates,model["organizer_decision"])
    expected={s["seat_key"]:(s["seat_number"],s["status"]) for s in verified["seats"]}
    if expected!={s["seat_key"]:(s["seat_number"],s["status"]) for s in model["seats"]}:raise ValueError("Inventory differs from organizer decision")
    if set(positions)!=set(expected):raise ValueError("Complete presentation mapping required")
    ns="{"+SVG+"}";root=ET.fromstring(svg);radius=_number(profile["seat_radius"],True)
    group=ET.SubElement(root,ns+"g",{"id":"UnavailableSeats","style":"fill:#b8b8b8;stroke:#64748b;stroke-width:1"});labels=[]
    text_group=ET.SubElement(group,ns+"g",{"style":"font-size:10px;text-anchor:middle;fill:#475569;stroke:none"})
    actual=[]
    for g in root.findall(ns+"g"):
        row=g.findtext(ns+"title")
        if row is None:continue
        section=g.get("{http://www.inkscape.org/namespaces/inkscape}label","").removeprefix("#")
        for c in g.findall(ns+"circle"):
            number=c.findtext(ns+"title");actual.append((section,row,number))
            matching=[s for s in model["seats"] if (s["section_id"],str(s["row_label"]),s["seat_number"])==(section,row,number) and s["status"]=="VALID"]
            if len(matching)!=1:raise ValueError("Unexpected selectable inventory")
            x,y=positions[matching[0]["seat_key"]]
            if abs(float(c.get("cx"))-x)>1e-8 or abs(float(c.get("cy"))-y)>1e-8 or float(c.get("r"))!=radius:raise ValueError("Selectable geometry differs from complete mapping")
    expected_active=[(s["section_id"],str(s["row_label"]),s["seat_number"]) for s in model["seats"] if s["status"]=="VALID"]
    if sorted(actual)!=sorted(expected_active):raise ValueError("SVG active inventory differs from confirmed availability")
    points=[]
    for s in model["seats"]:
        x,y=map(_number,positions[s["seat_key"]]);points.append((x,y,radius))
        if not radius<=x<=profile["width"]-radius or not radius<=y<=profile["height"]-radius:raise ValueError("Unavailable inventory outside canvas")
        if s["status"]!="UNAVAILABLE":continue
        ident="unavailable-"+hashlib.sha256((s["section_id"]+"|"+str(s["row_label"])+"|"+s["seat_number"]).encode()).hexdigest()[:20]
        ET.SubElement(group,ns+"rect",{"id":ident,"x":format(x-radius,".10g"),"y":format(y-radius,".10g"),"width":format(2*radius,".10g"),"height":format(2*radius,".10g"),"rx":format(radius,".10g")})
        label=ET.SubElement(text_group,ns+"text",{"x":format(x,".10g"),"y":format(y+3,".10g")});label.text=s["seat_number"]
        labels.append({"section":s["section_id"],"row":s["row_label"],"number":s["seat_number"],"source_key":s["seat_key"],"x":x,"y":y,"functional_seat":False,"bookable":False})
    if any(math.hypot(x-a,y-b)<r+q+1-1e-8 for i,(x,y,r) in enumerate(points) for a,b,q in points[i+1:]):raise ValueError("Inventory overlaps")
    group.remove(text_group);group.append(text_group)
    label=ET.SubElement(root,ns+"text",{"id":"UnavailableLegend","x":"30","y":"490","style":"font-size:14px;fill:#475569"});label.text="Серые места — неактивны"
    if group.find(ns+"title") is not None or list(group.iter(ns+"circle")):raise ValueError("Unavailable inventory entered functional grammar")
    raw=ET.tostring(root,encoding="utf-8",xml_declaration=True)
    return raw,{"owner_decision":model["organizer_decision"],"unavailable_places":labels,"master_inventory_sha256":digest(model),**check_input_budget(raw,sum(s["status"]=="VALID" for s in model["seats"]))}
