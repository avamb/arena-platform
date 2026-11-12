#!/usr/bin/env python3
"""Load converted Forzum hall plans into one arena server (README, "Loader").

KNOWN GAP (checked 2026-11-12 against apps/backend/openapi/openapi.yaml and
08_architecture/64 section 3.4): the API has NO way to create a plan with a
"verification" mark. A plan and its venue are unverified by default:
`seating_plan.verify` and `POST /v1/admin/seating-plans/{id}/verify` and
`PATCH /v1/admin/venues/{id}/review` belong to the platform operator and this
tool never calls them. So the loader uploads the normal way (plan in status
`draft`, visibility `private`, owned by --org) and publishes and verifies
nothing. Nobody can tell a library plan from any other draft plan except by
its name until the operator verifies it by hand.

Modes: `plans` (also the bare form with no subcommand), `venues` (every
inventory venue, with or without a plan; `--mark-verified` is the ONLY place
that calls PATCH /v1/admin/venues/{id}/review, on by default for venues by
the owner's decision of 2026-11-12, `--no-mark-verified` turns it off) and
`venues-report` (offline, no login). Venues mode also uses
POST /v1/organizations/{org}/cities {country_id, name, locale} (idempotent by
name, needs city.create) and PATCH /v1/admin/venues/{id}/review
{review_status} with the header X-Admin-Reason.

Routes used (all JSON, bearer token after the login):
  POST /v1/auth/login                      {email, password}
  POST /v1/auth/refresh                    {refresh_token}   (token lives 15 min)
  GET  /v1/geo/countries                   (public) iso2, name
  GET  /v1/geo/cities                      (public) id, country_iso2, name, timezone
  GET  /v1/organizations/{org}/venues      venue list of the organization
  POST /v1/organizations/{org}/venues      {name, city_id, country, address_line1,
                                            geo_lat, geo_lng, timezone}
  GET  /v1/venues/{venue}/seating-plans    plans of the venue
  POST /v1/venues/{venue}/seating-plans    {owner_org_id, name, plan_type}
  POST /v1/seating-plans/{plan}/versions   {svg}

Time zone: the server does not derive it from coordinates. It is taken, in this
order, from `--timezones <json>` (Forzum cityId -> IANA zone) or from the
arena city (`timezone` of GET /v1/geo/cities). A venue with neither is skipped
and reported. A plan whose country or city is unknown to the arena geo tables
is skipped too (the tool creates no city).

The e-mail is read with input() and the password with getpass at run time.
Neither is stored, printed or written. The token stays in memory.
"""
from __future__ import annotations

import argparse
import datetime
import getpass
import json
import os
import re
import sys
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

DEFAULT_LIB = r"C:\Arena\forzum-library"
GO_AHEAD_FLAG = "--i-have-the-owners-go-ahead"


class ApiError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(f"HTTP {status}: {message}")
        self.status = status
        self.message = message


def norm(text) -> str:
    """Case-, whitespace- and punctuation-insensitive form of a name."""
    s = unicodedata.normalize("NFKC", str(text or "")).casefold()
    return re.sub(r"[\W_]+", " ", s).strip()


def host_is_staging(api: str) -> bool:
    host = urllib.parse.urlparse(api).hostname or ""
    return "staging" in host.lower()


USER_AGENT = "arena-forzum-loader/1.0"


class Client:
    def __init__(self, base: str, timeout: float = 60.0):
        self.base = base.rstrip("/")
        self.timeout = timeout
        self._access = None
        self._refresh = None

    def _do(self, method: str, path: str, body=None, auth: bool = True, headers=None):
        data = None if body is None else json.dumps(body).encode("utf-8")
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        # The default urllib agent is refused by the CDN in front of the API (403, code 1010).
        req.add_header("User-Agent", USER_AGENT)
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if auth and self._access:
            req.add_header("Authorization", "Bearer " + self._access)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as e:
            raw = e.read() or b""
            raise ApiError(e.code, _error_text(raw)) from None
        except urllib.error.URLError as e:
            raise ApiError(0, f"connection failed: {e.reason}") from None
        return json.loads(raw) if raw else {}

    def request(self, method: str, path: str, body=None, auth: bool = True, headers=None):
        try:
            return self._do(method, path, body, auth, headers)
        except ApiError as e:
            if e.status == 401 and auth and self._refresh:
                self._refresh_token()
                return self._do(method, path, body, auth, headers)
            raise

    def login(self, email: str, password: str) -> None:
        out = self._do("POST", "/v1/auth/login", {"email": email, "password": password}, auth=False)
        self._access = out["access_token"]
        self._refresh = out.get("refresh_token")

    def _refresh_token(self) -> None:
        out = self._do("POST", "/v1/auth/refresh", {"refresh_token": self._refresh}, auth=False)
        self._access = out["access_token"]
        self._refresh = out.get("refresh_token") or self._refresh


def _error_text(raw: bytes) -> str:
    try:
        d = json.loads(raw)
        err = d.get("error") or {}
        return f"{err.get('code', '')} {err.get('message', '')}".strip() or "error"
    except Exception:
        return "error"


def read_json(path: Path, default=None):
    try:
        return json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return default


