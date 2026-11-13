"""Source-only numbered PDF table pipeline; explicit source profile, no API.

Profiles are measurement/configuration, never a replacement for source glyphs.
All detected glyphs must be covered by the source review; no sequence is filled.
"""
from pathlib import Path
import argparse,hashlib,json,copy,time
import pdfplumber
from pdf_seating_adapter.grid_cells import reconstruct_numbered_cells
from bil24_svg_exporter.core import digest
from bil24_svg_exporter.multi_section import render_sections

def inside(x,y,roi):return roi[0]<=x<=roi[2] and roi[1]<=y<=roi[3]

def extract_pdf(profile):
    source=Path(profile["source"])
    sha=hashlib.sha256(source.read_bytes()).hexdigest()
    if sha!=profile["source_sha256"]:raise ValueError("Source hash mismatch")
    with pdfplumber.open(source) as pdf:
        page=pdf.pages[profile.get("page",1)-1]
        words=page.extract_words(x_tolerance=1,y_tolerance=1)
        rects=[{k:r[k] for k in ("x0","x1","top","bottom")} for r in page.rects]
        candidates=reconstruct_numbered_cells(rects,words,**profile.get("cell_parameters",{}))
        row_glyphs=[w for w in words if w["text"].isascii() and w["text"].isdigit()
                    and inside((w["x0"]+w["x1"])/2,(w["top"]+w["bottom"])/2,profile["row_label_roi"])]
        selected=[c for c in candidates if inside(c["x"],c["y"],profile["seat_roi"])]
        seats=[];unknown=[]
        for index,c in enumerate(selected):
            labels=[w for w in row_glyphs if abs((w["top"]+w["bottom"])/2-c["y"])<=profile["row_label_tolerance"]]
            if len(labels)!=1:unknown.append({"cell":c,"reason":"missing_or_ambiguous_row_glyph"});continue
            label=labels[0]
            seats.append({"seat_key":"cell-"+digest([sha,profile.get("page",1),c["source_bbox"]])[:24],
                "section_id":profile["section_label"],"row_label":label["text"],"seat_number":c["number"],
                "status":"QUARANTINE","source_coordinate":{"x":c["x"],"y":c["y"]},"source_bbox":c["source_bbox"],
                "provenance":{"identity_origin":"source_glyph","confidence":1.0,"number_word":c["evidence_word"],"row_word":label,"source_name":source.name,"page":profile.get("page",1)}})
        # Check every source number in the explicitly measured seat ROI, not a
        # target count from a historical/manual SVG or Claude response.
        all_numbers=[w for w in words if w["text"].isascii() and w["text"].isdigit()
                     and inside((w["x0"]+w["x1"])/2,(w["top"]+w["bottom"])/2,profile["seat_roi"])]
        covered={(s["provenance"]["number_word"]["x0"],s["provenance"]["number_word"]["top"],s["seat_number"]) for s in seats}
        for word in all_numbers:
            if (word["x0"],word["top"],word["text"]) not in covered:unknown.append({"word":word,"reason":"number_without_closed_cell"})
    if hashlib.sha256(source.read_bytes()).hexdigest()!=sha:raise ValueError("Source changed")
    identities=[(s["section_id"],s["row_label"],s["seat_number"]) for s in seats]
    if len(identities)!=len(set(identities)):raise ValueError("Duplicate identities")
    return {"schema_version":"seating-json-v1","source_only":True,"reference_svg_used":False,
            "source":{"sha256":sha,"name":source.name,"path":str(source)},"seats":seats,"quarantine":unknown,
            "validation":{"status":"SOURCE_COVERAGE_PASS" if seats and not unknown else "INCOMPLETE",
                          "source_numbers_in_roi":len(all_numbers),"mapped_source_numbers":len(covered),"unknown":len(unknown)}}

