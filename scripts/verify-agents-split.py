#!/usr/bin/env python3
"""Prove that the AGENTS.md split lost nothing and added nothing.

One-off check for the change that cut the single 580 KB AGENTS.md into an index
plus docs/agents/*.md. Run it from the repo root against the commit that still
holds the old file:

    python -I scripts/verify-agents-split.py --old-rev 890e20f2

It exits 1 on the first violated rule. Rules:
  1. every item of the old AGENTS.md (a `- ` line plus its indented continuation
     lines) occurs byte for byte in exactly one new file, and nowhere else;
  2. the only old items that do not survive are the listed exact duplicates;
  3. no new file holds an item that the old file did not have;
  4. a topic file contains nothing but its header and items (no stray lines);
  5. every topic file is linked from the AGENTS.md index, every link points at
     an existing file, and the index lists exactly one line per item.
"""
import argparse
import collections
import re
import subprocess
import sys
from pathlib import Path

# Sections of AGENTS.md that are generated text, not old items.
GENERATED_SECTIONS = {"Always", "Topic index"}


def read_utf8(path):
    with open(path, encoding="utf-8", newline="") as f:
        return f.read()


def parse_items(text, skip_sections=(), where="file"):
    """Return (items, problems, prose). Items are column-0 `- ` lines with their
    two-space-indented continuation lines, joined with LF. `prose` holds every
    other non-blank, non-heading line (intro text, HTML comments)."""
    items, problems, prose = [], [], []
    cur = None
    section = None
    for n, line in enumerate(text.split("\n"), 1):
        if line.startswith("#"):
            if line.startswith("## "):
                section = line[3:].strip()
            cur = None
            continue
        if section in skip_sections:
            cur = None
            continue
        if line.startswith("- "):
            cur = [line]
            items.append(cur)
        elif line.startswith("  "):
            if cur is None:
                problems.append(f"{where}:{n}: indented line outside an item")
            else:
                cur.append(line)
        elif line.strip() == "":
            cur = None
        else:
            prose.append(line)
    return ["\n".join(i) for i in items], problems, prose


def title_of(item):
    m = re.match(r"- \*\*(.+?)\*\*", item, re.S)
    s = m.group(1) if m and len(m.group(1).strip("* ")) >= 8 else item[2:]
    s = re.sub(r"\s+", " ", s.replace("*", "").replace("`", "")).strip().rstrip(".:;, ")
    return s


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--old-rev", required=True, help="commit holding the old AGENTS.md")
    args = ap.parse_args()
    fail = []

    old_text = subprocess.run(
        ["git", "show", f"{args.old_rev}:AGENTS.md"], check=True, capture_output=True
    ).stdout.decode("utf-8")
    old_items, probs, old_prose = parse_items(old_text, where="old AGENTS.md")
    fail += probs
    print(f"old non-item prose lines (rewritten or dropped on purpose): {len(old_prose)}")
    for line in old_prose:
        if line.startswith("<!--"):
            print("  dropped placeholder:", line)
    print(f"old AGENTS.md: {len(old_text.encode('utf-8'))} bytes, {len(old_items)} items")

    old_count = collections.Counter(old_items)
    dup_texts = {t: c for t, c in old_count.items() if c > 1}
    print(f"exact duplicates in old file: {len(dup_texts)}")
    for t in dup_texts:
        print("  duplicated item kept once:", title_of(t)[:100])

    expected = collections.Counter({t: 1 for t in old_count})  # each distinct item once

    new_files = [Path("AGENTS.md")] + sorted(Path("docs/agents").glob("*.md"))
    where = collections.defaultdict(list)  # item -> [files]
    per_file_items = {}
    for p in new_files:
        text = read_utf8(p)
        skip = GENERATED_SECTIONS if p.name == "AGENTS.md" else ()
        items, probs, prose = parse_items(text, skip_sections=skip, where=str(p))
        fail += probs
        if p.name != "AGENTS.md" and len(prose) != 1:
            fail.append(f"{p}: expected exactly one description line besides items, found {len(prose)}")
        per_file_items[p] = items
        for it in items:
            where[it].append(str(p))
        print(f"{str(p):52s} {len(text.encode('utf-8')):7d} bytes  {len(items):3d} items")

    new_count = collections.Counter({t: len(f) for t, f in where.items()})
    lost = [t for t in expected if new_count[t] == 0]
    twice = [t for t in new_count if new_count[t] > 1]
    extra = [t for t in new_count if t not in expected]
    for t in lost:
        fail.append("LOST: " + title_of(t)[:100])
    for t in twice:
        fail.append(f"IN {new_count[t]} FILES {where[t]}: " + title_of(t)[:80])
    for t in extra:
        fail.append(f"ADDED (not in old file): {title_of(t)[:100]} in {where[t]}")
    total_new = sum(new_count.values())
    print(
        f"distinct old items {len(expected)}, items now {total_new}, "
        f"lost {len(lost)}, in several files {len(twice)}, added {len(extra)}"
    )
    removed = len(old_items) - total_new
    print(f"removed duplicates: {removed} (expected {sum(c - 1 for c in dup_texts.values())})")
    if removed != sum(c - 1 for c in dup_texts.values()):
        fail.append("number of removed items differs from the number of exact duplicates")

    # 4: topic file layout = '# title', description paragraph, then items only.
    for p in new_files[1:]:
        head = read_utf8(p).split("\n- ", 1)[0]
        # head is "# title\n\ndescription\n" (the split ate the blank line before "- ")
        if not head.startswith("# ") or head.count("\n\n") != 1 or head.count("\n") != 3:
            fail.append(f"{p}: unexpected header block")

    # 5: index.
    agents = read_utf8("AGENTS.md")
    idx = agents.split("## Topic index", 1)[1]
    links = re.findall(r"^### \[(.+?)\]\((docs/agents/[^)]+)\)", idx, re.M)
    linked = {l[1] for l in links}
    for _, link in links:
        if not Path(link).is_file():
            fail.append(f"index link to a missing file: {link}")
    for p in new_files[1:]:
        if p.as_posix() not in linked:
            fail.append(f"topic file not in the index: {p}")
    bullets = re.findall(r"^\* (.+)$", idx, re.M)
    wanted = sum(len(per_file_items[p]) for p in new_files[1:])
    print(f"index: {len(links)} topic links, {len(bullets)} title lines (topic items: {wanted})")
    if len(bullets) != wanted:
        fail.append("index title lines != topic items")
    # each title line must be the (possibly shortened) title of an item of the same topic
    sections = re.split(r"^### ", idx, flags=re.M)[1:]
    for sec in sections:
        m = re.match(r"\[.+?\]\((docs/agents/[^)]+)\)", sec)
        titles = [b[2:] for b in sec.split("\n") if b.startswith("* ")]
        mine = [title_of(i) for i in per_file_items[Path(m.group(1))]]
        for tl in titles:
            stem = tl.rstrip("…").rstrip()
            if not any(t.replace("`", "").startswith(stem[:60]) or stem in t for t in mine):
                fail.append(f"index line without an item in {m.group(1)}: {tl[:80]}")

    if fail:
        print("\nFAILED:")
        for f in fail:
            print(" -", f)
        sys.exit(1)
    print("\nOK: nothing lost, nothing added, no item in two files, index consistent")


if __name__ == "__main__":
    main()