def write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def find_plans(lib: Path):
    """Plan folders whose convert status is ok: (folder, plan.json dict)."""
    out = []
    root = lib / "plans"
    for pj in sorted(root.rglob("plan.json")):
        folder = pj.parent
        plan = read_json(pj)
        conv = read_json(folder / "convert.json")
        if not isinstance(plan, dict) or not isinstance(conv, dict):
            continue
        if conv.get("status") != "ok" or not (folder / "house.svg").is_file():
            continue
        if plan.get("status") not in (None, "downloaded"):
            continue
        if plan.get("seatingPlanId") in (None, ""):
            continue
        out.append((folder, plan))
    return out


def _now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class Loader:
    def __init__(self, client, org, lib, dry_run, timezones, out, pause_ms=0):
        self.c = client
        self.org = org
        self.lib = lib
        self.dry = dry_run
        self.tz = timezones
        self.out = out
        self.pause = pause_ms / 1000.0
        self.countries = {}  # norm key -> iso2
        self.cities = {}  # (iso2, norm name) -> city dict
        self.city_by_id = {}
        self.venues = []  # [{id, name, city_key}]
        self.country_ids = {}  # iso2 -> arena country uuid

    def say(self, msg):
        print(msg, file=self.out)

    def _sleep(self):
        if self.pause:
            time.sleep(self.pause)

    def load_reference(self):
        for c in self.c.request("GET", "/v1/geo/countries?lang=en", auth=False).get("countries", []):
            for k in (c.get("iso2"), c.get("iso3"), c.get("slug"), c.get("name")):
                if k:
                    self.countries[norm(k)] = c["iso2"].upper()
            if c.get("id"):
                self.country_ids[c["iso2"].upper()] = c["id"]
        for ci in self.c.request("GET", "/v1/geo/cities?lang=en", auth=False).get("cities", []):
            key = (ci["country_iso2"].upper(), norm(ci["name"]))
            self.cities.setdefault(key, ci)
            self.city_by_id[ci["id"]] = ci
        self.refresh_venues()

    def refresh_venues(self):
        res = self.c.request("GET", f"/v1/organizations/{self.org}/venues")
        self.venues = []
        for v in res.get("venues", []):
            ci = self.city_by_id.get(v.get("city_id") or "")
            key = (ci["country_iso2"].upper(), norm(ci["name"])) if ci else None
            self.venues.append({"id": v["id"], "name": v["name"], "city_key": key})

    def decide_venue(self, plan):
        """Return (action, venue_dict_or_None, candidates)."""
        want = norm(plan.get("venueName"))
        cands = [v for v in self.venues if norm(v["name"]) == want]
        if not cands:
            return "create", None, []
        key = self._city_key(plan)
        if len(cands) == 1 and cands[0]["city_key"] is not None and cands[0]["city_key"] == key:
            return "reuse", cands[0], cands
        return "ambiguous", None, cands

    def _city_key(self, plan):
        # The Forzum spelling first ("Bulgary"), through the same table the venues mode uses.
        iso = country_iso2(plan.get("countryName")) or self.countries.get(norm(plan.get("countryName")))
        if not iso:
            return None
        return (iso, norm(plan.get("cityName")))

    def process(self, folder: Path, plan: dict) -> dict:
        pid = str(plan["seatingPlanId"])
        rec = {"seatingPlanId": pid, "venueId": None, "venueAction": None, "seatingPlanUuid": None, "reason": "", "at": _now()}
        key = self._city_key(plan)
        if key is None:
            return {**rec, "status": "skipped", "reason": "country not in arena geo"}
        city = self.cities.get(key)
        if city is None:
            return {**rec, "status": "skipped", "reason": "city not in arena geo"}
        action, venue, cands = self.decide_venue(plan)
        rec["venueAction"] = action
        if action == "ambiguous":
            rec["status"] = "ambiguous"
            rec["reason"] = f"{len(cands)} venue(s) with the same name in the organization, none is a single match in this city"
            rec["candidates"] = [{"id": v["id"], "name": v["name"]} for v in cands]
            return rec
        tz = self.tz.get(str(plan.get("cityId"))) or city.get("timezone")
        if action == "create" and not tz:
            return {**rec, "status": "skipped", "reason": "no timezone for city"}
        plan_name = str(plan.get("seatingPlanName") or f"Plan {pid}")
        if self.dry:
            rec["status"] = "dry-run"
            rec["reason"] = ("would create venue and " if action == "create" else "would reuse venue and ") + "upload plan"
            rec["venueId"] = venue["id"] if venue else None
            return rec
        try:
            if action == "create":
                body = {"name": plan.get("venueName"), "city_id": city["id"], "country": key[0], "timezone": tz}
                if plan.get("address"):
                    body["address_line1"] = str(plan["address"])
                lat, lon = coord(plan.get("geoLat")), coord(plan.get("geoLon"))
                if valid_coords(lat, lon):
                    body["geo_lat"], body["geo_lng"] = lat, lon
                self._sleep()
                made = self.c.request("POST", f"/v1/organizations/{self.org}/venues", body)["venue"]
                venue = {"id": made["id"], "name": made["name"], "city_key": key}
                self.venues.append(venue)
            rec["venueId"] = venue["id"]
            existing = self.c.request("GET", f"/v1/venues/{venue['id']}/seating-plans").get("seating_plans", [])
            same = [p for p in existing if norm(p.get("name")) == norm(plan_name) and p.get("owner_org_id") == self.org]
            plan_uuid = None
            if same:
                if same[0].get("current_version_id"):
                    rec["seatingPlanUuid"] = same[0]["id"]
                    rec["status"] = "loaded"
                    rec["reason"] = "already present on the server"
                    return rec
                plan_uuid = same[0]["id"]
            if plan_uuid is None:
                self._sleep()
                plan_uuid = self.c.request(
                    "POST", f"/v1/venues/{venue['id']}/seating-plans",
                    {"owner_org_id": self.org, "name": plan_name, "plan_type": "assigned_seats"},
                )["seating_plan"]["id"]
            rec["seatingPlanUuid"] = plan_uuid
            svg = (folder / "house.svg").read_text(encoding="utf-8")
            self._sleep()
            self.c.request("POST", f"/v1/seating-plans/{plan_uuid}/versions", {"svg": svg})
            rec["status"] = "loaded"
            return rec
        except ApiError as e:
            rec["status"] = "failed"
            rec["reason"] = str(e)
            return rec


