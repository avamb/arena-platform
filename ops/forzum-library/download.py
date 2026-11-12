"""Step 3: download source.svg and GET_SCHEMA for each distinct plan, then dedupe.

Read-only. Resumable: a plan whose plan.json is 'downloaded' or 'skipped' is not fetched again.
"""

import argparse
import hashlib
import os
import re
import sys
from datetime import datetime, timezone
from functools import cmp_to_key

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # works under python -I
from forzum_client import (DEFAULT_APIS, Client, TokenError, add_common_args,
                           choose_api, is_russian, load_json, save_json, saved_api, validate_apis, walk)

TEST_RE = re.compile(r"test|тест", re.IGNORECASE)
VERSION_RE = re.compile(r"(\bv(er)?\.?\s*\d+(\.\d+)*\b|\bверсия\s*\d+\b|\(\s*\d+\s*\)|\b(new|old|нов\w*|стар\w*)\b)", re.IGNORECASE)


def _is_int(v):
    if isinstance(v, bool):
        return False
    if isinstance(v, int):
        return True
    return isinstance(v, str) and re.fullmatch(r"\s*\d+\s*", v) is not None


def compare_plan_ids(a, b):
    """Numeric when both are integers (1000 > 999), else as text."""
    if _is_int(a) and _is_int(b):
        x, y = int(a), int(b)
    else:
        x, y = str(a), str(b)
    return (x > y) - (x < y)


def safe(name, ident):
    s = re.sub(r"[^\w\-]+", "-", str(name or "x"), flags=re.UNICODE).strip("-") or "x"
    return "%s_%s" % (s, ident)


def seat_set(schema):
    out = set()
    for d in walk(schema or {}):
        if d.get("seatId") is None and d.get("seat_id") is None:
            continue
        sector = d.get("sector", d.get("sectorName"))
        row = d.get("row", d.get("rowName"))
        num = d.get("number", d.get("seat", d.get("seatNumber")))
        out.add((str(sector), str(row), str(num)))
    return out


def norm_name(name):
    return re.sub(r"\s+", " ", VERSION_RE.sub(" ", str(name or "")).lower()).strip(" -_.")


def plan_dir(lib, p):
    return os.path.join(lib, "plans", safe(p["countryName"], p["countryId"]), safe(p["cityName"], p["cityId"]),
                        str(p["venueId"]), str(p["seatingPlanId"]))


def now():
    return datetime.now(timezone.utc).isoformat()


def base_record(p, status, reason=""):
    return {"seatingPlanId": p["seatingPlanId"], "seatingPlanName": p["seatingPlanName"],
            "venueId": p["venueId"], "venueName": p["venueName"], "address": p.get("address"),
            "geoLat": p.get("geoLat"), "geoLon": p.get("geoLon"),
            "countryId": p["countryId"], "countryName": p["countryName"],
            "cityId": p["cityId"], "cityName": p["cityName"],
            "actionEventId": p["newestActionEventId"], "sourceUrl": None, "sha256": None,
            "seatCount": 0, "fetchedAt": now(), "status": status, "reason": reason}


def fetch_plan(c, p, d):
    rec = base_record(p, "failed")
    aeid = p["newestActionEventId"]
    svg, url = c.fetch_image(aeid)
    if svg is None:
        rec["reason"] = "plan picture not received"
        return rec
    schema = c.call("GET_SCHEMA", actionEventId=aeid)
    if schema is None:
        rec["reason"] = "GET_SCHEMA failed"
        return rec
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, "source.svg"), "wb") as f:
        f.write(svg)
    save_json(os.path.join(d, "schema.json"), schema)
    rec.update(sourceUrl=url, sha256=hashlib.sha256(svg).hexdigest(), seatCount=len(seat_set(schema)),
               status="downloaded", reason="")
    return rec


