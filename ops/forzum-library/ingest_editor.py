"""Ingest hall plans saved from the Forzum Editor as SVG files.

Reads <from>/manifest.jsonl (or .json, .csv) and the SVG files next to it, and writes the plan
folders <lib>/plans/<country>/<city>/<venueId>/<seatingPlanId>/ (source.svg + plan.json).
Resumable: a plan already on disk with the same file content is not copied again.
Works offline: no API, no token.
"""

import argparse
import csv
import hashlib
import json
import os
import re
import shutil
import sys
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # works under python -I
from download import (TEST_RE, dedupe, newest_by_id, now, plan_dir, rebuild_manifest)
from forzum_client import DEFAULT_LIB, is_russian, load_json, save_json

INKSCAPE_LABEL = "{http://www.inkscape.org/namespaces/inkscape}label"
SBT = "{http://www.w3.org/2015/sbt/1.0}"


def file_problem(src, rel):
    """Why a manifest file may not be copied, or None: it must be an .svg inside the --from folder."""
    if not rel.lower().endswith(".svg"):
        return "file is not an .svg"
    try:
        root = os.path.realpath(src)
        full = os.path.realpath(os.path.join(src, rel))
        if os.path.commonpath([root, full]) != root:
            return "file is outside the --from folder"
    except ValueError:
        return "file is outside the --from folder"
    return None


def read_seats(svg_path):
    """List of (sector, row, seat), one per seat circle. Three dialects:
    template (circle with sbt:seat under a g with sbt:sect / sbt:row), session sbt (circle with sbt:id
    and sbt:state) and authoring (any circle inside an element whose inkscape:label starts with '#')."""
    root = ET.parse(svg_path).getroot()
    out = []

    def walk(node, sect, row, label, idx):
        sect = node.get(SBT + "sect", sect)
        row = node.get(SBT + "row", row)
        if str(node.get(INKSCAPE_LABEL) or "").startswith("#"):
            label = str(node.get(INKSCAPE_LABEL))
            idx = [0]
        if node.tag.rsplit("}", 1)[-1] == "circle":
            if node.get(SBT + "seat") is not None:
                out.append((str(sect or ""), str(row or ""), str(node.get(SBT + "seat"))))
            elif node.get(SBT + "id") is not None and node.get(SBT + "state") is not None:
                out.append((str(sect or ""), str(row or ""), str(node.get(SBT + "id"))))
            elif label is not None:
                idx[0] += 1
                title = ""
                for ch in node:
                    if ch.tag.rsplit("}", 1)[-1] == "title":
                        title = (ch.text or "").strip()
                out.append(("", label, title or str(idx[0])))
        for ch in node:
            walk(ch, sect, row, label, idx)

    walk(root, None, None, None, [0])
    return out


def count_seats(svg_path):
    return len(read_seats(svg_path))


def seat_signature(svg_path):
    try:
        return set(read_seats(svg_path))
    except (ET.ParseError, OSError):
        return set()


# --- same hall among the editor plans of one venue -------------------------------------------

_DATE_RE = re.compile(r"\s*\b\d{1,2}[/.]\d{4}\s*$")
_VER_RE = re.compile(r"(\bver(?:sion)?\.?\s*\d+|\bv\.?\s*\d+|\bверсия\s*\d+|\(\s*\d+\s*\))", re.IGNORECASE)
_TAIL_RE = re.compile(r"\s*[_\-]\s*\d+\s*$")
_COMB_TOKEN_RE = re.compile(r"\bcom\w*\s*\d*", re.IGNORECASE)


def is_comb(name):
    return bool(re.search(r"comb|\bcom\d", str(name or ""), re.IGNORECASE))


def hall_key(name):
    """The hall name without the version mark (and without the 'comb' word). '' means the default name."""
    s = str(name or "").lower()
    if is_comb(s):
        s = _COMB_TOKEN_RE.sub(" ", s)
    s = _DATE_RE.sub("", s)
    s = _VER_RE.sub(" ", s)
    for _ in range(3):
        s = _TAIL_RE.sub("", s)
    return re.sub(r"\s+", " ", s).strip(" -_.()")


def _match(a, b):
    """How two (path, rec, seats, _) items are the same hall: 'names', 'seats' or None."""
    if is_comb(a[1]["seatingPlanName"]) != is_comb(b[1]["seatingPlanName"]):
        return None
    if hall_key(a[1]["seatingPlanName"]) == hall_key(b[1]["seatingPlanName"]):
        return "names"
    if a[2] and a[2] == b[2]:
        return "seats"
    return None