def parse_args(argv):
    p = argparse.ArgumentParser(description="Load converted Forzum plans into an arena server.")
    p.add_argument("--api", required=True, help="arena base URL, for example https://api.staging.example")
    p.add_argument("--org", required=True, help="organization UUID or slug (a slug is resolved after sign-in)")
    p.add_argument("--apply", action="store_true", help="write to the server; without it the run is a dry run")
    p.add_argument("--exclude-venue", action="append", default=[], help="Forzum venueId to leave out (repeatable)")
    p.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--only-plan", help="seatingPlanId to load")
    p.add_argument("--limit", type=int, help="most plans to handle in this run")
    p.add_argument("--timezones", help="JSON file: Forzum cityId -> IANA time zone")
    p.add_argument("--pause-ms", type=int, default=200)
    p.add_argument("--production", action="store_true", help="allow a host without 'staging' in its name")
    p.add_argument("--i-have-the-owners-go-ahead", dest="go_ahead", action="store_true")
    return p.parse_args(argv)


LOCAL_HOSTS = ("localhost", "127.0.0.1")


def api_is_safe(api: str) -> bool:
    """https always; plain http only for localhost / 127.0.0.1."""
    u = urllib.parse.urlparse(api)
    host = (u.hostname or "").lower()
    return u.scheme == "https" or (u.scheme == "http" and host in LOCAL_HOSTS)


def check_gates(args):
    """Return (exit_code, None) on refusal, else (None, org uuid string)."""
    # A dry run is the default. Only --apply (or the older go-ahead flag) writes.
    args.dry_run = bool(args.dry_run) or not (args.apply or args.go_ahead)
    if not api_is_safe(args.api):
        print("Refused: --api must be https (plain http only for localhost or 127.0.0.1).", file=sys.stderr)
        return 2, None
    if not host_is_staging(args.api) and not args.production:
        print("Refused: the host has no 'staging' in its name. Add --production to run anyway.", file=sys.stderr)
        return 2, None
    try:
        return None, str(uuid.UUID(args.org))
    except ValueError:
        if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", args.org or ""):
            print("Refused: --org must be an organization UUID or slug.", file=sys.stderr)
            return 2, None
        return None, "slug:" + args.org


def resolve_org(client, org):
    """A UUID is returned as it is. A slug is looked up on the server after sign-in, never guessed."""
    if not org.startswith("slug:"):
        return org
    slug = org[5:]
    found = set()
    for path in ("/v1/me", "/v1/admin/organizations?q=" + urllib.parse.quote(slug) + "&limit=100"):
        try:
            data = client.request("GET", path)
        except ApiError:
            continue
        stack = [data]
        while stack:
            n = stack.pop()
            if isinstance(n, list):
                stack.extend(n)
            elif isinstance(n, dict):
                names = [n.get("org_name"), n.get("organization_name")] if ("org_id" in n or "organization_id" in n) else []
                by_name = any(v and norm(v).replace(" ", "").startswith(slug.replace("-", "")) for v in names)
                if by_name or slug in (n.get("slug"), n.get("org_slug"), n.get("organization_slug")):
                    oid = n.get("org_id") or n.get("organization_id") or n.get("id")
                    try:
                        found.add(str(uuid.UUID(str(oid))))
                    except ValueError:
                        pass
                stack.extend(n.values())
        if found:
            break
    if len(found) != 1:
        raise ApiError(0, f"organization {slug!r}: {len(found)} match(es) on the server, give the UUID in --org")
    return found.pop()


def can_curate(client) -> bool:
    """True when the signed-in account holds venue.curate (a platform superadmin)."""
    try:
        me = client.request("GET", "/v1/me")
    except ApiError:
        return False
    perms = me.get("permissions") if isinstance(me, dict) else None
    return isinstance(perms, list) and "venue.curate" in perms


def write_run_report(lib, name, data):
    """One report per run under <lib>/load-reports/. Holds no password and no token."""
    write_json(Path(lib) / "load-reports" / name, data)


