"""Merge the JSON blocks from the runner discovery runs into one list of pages per platform.

Reads ops/claude-sdk-runner/runs/legal-pages-*/result.md, keeps every platform entry that has at least one page,
merges duplicate platforms (union of pages, de-duplicated by URL) and writes
C:\\Projects\\arena_private_notes\\legal_pages_raw\\discovery\\merged.json
"""
import json
import pathlib
import re
import sys

RUNS = pathlib.Path(r"C:\Projects\arena_new\ops\claude-sdk-runner\runs")
OUT = pathlib.Path(r"C:\Projects\arena_private_notes\legal_pages_raw\discovery\merged.json")


def blocks(text):
    for m in re.finditer(r"```json\s*(.*?)```", text, re.S):
        try:
            yield json.loads(m.group(1))
        except json.JSONDecodeError:
            print("  skipped an invalid JSON block", file=sys.stderr)


def main():
    merged = {}
    for run in sorted(RUNS.glob("legal-pages-*")):
        res = run / "result.md"
        if not res.exists():
            print(f"{run.name}: no result yet")
            continue
        for doc in blocks(res.read_text(encoding="utf-8")):
            for plat in doc.get("platforms", []):
                name = plat.get("platform", "").strip()
                if not name:
                    continue
                cur = merged.setdefault(name, {
                    "platform": name, "website": plat.get("website", ""), "pages": [],
                    "who_authors_terms": "not_found", "terms_placement_in_flow": "not_found", "gaps": []})
                urls = {p["url"] for p in cur["pages"]}
                for page in plat.get("pages", []):
                    if page.get("url") and page["url"] not in urls:
                        cur["pages"].append(page)
                        urls.add(page["url"])
                if plat.get("who_authors_terms", "not_found") != "not_found":
                    cur["who_authors_terms"] = plat["who_authors_terms"]
                if plat.get("terms_placement_in_flow", "not_found") != "not_found":
                    cur["terms_placement_in_flow"] = plat["terms_placement_in_flow"]
                cur["gaps"].extend(plat.get("gaps", []))
        print(f"{run.name}: read")
    OUT.parent.mkdir(parents=True, exist_ok=True)
    platforms = sorted(merged.values(), key=lambda p: p["platform"].lower())
    OUT.write_text(json.dumps({"platforms": platforms}, ensure_ascii=False, indent=1), encoding="utf-8")
    total = sum(len(p["pages"]) for p in platforms)
    empty = [p["platform"] for p in platforms if not p["pages"]]
    print(f"platforms: {len(platforms)}, pages: {total}, without pages: {empty}")


if __name__ == "__main__":
    main()