def editor_seats(path, rec):
    return seat_signature(os.path.join(os.path.dirname(path), "source.svg"))


def editor_same(a, b):
    return _match(a, b) is not None


def editor_pick(group):
    """Newest = highest numeric seatingPlanId; planVersion is the file format mark and never decides."""
    newest = newest_by_id(group)
    how = {_match(newest, g) for g in group if g is not newest}
    how.discard(None)
    kind = "names" if how == {"names"} else "seats" if how == {"seats"} else "names, seats"
    return newest, "same hall by %s, newest by plan id" % kind


def norm_rel(p):
    return os.path.normpath(str(p).strip().replace("\\", "/")).replace(os.sep, "/")


EDITOR_FILE_RE = re.compile(r"^(\d+)_(\d+)\.svg$", re.IGNORECASE)

# canonical key (no spaces, '_' or '-', lower case) -> internal field
JSONL_FIELDS = {
    "file": "file",
    "country": "countryName", "countryname": "countryName",
    "city": "cityName", "cityname": "cityName",
    "venueid": "venueId",
    "venue": "venueName", "venuename": "venueName",
    "seatingplanid": "seatingPlanId",
    "planname": "seatingPlanName", "seatingplanname": "seatingPlanName",
    "version": "version", "planversion": "version",
    "status": "manifestStatus", "note": "note", "skippedplans": "skippedPlans",
}

GA_NAME_RE = re.compile(r"^\s*(GA|General|Без мест|Входн)", re.IGNORECASE)


def canon_key(k):
    return re.sub(r"[\s_\-]", "", str(k)).lower()


def jsonl_row(obj, lineno):
    """Maps one manifest.jsonl object onto the internal field names."""
    out = {}
    for k, v in obj.items():
        field = JSONL_FIELDS.get(canon_key(k))
        if field and (field not in out or blank(out[field])):
            out[field] = v
    if blank(out.get("file")) and not blank(out.get("venueId")) and not blank(out.get("seatingPlanId")):
        out["file"] = "%s_%s.svg" % (str(out["venueId"]).strip(), str(out["seatingPlanId"]).strip())
    out["_line"] = lineno
    if not blank(out.get("file")):
        m = EDITOR_FILE_RE.match(os.path.basename(norm_rel(out["file"])))
        if m and ((not blank(out.get("venueId")) and str(out["venueId"]).strip() != m.group(1))
                  or (not blank(out.get("seatingPlanId")) and str(out["seatingPlanId"]).strip() != m.group(2))):
            out["_reject"] = "file name and manifest ids differ"
    return out


def read_jsonl(path):
    """One row per non-blank line; a line that is not a JSON object becomes a rejected row."""
    rows = []
    with open(path, "r", encoding="utf-8-sig") as f:
        for lineno, line in enumerate(f, 1):
            if line.strip() == "":
                continue
            try:
                obj = json.loads(line)
            except ValueError:
                obj = None
            if not isinstance(obj, dict):
                rows.append({"_line": lineno, "_reject": "line is not a JSON object"})
                continue
            rows.append(jsonl_row(obj, lineno))
    return rows


def read_manifest(src):
    path = os.path.join(src, "manifest.jsonl")
    if os.path.exists(path):
        return read_jsonl(path)
    path = os.path.join(src, "manifest.json")
    if os.path.exists(path):
        data = load_json(path)
        if isinstance(data, dict):
            data = data.get("plans", data.get("items", []))
        return [r for r in (data or []) if isinstance(r, dict)]
    path = os.path.join(src, "manifest.csv")
    if os.path.exists(path):
        with open(path, "r", encoding="utf-8-sig", newline="") as f:
            return [dict(r) for r in csv.DictReader(f)]
    return None


def list_svgs(src):
    out = []
    for root, _, files in os.walk(src):
        for n in files:
            if n.lower().endswith(".svg"):
                out.append(os.path.relpath(os.path.join(root, n), src).replace(os.sep, "/"))
    return sorted(out)


def draft_manifest(src):
    rows = []
    for rel in list_svgs(src):
        stem = os.path.splitext(os.path.basename(rel))[0]
        rows.append({"file": rel, "venueId": "", "seatingPlanName": stem.replace("_", " ").replace("-", " ").strip(),
                     "seatingPlanId": "", "venueName": "", "cityName": "", "countryName": ""})
    path = os.path.join(src, "manifest.draft.json")
    save_json(path, rows)
    return path, len(rows)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        h.update(f.read())
    return h.hexdigest()