def run_plans(args, input_fn, getpass_fn, out) -> int:
    code, org = check_gates(args)
    if code is not None:
        return code
    tz = {}
    if args.timezones:
        tz = read_json(Path(args.timezones), None)
        if not isinstance(tz, dict):
            print("Refused: --timezones is not a JSON object.", file=sys.stderr)
            return 2
        tz = {str(k): v for k, v in tz.items()}
    lib = Path(args.lib)
    log_path = lib / "load" / "log.json"
    log = read_json(log_path, {"plans": {}})
    if not isinstance(log, dict) or not isinstance(log.get("plans"), dict):
        log = {"plans": {}}

    email = input_fn("arena e-mail: ").strip()
    password = getpass_fn("arena password: ")
    client = Client(args.api)
    try:
        client.login(email, password)
    except ApiError as e:
        print(f"Login failed: {e}", file=sys.stderr)
        return 1
    finally:
        password = None

    try:
        org = resolve_org(client, org)
        ld = Loader(client, org, lib, args.dry_run, tz, out, args.pause_ms)
        ld.load_reference()
    except ApiError as e:
        print(f"Cannot read the server: {e}", file=sys.stderr)
        return 1

    plans_root = lib / "plans"

    def plan_key(folder: Path) -> str:
        # The log is keyed by the folder path under <lib>/plans: one seatingPlanId
        # can sit in several folders (same plan in several venues).
        return folder.relative_to(plans_root).as_posix()

    todo = []
    for folder, plan in find_plans(lib):
        pid = str(plan["seatingPlanId"])
        if args.only_plan and pid != str(args.only_plan):
            continue
        if str(plan.get("venueId")) in {str(x) for x in args.exclude_venue}:
            continue
        if log["plans"].get(plan_key(folder), {}).get("status") == "loaded":
            continue
        todo.append((folder, plan))
    if args.limit is not None:
        todo = todo[: max(args.limit, 0)]

    counts = {}
    ambiguous = {}
    records = []
    for folder, plan in todo:
        rec = ld.process(folder, plan)
        records.append({**rec, "folder": plan_key(folder), "venueId": plan.get("venueId"),
                        "venueName": plan.get("venueName"), "cityName": plan.get("cityName"),
                        "seatingPlanName": plan.get("seatingPlanName")})
        pid = rec["seatingPlanId"]
        key = plan_key(folder)
        counts[rec["status"]] = counts.get(rec["status"], 0) + 1
        ld.say(f"plan {key}: {rec['status']} {rec['reason']}".rstrip())
        if rec["status"] == "ambiguous":
            ambiguous[key] = {**rec, "venueName": plan.get("venueName"), "cityName": plan.get("cityName")}
        if not args.dry_run:
            log["plans"][key] = rec
            write_json(log_path, log)
    if not args.dry_run:
        old = read_json(lib / "load" / "ambiguous.json", {})
        old = old if isinstance(old, dict) else {}
        for pid in list(old):
            if log["plans"].get(pid, {}).get("status") == "loaded":
                old.pop(pid)
        old.update(ambiguous)
        write_json(lib / "load" / "ambiguous.json", old)
    write_run_report(lib, "plans-dry-run.json" if args.dry_run else "plans-apply.json",
                     {"api": args.api, "organization": org, "dryRun": args.dry_run, "counts": counts,
                      "excludedVenues": args.exclude_venue, "plans": records})
    ld.say("done: " + ", ".join(f"{k}={v}" for k, v in sorted(counts.items())) if counts else "done: nothing to do")
    return 0


# ---------------------------------------------------------------------------
# Venues mode: every venue of the Forzum inventory, with or without a plan.
# ---------------------------------------------------------------------------

_COUNTRY_RAW = {
    "Armenia": "AM", "Austria": "AT", "Bulgary": "BG", "Bulgaria": "BG", "Canada": "CA",
    "Czech Republic": "CZ", "Czechia": "CZ", "Estonia": "EE", "Finland": "FI", "France": "FR",
    "Georgia": "GE", "Germany": "DE", "Greece": "GR", "Hungary": "HU", "Indonesia": "ID",
    "Israel": "IL", "Italy": "IT", "Kazakhstan": "KZ", "Latvia": "LV", "Lithuania": "LT",
    "Montenegro": "ME", "Netherlands": "NL", "The Netherlands": "NL", "Norway": "NO", "Poland": "PL",
    "Serbia": "RS", "Spain": "ES", "Sweden": "SE", "Thailand": "TH", "Turkey": "TR", "Turkiye": "TR",
    "United Kingdom": "GB", "UK": "GB", "Great Britain": "GB", "USA": "US", "United States": "US",
    "United States of America": "US", "Uzbekistan": "UZ", "Belarus": "BY", "Ukraine": "UA",
    "Cyprus": "CY", "Switzerland": "CH", "Portugal": "PT", "Romania": "RO", "Slovakia": "SK",
    "Slovenia": "SI", "Croatia": "HR", "Belgium": "BE", "Denmark": "DK", "Ireland": "IE",
    "Moldova": "MD", "Azerbaijan": "AZ", "Kyrgyzstan": "KG", "Egypt": "EG", "UAE": "AE",
    "United Arab Emirates": "AE", "China": "CN", "Japan": "JP", "India": "IN", "Mexico": "MX",
}
COUNTRY_ISO2 = {norm(k): v for k, v in _COUNTRY_RAW.items()}

VERIFY_REASON = "Forzum library venues load, verified by the platform owner"


