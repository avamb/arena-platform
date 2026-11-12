#!/usr/bin/env python3
"""Write <lib>/contact-sheet.html: every plan folder of the library on one page.

Grouped country -> city -> venue. A card shows house.svg (or source.svg when
there is no house.svg) through a relative <img src>, the names, ids, seat
count, convert status with its reasons. Plans that were skipped are listed
with their reason. Every text from the files is HTML-escaped.
"""
from __future__ import annotations

import argparse
import html
import json
import os
import sys
import urllib.parse
from pathlib import Path

DEFAULT_LIB = r"C:\Arena\forzum-library"

STYLE = """
body{font-family:sans-serif;margin:16px;background:#f4f4f4;color:#222}
h2{margin:24px 0 4px}h3{margin:12px 0 4px;color:#444}h4{margin:8px 0 4px;color:#666}
.cards{display:flex;flex-wrap:wrap;gap:12px}
.card{background:#fff;border:1px solid #ccc;border-radius:6px;padding:8px;width:340px}
.card img{width:100%;height:220px;object-fit:contain;background:#fafafa;border:1px solid #eee}
.noimg{height:60px;color:#888}
.st{font-weight:bold;padding:1px 6px;border-radius:3px}
.st-ok{background:#c8f0c8}.st-needs_review{background:#ffe9a8}.st-failed{background:#f5b5b5}
.st-skipped,.st-not_converted{background:#ddd}
.meta{font-size:12px;color:#555;word-break:break-all}
button{margin-right:6px;padding:4px 10px}
button.on{background:#333;color:#fff}
"""

SCRIPT = """
var cur='all';
function setFilter(s){cur=s;
 var bs=document.querySelectorAll('button[data-f]');
 for(var i=0;i<bs.length;i++){bs[i].className=bs[i].getAttribute('data-f')===s?'on':'';}
 var cs=document.querySelectorAll('.card');
 for(var j=0;j<cs.length;j++){var c=cs[j];
  c.style.display=(s==='all'||c.getAttribute('data-status')===s)?'':'none';}
}
"""


def e(x) -> str:
    return html.escape("" if x is None else str(x), quote=True)


def _read(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return None


def collect(lib: Path):
    items = []
    for pj in sorted((lib / "plans").rglob("plan.json")):
        folder = pj.parent
        plan = _read(pj)
        if not isinstance(plan, dict):
            continue
        conv = _read(folder / "convert.json")
        conv = conv if isinstance(conv, dict) else None
        if conv and conv.get("status"):
            status = str(conv["status"])
            reasons = conv.get("reasons") or []
        else:
            status = str(plan.get("status") or "not_converted")
            reasons = []
        if status == "downloaded":
            status = "not_converted"
        if plan.get("status") == "skipped" and not conv:
            status = "skipped"
        if plan.get("reason"):
            reasons = list(reasons) + [plan["reason"]] if status in ("skipped", "failed") else reasons
        img = None
        for name in ("house.svg", "source.svg"):
            if (folder / name).is_file():
                img = name
                break
        rel = None
        if img:
            rel = "/".join(urllib.parse.quote(p) for p in (folder / img).relative_to(lib).parts)
        items.append({"plan": plan, "status": status, "reasons": reasons, "img": rel, "img_name": img})
    return items


def render(items) -> str:
    statuses = sorted({i["status"] for i in items})
    tree = {}
    for it in items:
        p = it["plan"]
        tree.setdefault(str(p.get("countryName") or "?"), {}).setdefault(str(p.get("cityName") or "?"), {}).setdefault(
            (str(p.get("venueName") or "?"), str(p.get("venueId") or "")), []
        ).append(it)
    out = ["<!DOCTYPE html>", '<html><head><meta charset="utf-8"><title>Forzum plan contact sheet</title>',
           f"<style>{STYLE}</style></head><body>", f"<h1>Forzum plans: {len(items)}</h1>", "<div>"]
    out.append('<button type="button" class="on" data-f="all" onclick="setFilter(\'all\')">all</button>')
    for s in statuses:
        n = sum(1 for i in items if i["status"] == s)
        out.append(f'<button type="button" data-f="{e(s)}" onclick="setFilter(this.getAttribute(\'data-f\'))">{e(s)} ({n})</button>')
    out.append("</div>")
    for country in sorted(tree):
        out.append(f"<h2>{e(country)}</h2>")
        for city in sorted(tree[country]):
            out.append(f"<h3>{e(city)}</h3>")
            for (vname, vid) in sorted(tree[country][city]):
                out.append(f"<h4>{e(vname)} <span class=\"meta\">venue {e(vid)}</span></h4><div class=\"cards\">")
                for it in tree[country][city][(vname, vid)]:
                    p = it["plan"]
                    st = it["status"]
                    out.append(f'<div class="card" data-status="{e(st)}">')
                    if it["img"]:
                        out.append(f'<img loading="lazy" src="{e(it["img"])}" alt="{e(p.get("seatingPlanName"))}">')
                        out.append(f'<div class="meta">{e(it["img_name"])}</div>')
                    else:
                        out.append('<div class="noimg">no picture</div>')
                    out.append(f"<div><b>{e(p.get('seatingPlanName'))}</b></div>")
                    out.append(f'<div class="meta">plan {e(p.get("seatingPlanId"))} / venue {e(p.get("venueName"))} {e(p.get("venueId"))}</div>')
                    out.append(f'<div>seats: {e(p.get("seatCount"))}</div>')
                    out.append(f'<div>status: <span class="st st-{e(st)}">{e(st)}</span></div>')
                    if it["reasons"]:
                        out.append("<ul>" + "".join(f"<li>{e(r)}</li>" for r in it["reasons"]) + "</ul>")
                    out.append("</div>")
                out.append("</div>")
    out.append(f"<script>{SCRIPT}</script></body></html>")
    return "\n".join(out) + "\n"


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="Write <lib>/contact-sheet.html")
    ap.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
    args = ap.parse_args(argv)
    lib = Path(args.lib)
    items = collect(lib)
    target = lib / "contact-sheet.html"
    target.write_text(render(items), encoding="utf-8")
    print(f"{len(items)} plans -> {target}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