def blank(v):
    return v is None or str(v).strip() == ""


def inventory_lookup(lib):
    inv = os.path.join(lib, "inventory")
    venues = {str(v["venueId"]): v for v in (load_json(os.path.join(inv, "venues.json"), []) or []) if "venueId" in v}
    countries = {str(c["countryId"]): c.get("countryName") for c in (load_json(os.path.join(inv, "countries.json"), []) or [])
                 if "countryId" in c}
    cities = {str(c["cityId"]): c.get("cityName") for c in (load_json(os.path.join(inv, "cities.json"), []) or [])
              if "cityId" in c}
    return venues, countries, cities


def venue_data(row, venues, countries, cities):
    """Returns the venue fields or None when they cannot be worked out."""
    vid = row.get("venueId")
    v = venues.get(str(vid).strip()) if not blank(vid) else None
    if v is not None:
        return {"venueId": v["venueId"], "venueName": v.get("venueName"), "address": v.get("address"),
                "geoLat": v.get("geoLat"), "geoLon": v.get("geoLon"),
                "countryId": v.get("countryId"), "countryName": countries.get(str(v.get("countryId"))) or row.get("countryName"),
                "cityId": v.get("cityId"), "cityName": cities.get(str(v.get("cityId"))) or row.get("cityName")}
    names = [str(row.get(k) or "").strip() for k in ("venueName", "cityName", "countryName")]
    if not all(names):
        return None
    if blank(vid):
        vid = "ed-" + hashlib.sha256("|".join(n.lower() for n in names).encode("utf-8")).hexdigest()[:8]
    else:
        vid = str(vid).strip()
    return {"venueId": vid, "venueName": names[0], "address": row.get("address") or None,
            "geoLat": None, "geoLon": None,
            "countryId": row.get("countryId") or "ed", "countryName": names[2],
            "cityId": row.get("cityId") or "ed", "cityName": names[1]}


def is_general_admission(row):
    name = str(row.get("seatingPlanName") or "")
    note = str(row.get("note") or "").lower()
    return bool(GA_NAME_RE.search(name)) or "category table only" in note or "without seats" in note


def route_status(row, report):
    """Routes a manifest line that is not a plan to copy. Returns True when the line was routed."""
    status = str(row.get("manifestStatus") or "").strip().lower()
    if status in ("", "saved"):
        return False
    vid, vname = row.get("venueId"), row.get("venueName")
    if status == "no_picture":
        entry = {"venueId": vid, "venueName": vname, "seatingPlanId": row.get("seatingPlanId"),
                 "planName": row.get("seatingPlanName"), "note": row.get("note")}
        report["noPicture"].append(entry)
        if is_general_admission(row):
            report["generalAdmission"].append(entry)
    elif status == "no_plan":
        report["noPlan"].append({"venueId": vid, "venueName": vname})
    elif status == "skipped_test":
        report["skippedTest"].append({"venueId": vid, "venueName": vname, "skippedPlans": row.get("skippedPlans")})
    else:
        report["notCopied"].append({"file": row.get("file") or None, "reason": "manifest status %s" % status})
    return True