def country_iso2(name):
    return COUNTRY_ISO2.get(norm(name))


def coord(x):
    """Forzum sends coordinates as text ("50.2288"). Returns a float, or None when it is not a number."""
    if isinstance(x, bool):
        return None
    if isinstance(x, (int, float)):
        return float(x)
    try:
        return float(str(x).strip().replace(",", "."))
    except ValueError:
        return None


def valid_coords(lat, lon) -> bool:
    ok = lambda x: isinstance(x, (int, float)) and not isinstance(x, bool)
    return ok(lat) and ok(lon) and -90 <= lat <= 90 and -180 <= lon <= 180 and bool(lat or lon)


def _items(data, key):
    if isinstance(data, dict):
        data = data.get(key)
    return [d for d in (data or []) if isinstance(d, dict)]


def load_inventory_venues(lib: Path):
    """Return (entries, error). An entry is one inventory venue, normalised."""
    inv = lib / "inventory"
    raw = read_json(inv / "venues.json", None)
    if raw is None:
        return [], f"{inv / 'venues.json'} is missing or not JSON"
    cities = {str(c.get("cityId")): c.get("cityName") for c in _items(read_json(inv / "cities.json", []), "cities")}
    countries = {str(c.get("countryId")): c.get("countryName") for c in _items(read_json(inv / "countries.json", []), "countries")}
    entries = []
    for v in _items(raw, "venues"):
        cid, coid = str(v.get("cityId")), str(v.get("countryId"))
        country = countries.get(coid) or v.get("countryName") or ""
        city = str(cities.get(cid) or v.get("cityName") or "").strip()
        iso2 = country_iso2(country)
        entries.append({
            "venueId": str(v.get("venueId")), "name": str(v.get("venueName") or "").strip(),
            "countryId": coid, "countryName": str(country), "iso2": iso2,
            "cityId": cid, "cityName": city,
            "address": str(v.get("address") or "").strip(),
            "lat": coord(v.get("geoLat")), "lon": coord(v.get("geoLon")), "type": v.get("type"),
            "key": (iso2 or "country:" + coid, norm(city) or "city:" + cid),
        })
    by_key = {}
    for e in entries:
        by_key.setdefault(e["key"], []).append(e["cityId"])
    for e in entries:
        e["cityIds"] = by_key[e["key"]]
    return entries, None


def inventory_duplicates(entries):
    """Groups of venues with the same normalised name in the same city (never merged)."""
    groups = {}
    for e in entries:
        if norm(e["name"]):
            groups.setdefault((norm(e["name"]), e["key"]), []).append(e)
    return [g for g in groups.values() if len(g) > 1]


def load_zones(lib: Path):
    """cityId (str) -> IANA zone, or None when venues-zones.json is missing."""
    raw = read_json(lib / "load" / "venues-zones.json", None)
    if not isinstance(raw, list):
        return None
    out = {}
    for it in raw:
        if isinstance(it, dict) and isinstance(it.get("timezone"), str) and it["timezone"].strip():
            out[str(it.get("key"))] = it["timezone"].strip()
    return out


def zone_for(zones, e):
    for cid in e["cityIds"]:
        if zones and zones.get(cid):
            return zones[cid]
    return None


def build_report(lib: Path, out) -> int:
    entries, err = load_inventory_venues(lib)
    if err:
        print(f"Cannot build the report: {err}", file=sys.stderr)
        return 1
    zones = load_zones(lib)
    dups = inventory_duplicates(entries)
    cities = {}
    for e in entries:
        cities.setdefault(e["key"], e)
    request = [{"key": c["cityId"], "iso2": c["iso2"], "city": c["cityName"]}
               for c in cities.values() if c["iso2"] and c["cityName"]]
    write_json(lib / "load" / "venues-zone-request.json", request)
    resolved = sum(1 for c in cities.values() if zone_for(zones, c))
    bad_countries = sorted({e["countryName"] for e in entries if not e["iso2"]})
    typed = sum(1 for e in entries if e["type"] not in (None, ""))
    report = {
        "venues_total": len(entries),
        "would_create": len(entries),
        "venues_without_name": sum(1 for e in entries if not norm(e["name"])),
        "ambiguous_groups": len(dups),
        "ambiguous_venues": sum(len(g) for g in dups),
        "without_coordinates": sum(1 for e in entries if not valid_coords(e["lat"], e["lon"])),
        "without_address": sum(1 for e in entries if not e["address"]),
        "distinct_cities": len(cities),
        "countries_without_iso2": bad_countries,
        "venues_in_countries_without_iso2": sum(1 for e in entries if not e["iso2"]),
        "timezone_file_present": zones is not None,
        "cities_timezone_resolved": resolved,
        "cities_timezone_unresolved": (len(cities) - resolved) if zones is not None else 0,
        "cities_timezone_not_looked_up": len(cities) if zones is None else 0,
        "venues_with_type": typed,
        "venue_type_note": "the venue create route has no type field: the type is never sent"
        + ("" if typed else " (all inventory types are null)"),
        "zone_request_items": len(request),
        "ambiguous": [[{"venueId": e["venueId"], "name": e["name"], "cityName": e["cityName"]} for e in g] for g in dups],
    }
    write_json(lib / "load" / "venues-report.json", report)
    say = lambda s: print(s, file=out)
    say(f"venues total: {report['venues_total']}")
    say(f"would create (offline, all): {report['would_create']}")
    say(f"duplicates inside the inventory: {report['ambiguous_groups']} group(s), {report['ambiguous_venues']} venue(s), listed as ambiguous")
    say(f"venues without coordinates: {report['without_coordinates']}")
    say(f"venues without address: {report['without_address']}")
    say(f"distinct cities needed: {report['distinct_cities']}")
    say(f"countries without ISO2 code: {len(bad_countries)} {bad_countries}".rstrip())
    if zones is None:
        say("venues-zones.json is missing: run `go run ./apps/backend/cmd/arena-geotz-lookup --in "
            f"{lib / 'load' / 'venues-zone-request.json'} --out {lib / 'load' / 'venues-zones.json'}`")
        say(f"timezone: not looked up yet for {report['cities_timezone_not_looked_up']} city(ies)")
    else:
        say(f"timezone: resolved {resolved}, unresolved {report['cities_timezone_unresolved']} city(ies)")
    say(report["venue_type_note"])
    return 0


