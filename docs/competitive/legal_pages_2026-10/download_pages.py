"""Download the public pages found by the discovery runs.

Reads discovery/<group>.json (made from the runner result.md files), downloads every URL with a plain HTTP request
first and falls back to the Bright Data unlocker when the site blocks or serves a challenge page. The Bright Data
token is read from the Claude Desktop config at run time and is never printed or written anywhere.

Output (outside the public repo): <raw>/<platform_slug>/NN_<kind>.html, NN_<kind>.txt and manifest.json
"""
import hashlib
import json
import os
import pathlib
import re
import sys
import time
import urllib.parse

import requests
from bs4 import BeautifulSoup

RAW = pathlib.Path(r"C:\Projects\arena_private_notes\legal_pages_raw")
DISC = RAW / "discovery"
UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) "
      "Chrome/124.0 Safari/537.36")
BLOCK_MARKERS = ("just a moment", "cf-chl", "attention required", "access denied", "captcha", "enable javascript")
MAX_PER_PLATFORM = 12


def slug(text):
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-") or "x"


def bright_token():
    cfg = pathlib.Path(os.path.expandvars(r"%APPDATA%\Claude\claude_desktop_config.json"))
    data = json.loads(cfg.read_text(encoding="utf-8"))
    env = data["mcpServers"]["bright-data"]["env"]
    return env["API_TOKEN"], env.get("WEB_UNLOCKER_ZONE", "web_unlocker1")


def looks_blocked(status, body):
    if status != 200 or len(body) < 1500:
        return True
    head = body[:6000].lower()
    return any(m in head for m in BLOCK_MARKERS)


def fetch_direct(url):
    r = requests.get(url, headers={"User-Agent": UA, "Accept-Language": "en,*;q=0.5"}, timeout=25)
    r.encoding = r.encoding or "utf-8"
    return r.status_code, r.text


def fetch_bright(url, token, zone):
    r = requests.post(
        "https://api.brightdata.com/request",
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        json={"zone": zone, "url": url, "format": "raw"},
        timeout=90,
    )
    return r.status_code, r.text


def to_text(html):
    soup = BeautifulSoup(html, "lxml")
    for tag in soup(["script", "style", "noscript", "svg"]):
        tag.decompose()
    title = (soup.title.string or "").strip() if soup.title else ""
    text = soup.get_text("\n")
    text = re.sub(r"\n\s*\n+", "\n\n", text).strip()
    return title, text


def main():
    token = zone = None
    manifest = []
    for disc_file in sorted(DISC.glob("*.json")):
        group = json.loads(disc_file.read_text(encoding="utf-8"))
        for plat in group.get("platforms", []):
            pdir = RAW / slug(plat["platform"])
            pdir.mkdir(parents=True, exist_ok=True)
            seen = set()
            n = 0
            for page in plat.get("pages", []):
                url = page.get("url", "").strip()
                if not url.startswith("http") or url in seen or n >= MAX_PER_PLATFORM:
                    continue
                seen.add(url)
                n += 1
                base = f"{slug(page.get('kind', 'page'))}_{hashlib.sha1(url.encode()).hexdigest()[:8]}"
                entry = {"platform": plat["platform"], "kind": page.get("kind"), "url": url, "file": base}
                if (pdir / f"{base}.txt").exists():
                    entry.update(method="cached", status=200)
                    manifest.append(entry)
                    continue
                try:
                    status, body = fetch_direct(url)
                    method = "direct"
                    if looks_blocked(status, body):
                        if token is None:
                            token, zone = bright_token()
                        status, body = fetch_bright(url, token, zone)
                        method = "brightdata"
                    entry.update(method=method, status=status, bytes=len(body))
                    if status == 200 and body:
                        title, text = to_text(body)
                        (pdir / f"{base}.html").write_text(body, encoding="utf-8")
                        (pdir / f"{base}.txt").write_text(f"{url}\n{title}\n\n{text}", encoding="utf-8")
                        entry.update(title=title, text_chars=len(text),
                                     sha256=hashlib.sha256(body.encode("utf-8", "replace")).hexdigest()[:16])
                    else:
                        entry["error"] = f"http {status}"
                except Exception as exc:  # network or parsing failure
                    entry["error"] = type(exc).__name__ + ": " + str(exc)[:120]
                manifest.append(entry)
                print(f"{plat['platform'][:18]:18} {entry.get('method', '-'):10} {entry.get('status', '-')}  {url[:90]}")
                time.sleep(1.0)
    (RAW / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=1), encoding="utf-8")
    ok = sum(1 for e in manifest if e.get("status") == 200)
    print(f"done: {ok}/{len(manifest)} pages saved")


if __name__ == "__main__":
    sys.exit(main())