def ingest(lib, src):
    rows = read_manifest(src)
    if rows is None:
        print("error: no manifest.jsonl, manifest.json or manifest.csv in %s" % src, file=sys.stderr)
        return None
    venues, countries, cities = inventory_lookup(lib)
    report = {"ingested": [], "unchanged": [], "notCopied": [], "noPicture": [], "noPlan": [],
              "skippedTest": [], "generalAdmission": []}
    listed = set()
    for row in rows:
        if not row.get("_reject") and route_status(row, report):
            continue
        rel = norm_rel(row.get("file")) if not blank(row.get("file")) else ""
        if rel:
            listed.add(rel)
        if row.get("_reject"):
            entry = {"file": rel or None, "reason": row["_reject"]}
            if "_line" in row:
                entry["line"] = row["_line"]
            report["notCopied"].append(entry)
            continue
        problem = file_problem(src, rel) if rel else None
        if problem:
            report["notCopied"].append({"file": rel, "reason": problem, "seatingPlanName": row.get("seatingPlanName")})
            continue
        if not rel or not os.path.isfile(os.path.join(src, rel)):
            report["notCopied"].append({"file": rel or None, "reason": "manifest line without file",
                                        "seatingPlanName": row.get("seatingPlanName")})
            continue
        name = str(row.get("seatingPlanName") or "").strip()
        if not name:
            report["notCopied"].append({"file": rel, "reason": "seatingPlanName missing in manifest"})
            continue
        if TEST_RE.search(name):
            report["notCopied"].append({"file": rel, "reason": "test plan"})
            continue
        vd = venue_data(row, venues, countries, cities)
        if vd is None:
            report["notCopied"].append({"file": rel, "reason": "venue, city or country not known"})
            continue
        if is_russian(vd["countryName"]):
            report["notCopied"].append({"file": rel, "reason": "russian venue"})
            continue
        file_path = os.path.join(src, rel)
        sha = sha256_file(file_path)
        pid = str(row.get("seatingPlanId") or "").strip() or "ed-" + sha[:12]
        rec = {"seatingPlanId": pid, "seatingPlanName": name,
               "venueId": vd["venueId"], "venueName": vd["venueName"], "address": vd["address"],
               "geoLat": vd["geoLat"], "geoLon": vd["geoLon"],
               "countryId": vd["countryId"], "countryName": vd["countryName"],
               "cityId": vd["cityId"], "cityName": vd["cityName"],
               "actionEventId": None, "sourceUrl": "forzum-editor:" + rel, "sha256": sha,
               "seatCount": 0, "fetchedAt": now(), "status": "downloaded", "reason": "", "source": "editor"}
        if not blank(row.get("version")):
            rec["version"] = str(row["version"]).strip()
        d = plan_dir(lib, rec)
        path = os.path.join(d, "plan.json")
        old = load_json(path)
        if isinstance(old, dict) and old.get("sha256") == sha and os.path.exists(os.path.join(d, "source.svg")):
            report["unchanged"].append({"file": rel, "plan": pid})
            continue
        try:
            rec["seatCount"] = count_seats(file_path)
        except ET.ParseError:
            report["notCopied"].append({"file": rel, "reason": "svg cannot be read"})
            continue
        os.makedirs(d, exist_ok=True)
        shutil.copy2(file_path, os.path.join(d, "source.svg"))
        save_json(path, rec)
        report["ingested"].append({"file": rel, "plan": pid})
    for rel in list_svgs(src):
        if rel not in listed:
            report["notCopied"].append({"file": rel, "reason": "file not in manifest"})

    # same-hall dedupe among the editor plans on disk
    entries = []
    root_dir = os.path.join(lib, "plans")
    for root, _, files in os.walk(root_dir):
        if "plan.json" in files:
            p = os.path.join(root, "plan.json")
            r = load_json(p)
            if isinstance(r, dict) and r.get("source") == "editor":
                entries.append((p, r))
    entries.sort(key=lambda e: e[0])
    dedupe(entries, editor_pick, seats_of=editor_seats, same=editor_same)
    for p, r in entries:
        if r.get("status") == "skipped" and str(r.get("reason", "")).startswith("older version"):
            report.setdefault("olderVersions", []).append({"plan": r["seatingPlanId"], "reason": r["reason"]})
    rebuild_manifest(lib)
    report["generatedAt"] = now()
    save_json(os.path.join(src, "ingest-report.json"), report)
    return report


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
    p.add_argument("--from", dest="src", default=None, help="folder with the SVG files and the manifest")
    p.add_argument("--draft-manifest", action="store_true",
                   help="write manifest.draft.json from the file names and stop")
    a = p.parse_args(argv)
    src = a.src or os.path.join(a.lib, "editor-export")
    if not os.path.isdir(src):
        print("error: folder not found: %s" % src, file=sys.stderr)
        return 2
    if a.draft_manifest:
        path, n = draft_manifest(src)
        print("draft manifest: %d file(s) written to %s" % (n, path))
        return 0
    report = ingest(a.lib, src)
    if report is None:
        return 2
    print("ingest: %d new, %d unchanged, %d not copied, %d no picture (%d general admission), "
          "%d no plan, %d skipped test, %d older versions" % (
              len(report["ingested"]), len(report["unchanged"]), len(report["notCopied"]),
              len(report["noPicture"]), len(report["generalAdmission"]), len(report["noPlan"]),
              len(report["skippedTest"]), len(report.get("olderVersions", []))))
    return 0


if __name__ == "__main__":
    sys.exit(main())