class VenueLoader(Loader):
    def __init__(self, client, org, lib, dry_run, zones, out, pause_ms=0, mark_verified=False):
        super().__init__(client, org, lib, dry_run, {}, out, pause_ms)
        self.zones = zones or {}
        self.mark = mark_verified
        self.cities_new = []  # cities created (or, in dry-run, to create)

    def verify(self, rec):
        """PATCH review_status=verified, only with --mark-verified and not in dry-run."""
        if not self.mark or self.dry or not rec.get("venueUuid") or rec.get("verified"):
            return
        try:
            self._sleep()
            self.c.request("PATCH", f"/v1/admin/venues/{rec['venueUuid']}/review",
                           {"review_status": "verified"}, headers={"X-Admin-Reason": VERIFY_REASON})
            rec["verified"] = True
            rec.pop("verifyError", None)
        except ApiError as e:
            rec["verified"] = False
            rec["verifyError"] = str(e)

    def process_venue(self, e, dup_ids) -> dict:
        rec = {"venueId": e["venueId"], "venueUuid": None, "name": e["name"], "cityName": e["cityName"],
               "countryName": e["countryName"], "action": None, "status": None, "reason": "",
               "verified": False, "at": _now()}
        if not norm(e["name"]):
            return {**rec, "status": "skipped", "reason": "venue has no name"}
        if not e["iso2"]:
            return {**rec, "status": "skipped", "reason": f"country name has no ISO2 code: {e['countryName']!r}"}
        if not e["cityName"]:
            return {**rec, "status": "skipped", "reason": "venue has no city name"}
        if e["venueId"] in dup_ids:
            return {**rec, "status": "ambiguous", "action": "ambiguous",
                    "reason": "same name and city as other inventory venue(s): " + ", ".join(dup_ids[e["venueId"]]),
                    "candidates": dup_ids[e["venueId"]]}
        iso = e["iso2"]
        if iso not in self.country_ids:
            return {**rec, "status": "skipped", "reason": "country not in arena geo"}
        key = (iso, norm(e["cityName"]))
        city = self.cities.get(key)
        tz = (city or {}).get("timezone") or zone_for(self.zones, e)
        if not tz:
            return {**rec, "status": "skipped", "reason": "timezone unresolved", "timezoneUnresolved": True}
        cands = [v for v in self.venues if norm(v["name"]) == norm(e["name"])]
        if not cands:
            action = "create"
        elif len(cands) == 1 and cands[0]["city_key"] == key:
            action = "match"
        else:
            return {**rec, "status": "ambiguous", "action": "ambiguous",
                    "reason": f"{len(cands)} venue(s) with the same name in the organization, none is a single match in this city",
                    "candidates": [{"id": v["id"], "name": v["name"]} for v in cands]}
        rec["action"] = action
        try:
            if action == "match":
                rec["venueUuid"] = cands[0]["id"]
                rec["status"] = "dry-run" if self.dry else "matched"
                rec["reason"] = "would match existing venue" if self.dry else "matched existing venue"
                self.verify(rec)
                return rec
            if city is None:
                if self.dry:
                    city = {"id": None, "timezone": None, "country_iso2": iso}
                else:
                    self._sleep()
                    made = self.c.request("POST", f"/v1/organizations/{self.org}/cities",
                                          {"country_id": self.country_ids[iso], "name": e["cityName"], "locale": "en"})
                    city = {**made["city"], "country_iso2": iso}
                    if made.get("created") is False:
                        self.say(f"city {e['cityName']} ({iso}): already existed under another name, reused")
                self.cities[key] = city
                if city.get("id"):
                    self.city_by_id[city["id"]] = {**city, "name": e["cityName"]}
                self.cities_new.append({"city": e["cityName"], "iso2": iso, "id": city.get("id")})
            if self.dry:
                rec["status"] = "dry-run"
                rec["reason"] = "would create venue"
                self.venues.append({"id": None, "name": e["name"], "city_key": key})
                return rec
            body = {"name": e["name"], "city_id": city["id"], "country": iso, "timezone": tz}
            if e["address"]:
                body["address_line1"] = e["address"]
            if valid_coords(e["lat"], e["lon"]):
                body["geo_lat"], body["geo_lng"] = e["lat"], e["lon"]
            self._sleep()
            made = self.c.request("POST", f"/v1/organizations/{self.org}/venues", body)["venue"]
            self.venues.append({"id": made["id"], "name": made["name"], "city_key": key})
            rec["venueUuid"] = made["id"]
            rec["status"] = "created"
            self.verify(rec)
            return rec
        except ApiError as ex:
            rec["status"] = "failed"
            rec["reason"] = str(ex)
            return rec


