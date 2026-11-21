"""Compact import SVG without discarding functional identities or source provenance."""
import json
import xml.etree.ElementTree as ET
from .core import SVG

# Input headroom, NOT a prediction of Forzum's processed BSS size.
MAX_INPUT_BYTES_PER_SEAT=250
OBSERVED_FORZUM_PROCESSED_LIMIT=400

def compact_authoring(data):
    root=ET.fromstring(data)
    mapping=[]
    for node in root.iter():
        if "data-source-key" in node.attrib:
            original_id=node.attrib["id"]
            compact_id="s"+original_id.removeprefix("seat-")[:16]
            mapping.append({"svg_id":compact_id,"original_svg_id":original_id,
                "source_key":node.attrib["data-source-key"],"section":node.attrib["data-section"],
                "row":node.attrib["data-row"],"seat_number":node.attrib["data-seat"],
                "source_coordinate":{"x":float(node.attrib["data-source-x"]),"y":float(node.attrib["data-source-y"])}})
            node.attrib["id"]=compact_id
            for key in list(node.attrib):
                if key.startswith("data-"):
                    del node.attrib[key]
            # Inline style retained for Forzum; redundant paint attributes removed.
            for key in ("fill","stroke","stroke-width"):
                node.attrib.pop(key,None)
        for key in ("cx","cy","x","y","r","width","height"):
            if key in node.attrib:
                try:
                    node.attrib[key]=format(float(node.attrib[key]),".12g")
                except ValueError:
                    pass
    metadata=root.find("{"+SVG+"}metadata")
    if metadata is not None:
        full=json.loads(metadata.text)
        metadata.text=json.dumps({"source_sha256":full["source_sha256"],
            "model_sha256":full["model_sha256"],"test_only":True},separators=(",",":"))
    for labels in (root.find("{"+SVG+"}g[@id='SeatNumberLabels']"),root.find("{"+SVG+"}g[@id='RowLabels']")):
      if labels is not None and len(labels):
          keys=("font-size","text-anchor","fill","pointer-events")
          common={key:labels[0].attrib[key] for key in keys if key in labels[0].attrib and all(child.attrib.get(key)==labels[0].attrib[key] for child in labels)}
          for key,value in common.items(): labels.set(key,value)
          for child in labels:
              # These generated label styles mirror the promoted attributes exactly.
              child.attrib.pop("style",None)
              for key in common: child.attrib.pop(key,None)
    # Standard SVG inheritance reduces repeated outline declarations. Keep fill
    # explicit on every seat so category matching never relies on inheritance.
    ns="{"+SVG+"}"
    for group in root.iter(ns+"g"):
        circles=group.findall(ns+"circle")
        if group.find(ns+"title") is None or not circles:continue
        paints=[dict(part.split(":",1) for part in c.get("style","").split(";") if ":" in part) for c in circles]
        common={key:paints[0][key] for key in ("stroke","stroke-width") if key in paints[0] and all(p.get(key)==paints[0][key] for p in paints)}
        if not common:continue
        group_paint=dict(part.split(":",1) for part in group.get("style","").split(";") if ":" in part)
        group_paint.update(common);group.set("style",";".join(k+":"+v for k,v in group_paint.items()))
        for circle,paint in zip(circles,paints):
            for key in common:paint.pop(key,None)
            circle.set("style",";".join(k+":"+v for k,v in paint.items()))
    # Inline style is the Forzum-compatible paint declaration; identical
    # presentation attributes need not repeat it on decorative nodes.
    for node in root.iter():
        paint=dict(part.split(":",1) for part in node.get("style","").split(";") if ":" in part)
        for key in ("fill","stroke","stroke-width","font-size","font-family","text-anchor","display"):
            if key in paint and node.get(key)==paint[key] and node.get("id") not in {"Sold","None","Reserved","MyTickets"}:node.attrib.pop(key,None)
    compact=ET.tostring(root,encoding="utf-8",xml_declaration=True)
    if not mapping: raise ValueError("No source-key seating elements for compact export")
    return compact,mapping

def check_input_budget(data,seat_count):
    if not isinstance(seat_count,int) or seat_count<=0:
        raise ValueError("Positive seat count required for size gate")
    ratio=len(data)/seat_count
    if ratio>MAX_INPUT_BYTES_PER_SEAT:
        raise ValueError(f"Compact SVG input budget exceeded: {ratio:.2f} bytes/seat > {MAX_INPUT_BYTES_PER_SEAT}; reduce decoration before upload")
    return {"svg_bytes":len(data),"input_bytes_per_seat":round(ratio,3),
            "input_budget_bytes_per_seat":MAX_INPUT_BYTES_PER_SEAT,
            "observed_forzum_processed_limit":OBSERVED_FORZUM_PROCESSED_LIMIT,
            "processed_forzum_bytes_per_seat":"NOT_MEASURED_LOCALLY"}