def _validate_raster_row_edge_alignment(annotation, rows, columns):
    """Validate explicit outside-edge anchoring for short annotated rows.

    An incomplete row must never be reconstructed from its neighbours.  When
    the source review says that a short block shares an outside edge with a
    full reference row, validate that source geometry before mapping a seat.
    The check is opt-in: profiles without the reviewed annotation retain their
    existing source-only requirements.
    """
    rules=annotation.get("row_edge_alignment", [])
    if rules is None: return
    if not isinstance(rules, list): raise ValueError("Row edge alignment must be a list")
    by_label={str(row.get("label")): row for row in rows}
    for rule in rules:
        if not isinstance(rule, dict): raise ValueError("Invalid row edge alignment rule")
        row_label=str(rule.get("row_label", ""));reference_label=str(rule.get("reference_row_label", ""))
        anchor=rule.get("anchor");tolerance=rule.get("tolerance_px", 0)
        target=by_label.get(row_label);reference=by_label.get(reference_label)
        if target is None or reference is None: raise ValueError("Row edge alignment references an unknown row")
        if anchor not in ("outer_left", "outer_right"): raise ValueError("Unsupported row edge anchor")
        if isinstance(tolerance, bool) or not isinstance(tolerance, int) or tolerance<0: raise ValueError("Invalid row edge tolerance")
        target_columns=target.get("columns", columns);reference_columns=reference.get("columns", columns)
        expected_count=rule.get("target_count")
        if isinstance(expected_count, bool) or not isinstance(expected_count, int) or expected_count<=0 or len(target_columns)!=expected_count:
            raise ValueError("Short row count does not match its reviewed edge-alignment annotation")
        if not target_columns or not reference_columns: raise ValueError("Row edge alignment needs source columns")
        if anchor=="outer_left":
            target_edge,reference_edge=target_columns[0][0],reference_columns[0][0]
        else:
            target_edge,reference_edge=target_columns[-1][1],reference_columns[-1][1]
        if abs(target_edge-reference_edge)>tolerance:
            raise ValueError("Short row is not anchored to its reviewed source outer edge")


def extract_raster(profile):
    """Measured table cells plus independently reviewed printed label template.

    This is explicitly a configured source annotation, not automatic OCR.
    No detector guesses identities from cell index without the bound review.
    """
    import cv2
    import numpy as np
    source=Path(profile["source"]);sha=hashlib.sha256(source.read_bytes()).hexdigest()
    if sha!=profile["source_sha256"]:raise ValueError("Source hash mismatch")
    image=cv2.imdecode(np.frombuffer(source.read_bytes(),dtype=np.uint8),cv2.IMREAD_COLOR)
    if image is None:raise ValueError("Unreadable source image")
    annotation=profile["printed_cell_annotation"]
    if annotation.get("source_sha256")!=sha or annotation.get("review_verdict")!="PASS" or not annotation.get("evidence"):
        raise ValueError("Printed-cell annotation must be independently source-bound")
    threshold=annotation.get("ink_threshold",150)
    if isinstance(threshold,bool) or not isinstance(threshold,int) or not 1<=threshold<=230:raise ValueError("Invalid reviewed source ink threshold")
    columns=annotation["columns"];rows=annotation["rows"]
    _validate_raster_row_edge_alignment(annotation, rows, columns)
    seats=[];unknown=[]
    for row in rows:
        row_columns=row.get("columns",columns)
        if len(row["printed_numbers"])!=len(row_columns):raise ValueError("Every annotated cell needs an explicit source label")
        mode=row.get("identity_mode","printed_cell")
        if mode not in ("printed_cell","column_header"):raise ValueError("Unverified raster identity mode")
        headers=row.get("number_header_bboxes",[])
        if mode=="column_header" and (len(headers)!=len(row_columns) or not annotation.get("column_header_evidence")):
            raise ValueError("Column header identities require source evidence and one header per cell")
        for column_index,(column,number) in enumerate(zip(row_columns,row["printed_numbers"])):
            box=[column[0],row["top"],column[1],row["bottom"]]
            if not all(isinstance(v,int) and not isinstance(v,bool) for v in box) or not 0<=box[0]<box[2]<=image.shape[1] or not 0<=box[1]<box[3]<=image.shape[0]:raise ValueError("Cell outside source")
            patch=image[box[1]+2:box[3]-2,box[0]+2:box[2]-2]
            # A printed cell must contain actual dark ink, not an empty slot.
            ink=int((cv2.cvtColor(patch,cv2.COLOR_BGR2GRAY)<threshold).sum())
            header=None
            if mode=="column_header":
                header=headers[column_index]
                if not isinstance(header,list) or len(header)!=4 or not all(isinstance(v,int) and not isinstance(v,bool) for v in header) or not 0<=header[0]<header[2]<=image.shape[1] or not 0<=header[1]<header[3]<=image.shape[0]:raise ValueError("Header outside source")
                if abs((header[0]+header[2])/2-(box[0]+box[2])/2)>2:raise ValueError("Header must align to its source column")
                hp=image[header[1]+2:header[3]-2,header[0]+2:header[2]-2]
                header_ink=int((cv2.cvtColor(hp,cv2.COLOR_BGR2GRAY)<150).sum()) if hp.size else 0
                hsv=cv2.cvtColor(patch,cv2.COLOR_BGR2HSV) if patch.size else np.zeros((1,1,3))
                # Explicit cyan-cell profile: white blanks and magenta excluded.
                cyan=(hsv[:,:,0]>=80)&(hsv[:,:,0]<=100)&(hsv[:,:,1]>=150)&(hsv[:,:,2]>=150)
                ink=3 if header_ink>=3 and float(cyan.mean())>=.65 else 0
            if ink<3:
                unknown.append({"source_bbox":box,"reason":"no_source_ink_for_annotated_label"});continue
            if not str(number).isascii() or not str(number).isdigit():raise ValueError("Unverified raster identity")
            seats.append({"seat_key":"cell-"+digest([sha,box])[:24],"section_id":row.get("section",profile["section_label"]),"row_label":str(row["label"]),"seat_number":str(number),"status":"QUARANTINE",
                          "source_coordinate":{"x":(box[0]+box[2])/2,"y":(box[1]+box[3])/2},"source_bbox":box,
                          "provenance":{"identity_origin":"independently_reviewed_source_column_header" if mode=="column_header" else "independently_reviewed_source_cell_annotation","number_header_bbox":header,"evidence":annotation["evidence"],"confidence":None,"automatic_ocr_verified":False,"source_name":source.name,"page":1}})
    if hashlib.sha256(source.read_bytes()).hexdigest()!=sha:raise ValueError("Source changed")
    identities=[(s["section_id"],s["row_label"],s["seat_number"]) for s in seats]
    if len(identities)!=len(set(identities)) or len({s["seat_key"] for s in seats})!=len(seats):raise ValueError("Duplicate raster identities or physical source cells")
    return {"schema_version":"seating-json-v1","source_only":True,"reference_svg_used":False,"source":{"sha256":sha,"name":source.name,"path":str(source)},"seats":seats,"quarantine":unknown,
            "validation":{"status":"SOURCE_COVERAGE_PASS" if seats and not unknown else "INCOMPLETE","source_numbers_in_roi":sum(len(r.get("columns",columns)) for r in rows),"mapped_source_numbers":len(seats),"unknown":len(unknown),"extraction_mode":"reviewed source cell/header annotation; pixel geometry and ink/colour gate; no automatic OCR claim"}}