def parse_venues_args(argv):
    p = argparse.ArgumentParser(prog="loader.py venues", description="Load every Forzum inventory venue into one arena organization.")
    p.add_argument("--api", required=True, help="arena base URL, for example https://api.staging.example")
    p.add_argument("--org", required=True, help="organization UUID or slug (a slug is resolved after sign-in)")
    p.add_argument("--apply", action="store_true", help="write to the server; without it the run is a dry run")
    p.add_argument("--exclude-venue", action="append", default=[], help="Forzum venueId to leave out (repeatable)")
    p.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--mark-verified", action=argparse.BooleanOptionalAction, default=True,
                   help="PATCH review_status=verified for venues created or matched (on by default: the owner "
                        "checked these venues by hand; --no-mark-verified leaves them unverified)")
    p.add_argument("--pause-ms", type=int, default=200)
    p.add_argument("--production", action="store_true", help="allow a host without 'staging' in its name")
    p.add_argument("--i-have-the-owners-go-ahead", dest="go_ahead", action="store_true")
    return p.parse_args(argv)


def login_client(args, input_fn, getpass_fn):
    email = input_fn("arena e-mail: ").strip()
    password = getpass_fn("arena password: ")
    client = Client(args.api)
    try:
        client.login(email, password)
    except ApiError as e:
        print(f"Login failed: {e}", file=sys.stderr)
        return None
    finally:
        password = None
    return client


def run_venues(args, input_fn, getpass_fn, out) -> int:
    code, org = check_gates(args)
    if code is not None:
        return code
    lib = Path(args.lib)
    entries, err = load_inventory_venues(lib)
    if err:
        print(f"Refused: {err}", file=sys.stderr)
        return 2
    zones = load_zones(lib) or {}
    log_path = lib / "load" / "venues-log.json"
    log = read_json(log_path, {"venues": {}})
    if not isinstance(log, dict) or not isinstance(log.get("venues"), dict):
        log = {"venues": {}}
    dup_ids = {}
    for g in inventory_duplicates(entries):
        for e in g:
            dup_ids[e["venueId"]] = [o["venueId"] for o in g if o is not e]

    client = login_client(args, input_fn, getpass_fn)
    if client is None:
        return 1
    try:
        org = resolve_org(client, org)
        if args.mark_verified and not can_curate(client):
            args.mark_verified = False
            print("This account has no venue.curate permission: venues are loaded UNVERIFIED. "
                  "Run `loader.py verify-venues` later with a platform superadmin sign-in.", file=out)
        ld = VenueLoader(client, org, lib, args.dry_run, zones, out, args.pause_ms, args.mark_verified)
        ld.load_reference()
    except ApiError as e:
        print(f"Cannot read the server: {e}", file=sys.stderr)
        return 1

    counts = {}
    buckets = {"would_create": [], "would_match": [], "ambiguous": [], "timezone_unresolved": [], "skipped": [], "failed": []}
    ambiguous = {}
    excluded = {str(x) for x in args.exclude_venue}
    records = []
    for e in entries:
        vid = e["venueId"]
        if str(vid) in excluded:
            continue
        done = log["venues"].get(vid)
        if done and done.get("status") in ("created", "matched"):
            if args.mark_verified and not args.dry_run and not done.get("verified"):
                ld.verify(done)
                log["venues"][vid] = done
                write_json(log_path, log)
                counts["verified-later"] = counts.get("verified-later", 0) + 1
            continue
        rec = ld.process_venue(e, dup_ids)
        records.append(rec)
        counts[rec["status"]] = counts.get(rec["status"], 0) + 1
        ld.say(f"venue {vid}: {rec['status']} {rec['reason']}".rstrip())
        if args.dry_run:
            if rec["status"] == "dry-run":
                buckets["would_create" if rec["action"] == "create" else "would_match"].append(rec)
            elif rec.get("timezoneUnresolved"):
                buckets["timezone_unresolved"].append(rec)
            elif rec["status"] in buckets:
                buckets[rec["status"]].append(rec)
            else:
                buckets["skipped"].append(rec)
        else:
            if rec["status"] == "ambiguous":
                ambiguous[vid] = rec
            log["venues"][vid] = rec
            write_json(log_path, log)
    typed = sum(1 for e in entries if e["type"] not in (None, ""))
    note = ("the venue create route has no type field: the type is never sent; "
            + (f"{typed} inventory venue(s) have a type" if typed else "all inventory types are null"))
    if args.dry_run:
        write_json(lib / "load" / "venues-dry-run.json", {
            **buckets, "cities_to_create": ld.cities_new,
            "would_verify": (len(buckets["would_create"]) + len(buckets["would_match"])) if args.mark_verified else 0,
            "venue_type_note": note})
    else:
        old = read_json(lib / "load" / "venues-ambiguous.json", {})
        old = old if isinstance(old, dict) else {}
        for vid in list(old):
            if log["venues"].get(vid, {}).get("status") in ("created", "matched"):
                old.pop(vid)
        old.update(ambiguous)
        write_json(lib / "load" / "venues-ambiguous.json", old)
        for c in ld.cities_new:
            ld.say(f"city created: {c['city']} ({c['iso2']})")
    if args.dry_run:
        for c in ld.cities_new:
            ld.say(f"city to create: {c['city']} ({c['iso2']})")
    summary = {"api": args.api, "organization": org, "dryRun": args.dry_run, "markVerified": args.mark_verified,
               "counts": counts, "excludedVenues": sorted(excluded), "citiesToCreate" if args.dry_run else "citiesCreated": ld.cities_new,
               "venueTypeNote": note}
    if args.dry_run:
        summary.update({k: v for k, v in buckets.items()})
    else:
        summary["venues"] = records
    write_run_report(lib, "venues-dry-run.json" if args.dry_run else "venues-apply.json", summary)
    ld.say(note)
    ld.say("done: " + ", ".join(f"{k}={v}" for k, v in sorted(counts.items())) if counts else "done: nothing to do")
    return 0


