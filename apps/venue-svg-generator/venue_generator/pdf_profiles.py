"""Source-bound vector shapes and printed identities; no inferred numbering."""
from pathlib import Path
import hashlib,re
import pdfplumber
from bil24_svg_exporter.core import digest


def _inside(x,y,box):return box[0]<=x<=box[2] and box[1]<=y<=box[3]

def extract_profiled_pdf(profile):
    source=Path(profile["source"]);sha=hashlib.sha256(source.read_bytes()).hexdigest()
    if sha!=profile["source_sha256"]:raise ValueError("Source hash mismatch")
    seats=[];unknown=[];source_numbers=0;seen_shapes=set()
    with pdfplumber.open(source) as pdf:
        for region in profile["regions"]:
            page=pdf.pages[region.get("page",1)-1]
            words=page.extract_words(x_tolerance=1,y_tolerance=1)
            shapes=[]
            for kind in region["shape_kinds"]:
                if kind not in ("rects","curves"):raise ValueError("Unsupported primitive kind")
                for shape in getattr(page,kind):
                    x0,x1,t,b=(shape[k] for k in ("x0","x1","top","bottom"))
                    if not region["width_range"][0]<=x1-x0<=region["width_range"][1] or not region["height_range"][0]<=b-t<=region["height_range"][1]:continue
                    if not _inside((x0+x1)/2,(t+b)/2,region["seat_roi"]):continue
                    box=[x0,t,x1,b]
                    if box not in shapes:shapes.append(box)
            numbers=[w for w in words if re.fullmatch(r"[0-9]+",w["text"]) and _inside((w["x0"]+w["x1"])/2,(w["top"]+w["bottom"])/2,region["seat_roi"])]
            source_numbers+=len(numbers);covered=set()
            labels=[w for w in words if re.fullmatch(region.get("row_label_pattern",r"[0-9]+"),w["text"]) and _inside((w["x0"]+w["x1"])/2,(w["top"]+w["bottom"])/2,region["row_label_roi"])]
            axis=region.get("row_axis","y")
            if axis not in ("x","y"):raise ValueError("Invalid row matching axis")
            for box in shapes:
                x,y=(box[0]+box[2])/2,(box[1]+box[3])/2
                nums=[(i,w) for i,w in enumerate(numbers) if _inside((w["x0"]+w["x1"])/2,(w["top"]+w["bottom"])/2,box)]
                matched=[w for w in labels if abs(((w["x0"]+w["x1"])/2 if axis=="x" else (w["top"]+w["bottom"])/2)-(x if axis=="x" else y))<=region["row_label_tolerance"]]
                shape_key=(region.get("page",1),*box)
                if shape_key in seen_shapes:raise ValueError("Source shape selected by overlapping regions")
                seen_shapes.add(shape_key)
                if len(nums)!=1 or len(matched)!=1:
                    unknown.append({"source_bbox":box,"page":region.get("page",1),"reason":"missing_or_ambiguous_printed_number_or_row","number_matches":len(nums),"row_matches":len(matched)});continue
                i,word=nums[0]
                if i in covered:raise ValueError("Printed identity matched multiple source shapes")
                covered.add(i)
                seats.append({"seat_key":"cell-"+digest([sha,shape_key])[:24],"section_id":region["section"],"row_label":matched[0]["text"],"seat_number":word["text"],"status":"QUARANTINE","source_coordinate":{"x":x,"y":y},"source_bbox":box,"provenance":{"identity_origin":"source_glyph_inside_vector_shape","confidence":1.0,"source_name":source.name,"page":region.get("page",1),"number_word":word,"row_word":matched[0],"primitive_kinds":region["shape_kinds"]}})
            for i,w in enumerate(numbers):
                if i not in covered:unknown.append({"word":w,"reason":"printed_number_without_unambiguous_shape"})
    if hashlib.sha256(source.read_bytes()).hexdigest()!=sha:raise ValueError("Source changed")
    identities=[(s["section_id"],s["row_label"],s["seat_number"]) for s in seats]
    if len(identities)!=len(set(identities)):raise ValueError("Duplicate source identities")
    return {"schema_version":"seating-json-v1","source_only":True,"reference_svg_used":False,"source":{"sha256":sha,"name":source.name,"path":str(source)},"seats":seats,"quarantine":unknown,"validation":{"status":"SOURCE_COVERAGE_PASS" if seats and not unknown else "INCOMPLETE","source_numbers_in_roi":source_numbers,"mapped_source_numbers":len(seats),"unknown":len(unknown),"extraction_mode":"printed identities matched to source vector shapes"}}
