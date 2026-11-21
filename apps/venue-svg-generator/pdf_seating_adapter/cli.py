"""Local diagnostic CLI: no provider/API imports; never publishes sellable seats."""
import argparse
import hashlib
import json
from pathlib import Path
from .core import extract_pdf, render_diagnostic

def _save(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2, allow_nan=False)+"\n", encoding="utf-8")

def main(argv=None):
    parser = argparse.ArgumentParser(description="Source-only vector PDF diagnostic adapter")
    parser.add_argument("source", type=Path)
    parser.add_argument("--output-dir", type=Path, required=True,
                        help="New directory for this run; existing directories are refused")
    parser.add_argument("--section-label", action="append", default=[],
                        help="Exact case-sensitive source header to look for; never creates absent labels")
    parser.add_argument("--decor-label", action="append", default=[],
                        help="Exact source text to preserve as decoration")
    args = parser.parse_args(argv)
    out = args.output_dir
    try:
        out.mkdir(parents=True, exist_ok=False)
    except OSError as exc:
        print(json.dumps({"status":"FAIL", "error":str(exc), "output_directory_created":False}))
        return 1
    manifest = {"schema_version":"pdf-diagnostic-package-v1", "status":"INCOMPLETE",
                "product_acceptance":"NOT_VERIFIED", "files":{}}
    _save(out / "manifest.json", manifest)
    provenance = {"source":str(args.source), "source_only":True,
                  "reference_svg_used":False, "status":"INCOMPLETE"}
    try:
        model = extract_pdf(args.source, section_terms=args.section_label, decor_terms=args.decor_label)
        provenance.update(model["source"])
        provenance["status"] = model["validation"]["status"]
        if not model["source"]["source_unchanged"]:
            raise ValueError("Source changed during extraction")
        if not model["seats"]:
            raise ValueError("No unambiguous repeated-square candidates found")
        _save(out / "seating.json", model)
        render_diagnostic(model, out / "diagnostic.svg")
        summary = {**model["validation"], "summary":model["summary"],
                   "product_acceptance":"NOT_VERIFIED", "identity_accuracy":"UNKNOWN"}
        _save(out / "validation-summary.json", summary)
        _save(out / "provenance.json", provenance)
        manifest["status"] = "QUARANTINE"
        manifest["files"] = {name:hashlib.sha256((out/name).read_bytes()).hexdigest()
                             for name in ("seating.json","diagnostic.svg","validation-summary.json","provenance.json")}
        # Consumers must use the manifest, which is published atomically only after all outputs exist.
        _save(out / "manifest.pending.json", manifest)
        (out / "manifest.pending.json").replace(out / "manifest.json")
        print(json.dumps({"status":"QUARANTINE", "seat_count":len(model["seats"]),
                          "sellable_count":0, "output_dir":str(out)}))
        return 2
    except Exception as exc:
        provenance["status"] = "FAIL"
        _save(out / "provenance.json", provenance)
        _save(out / "validation-summary.json", {"status":"FAIL", "error":str(exc),
                                                "product_acceptance":"NOT_VERIFIED"})
        # Even if diagnostic files exist after a partial write, they are never a complete package.
        manifest.update(status="INCOMPLETE", error=str(exc), files={})
        _save(out / "manifest.json", manifest)
        print(json.dumps({"status":"FAIL", "error":str(exc), "output_dir":str(out)}))
        return 1

if __name__ == "__main__":
    raise SystemExit(main())