def extract(profile):
    if profile.get("source_type")=="pdf_numbered_regions":
        from .pdf_profiles import extract_profiled_pdf
        return extract_profiled_pdf(profile)
    return extract_raster(profile) if profile.get("source_type")=="raster_numbered_table" else extract_pdf(profile)

def generate(profile,out):
    start=time.monotonic();out=Path(out)
    if out.exists():raise ValueError("Existing output refused")
    out.mkdir(parents=True)
    write=lambda name,value:(out/name).write_text(json.dumps(value,ensure_ascii=False,indent=2),encoding="utf-8")
    write("manifest.json",{"status":"INCOMPLETE"})
    model=extract(profile);write("seating.json",model)
    review=profile["source_review"]
    if review.get("verdict")!="PASS" or review.get("source_sha256")!=model["source"]["sha256"] or not review.get("evidence"):
        raise ValueError("Independent source review missing")
    if model["validation"]["status"]!="SOURCE_COVERAGE_PASS":
        write("validation-summary.json",model["validation"]);return model["validation"]
    if profile.get("clarification_issues"):
        from .clarification import build_clarification
        questions=[build_clarification(issue) for issue in profile["clarification_issues"]]
        reply=profile.get("organizer_reply")
        decision=None
        if reply:
            from .organizer_comments import interpret_comment
            decision=interpret_comment(reply["text"],source_sha256=model["source"]["sha256"],region_ids=reply["region_ids"],evidence_id=reply["evidence_id"])
            write("organizer-decision.json",decision)
        required={r["id"] for q in questions for r in q["regions"]}
        if not decision or decision["status"]!="NONBOOKABLE_DISPLAY_ALLOWED" or set(decision["region_ids"])!=required:
            write("clarifications.json",questions)
            model["validation"].update(status="INCOMPLETE",human_clarification="REQUIRED")
            write("seating.json",model);write("validation-summary.json",model["validation"])
            return model["validation"]
        for question in questions:
            if question["source_sha256"]!=model["source"]["sha256"]:raise ValueError("Clarification source mismatch")
            for region in question["regions"]:
                measured=[m for m in profile.get("nonbookable_markers",[]) if m.get("region_id")==region["id"]]
                if len(measured)!=region["cell_count"]:raise ValueError("Service marker count must cover the clarified visible region")
                for marker in measured:
                    point=marker["source_coordinate"]
                    if not inside(point["x"],point["y"],region["bbox"]):raise ValueError("Service marker outside clarified source region")
        model["organizer_decision"]=decision
        model["nonbookable_features"]=profile.get("nonbookable_markers",[])
    semantics=review.get("semantics")
    if not isinstance(semantics,dict) or semantics.get("status")!="PASS" or semantics.get("unresolved"):
        model["validation"]["status"]="INCOMPLETE"
        model["validation"]["semantic_review"]=semantics or {"status":"NOT_REVIEWED"}
        write("validation-summary.json",model["validation"])
        write("seating.json",model)
        return model["validation"]
    for seat in model["seats"]:seat["status"]="VALID"
    model["validation"]["semantic_review"]="PASS"
    write("seating.json",model)
    items=[];section_profiles={};section_reviews={}
    for section in dict.fromkeys(s["section_id"] for s in model["seats"]):
        local=copy.deepcopy(model)
        local["seats"]=[s for s in local["seats"] if s["section_id"]==section]
        presentation=copy.deepcopy(profile["presentation"])
        if section!=presentation["section_label"] and section not in profile.get("section_profiles",{}):
            raise ValueError("Every extra section requires an explicit source presentation profile")
        presentation.update(profile.get("section_profiles",{}).get(section,{}))
        presentation["section_label"]=section
        sections={}
        for seat in local["seats"]:sections.setdefault(seat["row_label"],[]).append(seat)
        segments=[]
        for row,seats in sections.items():
            ordered=sorted(seats,key=lambda seat:seat["source_coordinate"]["x"])
            if profile.get("horizontal_rows_confirmed"):
                from bil24_svg_exporter.geometry import split_path
                points={seat["seat_key"]:(seat["source_coordinate"]["x"],seat["source_coordinate"]["y"]) for seat in ordered}
                segments.extend({"keys":part,"kind":"line","axis":"horizontal","axis_group":row} for part in split_path(points,list(points)) if len(part)>=3)
        if segments:presentation["geometry_regularization"]={"evidence":review["evidence"],"segments":segments,"max_shift_fraction":.35}
        export_review={"schema":"bil24-export-review-v1","model_sha256":digest(local),"source_sha256":local["source"]["sha256"],"verdict":"PASS","evidence":review["evidence"],"approved_identities":[[str(seat["row_label"]),str(seat["seat_number"])] for seat in local["seats"]]}
        items.append((local,presentation,export_review));section_profiles[section]=presentation;section_reviews[section]=export_review
    raw,summary,provenance=render_sections(items)
    presentation=items[0][1];export_review=items[0][2]
    if model.get("organizer_decision") and len(items)!=1:
        raise ValueError("Service markers across multiple transforms require individual source decisions")
    if model.get("organizer_decision"):
        from bil24_svg_exporter.nonbookable import append_nonbookable
        raw,nonbookable_report=append_nonbookable(raw,model,presentation,model["nonbookable_features"],model["organizer_decision"])
        provenance.update(nonbookable_report)
        summary.update(nonbookable_markers=len(model["nonbookable_features"]),functional_seats_added=0,role_certainty=model["organizer_decision"]["certainty"],svg_bytes=len(raw),input_bytes_per_seat=round(len(raw)/len(model["seats"]),3))
    # Shared authoring grammar is supported by both local parsers. Independently
    # test each target; equal bytes do not imply equal frontend rendering.
    (out/"venue_Bil24.svg").write_bytes(raw);(out/"venue_Arena.svg").write_bytes(raw)
    write("export-profile.json",presentation);write("export-review.json",export_review)
    write("export-profiles.json",section_profiles);write("export-reviews.json",section_reviews)
    write("provenance.json",provenance)
    summary.update(source_coverage=model["validation"],reference_svg_used=False,source_only=True,elapsed_local_seconds=time.monotonic()-start,
                   target_acceptance={"bil24_server":"PENDING_OWNER_UPLOAD","arena_import":"PENDING_LOCAL_TEST","arena_widget":"NOT_TESTED"})
    write("validation-summary.json",summary);write("venue-metadata.json",profile.get("metadata",{}))
    write("manifest.pending.json",{"status":"READY_FOR_LOCAL_TARGET_TESTS","files":{p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in out.iterdir() if p.is_file() and p.name!="manifest.json"}})
    (out/"manifest.pending.json").replace(out/"manifest.json")
    return summary

def main(argv=None):
    parser=argparse.ArgumentParser();parser.add_argument("profile",type=Path);parser.add_argument("--output-dir",type=Path,required=True)
    args=parser.parse_args(argv)
    existed=args.output_dir.exists()
    try:
        result=generate(json.loads(args.profile.read_text(encoding="utf-8")),args.output_dir)
        print(json.dumps(result));return 0 if result.get("source_coverage",{}).get("status")=="SOURCE_COVERAGE_PASS" else 2
    except Exception as exc:
        if not existed and args.output_dir.exists():
            (args.output_dir/"validation-summary.json").write_text(json.dumps({"status":"FAIL","error":str(exc)}),encoding="utf-8")
        print(json.dumps({"status":"FAIL","error":str(exc)}));return 1

if __name__=="__main__":raise SystemExit(main())
