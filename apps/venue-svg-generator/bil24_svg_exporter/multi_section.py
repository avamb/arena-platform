"""Compose independently reviewed sections using the existing authoring renderer.

Seat source coordinates/identities are never inferred here. Each section supplies
its own explicit presentation transform and evidence. Shared category/stage
configuration must agree. Collision and byte-budget gates remain mandatory.
"""
import copy
import hashlib
import json
import math
import xml.etree.ElementTree as ET
from .core import render,SVG,INK,digest,_categories
from .compact import compact_authoring,check_input_budget

def render_sections(items):
    if not isinstance(items,list) or not items: raise ValueError("Reviewed sections required")
    ns="{"+SVG+"}";ink="{"+INK+"}label"
    root=None;labels=None;rowlabels=None;mapping=[];provenance=[];expected={}
    shared=None;sections=set()
    for model,profile,review in items:
        section=profile["section_label"]
        if section in sections: raise ValueError("Duplicate section")
        sections.add(section)
        identity=(profile["width"],profile["height"],profile["stage"],_categories(profile))
        if shared is not None and identity!=shared: raise ValueError("Shared canvas/stage/categories differ")
        shared=copy.deepcopy(identity)
        raw,report,prov=render(model,profile,review)
        raw,seatmap=compact_authoring(raw);tree=ET.fromstring(raw)
        provenance.append(prov);mapping+=seatmap
        section_index=len(sections)-1
        for group in tree.findall(ns+"g"):
            if group.get(ink)=="#"+section:
                row=group.findtext(ns+"title")
                group.set("id","r"+hashlib.sha256((section+"|"+row).encode()).hexdigest()[:16])
                for circle in group.findall(ns+"circle"):
                    key=(section,row,circle.findtext(ns+"title"))
                    if key in expected: raise ValueError("Duplicate seat identity")
                    expected[key]=tuple(float(circle.get(k)) for k in ("cx","cy","r"))
        heading=tree.find(ns+"text[@id='SectionLabel']")
        heading.set("id","SectionLabel-"+str(section_index))
        heading.set("x",str(profile.get("section_label_x",profile["width"]/2)))
        if root is None:
            root=tree;labels=root.find(ns+"g[@id='SeatNumberLabels']");rowlabels=root.find(ns+"g[@id='RowLabels']")
        else:
            for group in tree.findall(ns+"g"):
                if group.get(ink)=="#"+section: root.append(group)
            for label in tree.find(ns+"g[@id='SeatNumberLabels']"):labels.append(label)
            for label in tree.find(ns+"g[@id='RowLabels']"):rowlabels.append(label)
            root.append(heading)
    root.remove(labels);root.append(labels)
    root.find(ns+"metadata").text=json.dumps({"source_sha256s":sorted({p["source_sha256"] for p in provenance}),"model_sha256":digest([m for m,p,r in items]),"test_only":True},separators=(",",":"))
    ids=[n.get("id") for n in root.iter() if n.get("id")]
    if len(ids)!=len(set(ids)):raise ValueError("Duplicate SVG IDs after composition")
    actual={}
    for group in root.findall(ns+"g"):
        section=group.get(ink,"")[1:]
        if section not in sections:continue
        row=group.findtext(ns+"title")
        for circle in group.findall(ns+"circle"):
            key=(section,row,circle.findtext(ns+"title"))
            if key in actual:raise ValueError("Duplicate seat identity after composition")
            actual[key]=tuple(float(circle.get(k)) for k in ("cx","cy","r"))
    if actual!=expected:raise ValueError("Identity/geometry loss during composition")
    circles=list(actual.values())
    stroke=items[0][1].get("seat_stroke_width",3)
    overlaps=sum(math.hypot(x-a,y-b)<r+s+stroke-1e-8 for i,(x,y,r) in enumerate(circles) for a,b,s in circles[i+1:])
    if overlaps:raise ValueError("Seat overlap across sections")
    raw=ET.tostring(root,encoding="utf-8",xml_declaration=True)
    report={"status":"LOCAL_STRUCTURAL_PASS","seats":len(actual),"sections":len(sections),"rows":len({(s,r) for s,r,n in actual}),"category_slots":len(_categories(items[0][1])[0]),"identity_mismatches":0,"coordinate_mismatches":0,"overlaps":overlaps,**check_input_budget(raw,len(actual))}
    return raw,report,{"sections":provenance,"seat_element_mapping":mapping,"source_only":True,"reference_svg_used":False}