def run_verify_venues(argv, input_fn, getpass_fn, out) -> int:
    """Mark venues that an earlier run loaded as verified. Creates nothing."""
    p = argparse.ArgumentParser(prog="loader.py verify-venues",
                                description="Set review_status=verified on venues already loaded (platform superadmin).")
    p.add_argument("--api", required=True)
    p.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
    p.add_argument("--apply", action="store_true", help="send the PATCH; without it the run only lists the venues")
    p.add_argument("--pause-ms", type=int, default=200)
    p.add_argument("--production", action="store_true")
    args = p.parse_args(argv)
    if not api_is_safe(args.api):
        print("Refused: --api must be https (plain http only for localhost or 127.0.0.1).", file=sys.stderr)
        return 2
    if not host_is_staging(args.api) and not args.production:
        print("Refused: the host has no 'staging' in its name. Add --production to run anyway.", file=sys.stderr)
        return 2
    lib = Path(args.lib)
    log_path = lib / "load" / "venues-log.json"
    log = read_json(log_path, {"venues": {}})
    if not isinstance(log, dict) or not isinstance(log.get("venues"), dict):
        log = {"venues": {}}
    # The apply report is the second source: it also holds the venue UUIDs.
    report = read_json(lib / "load-reports" / "venues-apply.json", {})
    for rec in (report.get("venues") or []) if isinstance(report, dict) else []:
        if isinstance(rec, dict) and rec.get("venueUuid") and str(rec.get("venueId")) not in log["venues"]:
            log["venues"][str(rec["venueId"])] = dict(rec)
    todo = [(vid, rec) for vid, rec in sorted(log["venues"].items())
            if rec.get("status") in ("created", "matched") and rec.get("venueUuid") and not rec.get("verified")]
    if not todo:
        print("Nothing to verify: no loaded venue is waiting.", file=out)
        return 0
    args.dry_run = not args.apply
    client = login_client(args, input_fn, getpass_fn)
    if client is None:
        return 1
    if not can_curate(client):
        print("Refused: this account has no venue.curate permission (a platform superadmin is needed). Nothing was sent.",
              file=sys.stderr)
        return 1
    done, failed = [], []
    for vid, rec in todo:
        if args.dry_run:
            print(f"venue {vid}: would verify {rec.get('name', '')}", file=out)
            continue
        if args.pause_ms:
            time.sleep(args.pause_ms / 1000.0)
        try:
            client.request("PATCH", f"/v1/admin/venues/{rec['venueUuid']}/review",
                           {"review_status": "verified"}, headers={"X-Admin-Reason": VERIFY_REASON})
            rec["verified"] = True
            rec.pop("verifyError", None)
            done.append(vid)
        except ApiError as e:
            rec["verifyError"] = str(e)
            failed.append({"venueId": vid, "error": str(e)})
        write_json(log_path, log)
    write_run_report(lib, "venues-verify-dry-run.json" if args.dry_run else "venues-verify.json",
                     {"api": args.api, "dryRun": args.dry_run, "waiting": [v for v, _ in todo],
                      "verified": done, "failed": failed})
    print(f"done: waiting={len(todo)} verified={len(done)} failed={len(failed)}", file=out)
    return 0


def main(argv=None, input_fn=input, getpass_fn=getpass.getpass, out=None) -> int:
    out = out or sys.stdout
    argv = list(argv if argv is not None else sys.argv[1:])
    mode = "plans"
    if argv and argv[0] == "verify-venues":
        return run_verify_venues(argv[1:], input_fn, getpass_fn, out)
    if argv and argv[0] in ("plans", "venues", "venues-report"):
        mode, argv = argv[0], argv[1:]
    if mode == "venues-report":
        p = argparse.ArgumentParser(prog="loader.py venues-report", description="Offline report on the inventory venues.")
        p.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
        return build_report(Path(p.parse_args(argv).lib), out)
    if mode == "venues":
        return run_venues(parse_venues_args(argv), input_fn, getpass_fn, out)
    return run_plans(parse_args(argv), input_fn, getpass_fn, out)


if __name__ == "__main__":
    sys.exit(main())