def rebuild_manifest(lib):
    """Rebuild <lib>/plans/manifest.json from every plan.json on disk. Returns the list of items."""
    root_dir = os.path.join(lib, "plans")
    found = []
    for root, _, files in os.walk(root_dir):
        if "plan.json" in files:
            path = os.path.join(root, "plan.json")
            rec = load_json(path)
            if isinstance(rec, dict):
                found.append((os.path.relpath(path, root_dir).replace(os.sep, "/"), rec))
    found.sort(key=lambda x: x[0])
    items = [{"plan": m, "seatingPlanId": r.get("seatingPlanId"), "status": r.get("status"),
              "reason": r.get("reason")} for m, r in found]
    save_json(os.path.join(root_dir, "manifest.json"),
              {"generatedAt": now(), "plans": [m for m, _ in found], "items": items})
    return items


def dedupe(entries, pick=None):
    """entries: list of (plan.json path, record). Keep the newest of each same-hall group.

    pick(group) -> (newest item of the group, rule text); the default keeps the highest seatingPlanId.
    """
    by_venue = {}
    for path, rec in entries:
        if rec["status"] == "downloaded":
            by_venue.setdefault(rec["venueId"], []).append((path, rec))
    for items in by_venue.values():
        info = []
        for path, rec in items:
            seats = seat_set(load_json(os.path.join(os.path.dirname(path), "schema.json"), {}))
            info.append((path, rec, seats, norm_name(rec["seatingPlanName"])))
        parent = list(range(len(info)))

        def find(i):
            while parent[i] != i:
                i = parent[i]
            return i

        for i in range(len(info)):
            for j in range(i + 1, len(info)):
                same = (info[i][2] and info[i][2] == info[j][2]) or (info[i][3] and info[i][3] == info[j][3])
                if same:
                    parent[find(i)] = find(j)
        groups = {}
        for i in range(len(info)):
            groups.setdefault(find(i), []).append(info[i])
        for g in groups.values():
            if len(g) < 2:
                continue
            if pick is None:
                newest, rule = max(g, key=cmp_to_key(lambda x, y: compare_plan_ids(x[1]["seatingPlanId"], y[1]["seatingPlanId"]))), ""
            else:
                newest, rule = pick(g)
            for path, rec, _, _ in g:
                if rec is newest[1]:
                    continue
                rec["status"] = "skipped"
                rec["reason"] = "older version of %s" % newest[1]["seatingPlanId"] + (" (%s)" % rule if rule else "")
                save_json(path, rec)


def main(argv=None):
    a = add_common_args(argparse.ArgumentParser(description=__doc__)).parse_args(argv)
    apis = a.api or DEFAULT_APIS
    if not validate_apis(apis):
        return 2
    c = Client(apis[0], a.fid, a.locale, a.token_file, a.pause_ms, a.backoff_base)
    try:
        c.check_token()
        plans =load_json(os.path.join(a.lib, "inventory", "plans.json"))
        if plans is None:
            print("error: inventory/plans.json not found, run inventory.py first", file=sys.stderr)
            return 2
        if choose_api(c, apis, saved_api(a.lib)) is None:
            print("error: no API address answers GET_COUNTRIES with resultCode 0", file=sys.stderr)
            return 2
        if a.only_venue is not None:
            plans = [p for p in plans if p["venueId"] == a.only_venue]
        if a.limit is not None:
            plans = plans[: a.limit]
        entries = []
        for p in plans:
            d = plan_dir(a.lib, p)
            path = os.path.join(d, "plan.json")
            rec = load_json(path)
            if rec is None or rec.get("status") == "failed":
                if is_russian(p.get("countryName")):
                    rec = base_record(p, "skipped", "Russia")
                    save_json(path, rec)
                elif TEST_RE.search(str(p.get("seatingPlanName") or "")):
                    rec = base_record(p, "skipped", "test plan")
                    save_json(path, rec)
                else:
                    rec = fetch_plan(c, p, d)
                    save_json(path, rec)
            entries.append((path, rec))
        dedupe(entries)
        rebuild_manifest(a.lib)
        counts = {}
        for _, r in entries:
            counts[r["status"]] = counts.get(r["status"], 0) + 1
        print("download: %s, failed calls %d" % (counts, len(c.failed)))
    except TokenError as e:
        print("error: %s" % e, file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
