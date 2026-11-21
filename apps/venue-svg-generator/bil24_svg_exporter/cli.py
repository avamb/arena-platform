"""Fresh-directory, offline-only authoring export."""
import argparse
import hashlib
import json
import xml.etree.ElementTree as ET
from pathlib import Path
from .core import render, validate
from .compact import compact_authoring, check_input_budget
from .profiles import uniform_contiguous_rows

def main(argv=None):
    parser=argparse.ArgumentParser(description="Reviewed TEST_ONLY Bil24 authoring SVG")
    parser.add_argument("source",type=Path)
    parser.add_argument("--profile",type=Path,required=True)
    parser.add_argument("--review",type=Path,required=True)
    parser.add_argument("--output-dir",type=Path,required=True)
    parser.add_argument("--uniform-rows-evidence",help="Source-based confirmation that rows are contiguous; enables reusable equal-gap presentation")
    parser.add_argument("--seat-gap",type=float,default=9,help="Visible gap in px for uniform rows (default 9)")
    parser.add_argument("--seat-stroke-width",type=float,default=1,help="Seat outline in px for uniform rows (default 1)")
    args=parser.parse_args(argv)
    try:
        if args.output_dir.exists():
            raise ValueError("Existing output directory refused; choose a new run directory")
        model,profile,review=(json.loads(p.read_text(encoding="utf-8")) for p in (args.source,args.profile,args.review))
        if args.uniform_rows_evidence is not None:
            profile=uniform_contiguous_rows(profile,args.uniform_rows_evidence,args.seat_gap,args.seat_stroke_width)
        elif args.seat_gap!=9 or args.seat_stroke_width!=1:
            raise ValueError("Spacing overrides require --uniform-rows-evidence")
        svg,summary,provenance=render(model,profile,review)
        svg,mapping=compact_authoring(svg)
        summary=validate(svg,model,profile)
        summary.update(check_input_budget(svg,len(model["seats"])))
        provenance["seat_element_mapping"]=mapping
        provenance["compact_svg"]=True
        provenance["presentation_layout"]["source_coordinate_recovery"]="Use provenance.json seat_element_mapping/source_coordinate; uniform layout is presentation only."
        args.output_dir.mkdir(parents=True,exist_ok=False)
        (args.output_dir/"authoring.svg").write_bytes(svg)
        for name,value in (("validation-summary.json",summary),("provenance.json",provenance)):
            (args.output_dir/name).write_text(json.dumps(value,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
        manifest={"status":"COMPLETE_TEST_ONLY","files":["authoring.svg","validation-summary.json","provenance.json"],
                  "svg_sha256":hashlib.sha256(svg).hexdigest(),"source_file_sha256":hashlib.sha256(args.source.read_bytes()).hexdigest()}
        temp=args.output_dir/"manifest.tmp"
        temp.write_text(json.dumps(manifest,indent=2)+"\n",encoding="utf-8")
        temp.replace(args.output_dir/"manifest.json")
        print(json.dumps(summary))
        return 0
    except (ValueError,OSError,KeyError,TypeError,ET.ParseError) as exc:
        print(json.dumps({"status":"FAIL","error":str(exc)},ensure_ascii=True))
        return 1
