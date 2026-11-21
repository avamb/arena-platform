"""Nonbookable service markers: decoration only, never imported ticket seats."""
import math,xml.etree.ElementTree as ET
from .core import SVG,_number
from .compact import check_input_budget

def append_nonbookable(svg,model,profile,markers,decision):
    if decision.get("schema")!="organizer-decision-v1" or decision.get("status")!="NONBOOKABLE_DISPLAY_ALLOWED" or decision.get("role")!="service" or decision.get("bookable") is not False or decision.get("visible") is not True or decision.get("source_sha256")!=model["source"]["sha256"] or not decision.get("evidence_id"):
        raise ValueError("Source-bound explicit nonbookable decision required")
    if not markers:raise ValueError("Measured service marker regions required")
    ns="{"+SVG+"}";ink="{http://www.inkscape.org/namespaces/inkscape}label"
    root=ET.fromstring(svg);ids={n.get("id") for n in root.iter() if n.get("id")}
    active=[(float(c.get("cx")),float(c.get("cy")),float(c.get("r"))) for g in root.findall(ns+"g") if g.find(ns+"title") is not None for c in g.findall(ns+"circle")]
    group=ET.SubElement(root,ns+"g",{"id":"ServiceMarkers"})
    transform=profile["transform"];radius=_number(profile["seat_radius"],True);mapped=[]
    for marker in markers:
        if marker.get("region_id") not in decision["region_ids"] or marker.get("seat_number") is not None:
            raise ValueError("Unknown region or invented service seat number")
        key=marker.get("id")
        if not isinstance(key,str) or not key or key in ids:raise ValueError("Unique service marker IDs required")
        ids.add(key)
        point=marker["source_coordinate"]
        x=_number(point["x"])*transform["scale_x"]+transform["translate_x"]
        y=_number(point["y"])*transform["scale_y"]+transform["translate_y"]
        if not radius<=x<=profile["width"]-radius or not radius<=y<=profile["height"]-radius:raise ValueError("Service marker outside canvas")
        if any(math.hypot(x-a,y-b)<radius+r+1-1e-8 for a,b,r in active):raise ValueError("Service marker overlaps active seat")
        if any(math.hypot(x-p["x"],y-p["y"])<2*radius+1-1e-8 for p in mapped):raise ValueError("Overlapping service markers")
        # Rounded rectangles look circular, but cannot be mistaken for a seat
        # circle by legacy target parsers. There is no functional row/title.
        ET.SubElement(group,ns+"rect",{"id":key,"x":format(x-radius,".12g"),"y":format(y-radius,".12g"),"width":str(2*radius),"height":str(2*radius),"rx":str(radius),"style":"fill:#e5e7eb;stroke:#64748b;stroke-width:1"})
        ET.SubElement(group,ns+"path",{"d":f"M {x-radius*.45:.12g} {y+radius*.45:.12g} L {x+radius*.45:.12g} {y-radius*.45:.12g}","style":"fill:none;stroke:#64748b;stroke-width:1"})
        mapped.append({**marker,"x":x,"y":y,"bookable":False,"functional_seat":False,"role_certainty":decision.get("certainty")})
    label=ET.SubElement(root,ns+"text",{"id":"ServiceMarkerLegend","x":"30","y":"500","style":"font-size:14px;fill:#475569"})
    label.text="Служебные · не продаются"+(" (предположение)" if decision.get("certainty")=="tentative" else "")
    # This is an invariant: markers must never add any selectable-seat circles.
    if group.find(ns+"title") is not None or group.get(ink) is not None or list(group.iter(ns+"circle")):
        raise ValueError("Nonbookable markers entered functional seating grammar")
    payload=ET.tostring(root,encoding="utf-8",xml_declaration=True)
    return payload,{"organizer_decision":decision,"nonbookable_markers":mapped,"functional_seats_added":0,**check_input_budget(payload,len(model["seats"]))}
