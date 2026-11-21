"""Steps 1-2: list countries, cities, venues, actions, sessions and plans of Forzum.

Read-only. Resumable: an answer already saved under inventory/raw is not asked again.
"""

import argparse
import os
import sys
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # works under python -I
from forzum_client import (DEFAULT_APIS, Client, TokenError, add_common_args,
                           choose_api, is_russian, load_json, save_json, saved_api, validate_apis, walk)


def is_russia(c):
    return is_russian(c.get("countryName") or c.get("name"), c.get("countryCode") or c.get("code"))


def collect_actions(tree):
    """Walk the whole tree: the nesting differs between protocol versions."""
    actions = {}

    def rec(node, ctx):
        if isinstance(node, list):
            for n in node:
                rec(n, ctx)
            return
        if not isinstance(node, dict):
            return
        here = dict(ctx)
        if node.get("cityId") is not None:
            here["cityId"] = node["cityId"]
        if node.get("actionId") is not None:
            here["actionId"] = node["actionId"]
            prev = actions.get(node["actionId"], {})
            actions[node["actionId"]] = {
                "actionId": node["actionId"],
                "cityId": here.get("cityId", prev.get("cityId")),
                "cityIds": sorted(set(prev.get("cityIds", [])) | ({here["cityId"]} if here.get("cityId") is not None else set())),
            }
        if node.get("actionEventId") is not None:
            owner = actions.get(here.get("actionId"))
            if owner is not None and here.get("cityId") is not None:
                owner["cityIds"] = sorted(set(owner["cityIds"]) | {here["cityId"]})
                if owner["cityId"] is None:
                    owner["cityId"] = here["cityId"]
        for v in node.values():
            rec(v, here)

    rec(tree, {})
    return actions


def collect_sessions(tree, action_id, city_id):
    """Every session node of a GET_ACTION_EXT answer, with the venue it hangs under."""
    out = []

    def rec(node, ctx):
        if isinstance(node, list):
            for n in node:
                rec(n, ctx)
            return
        if not isinstance(node, dict):
            return
        here = dict(ctx)
        for k in ("venueId", "venueName", "actionName"):
            if node.get(k) is not None and node.get("actionEventId") is None:
                here[k] = node[k]
        if node.get("actionEventId") is not None:
            s = {"actionEventId": node["actionEventId"], "actionId": action_id,
                 "actionName": node.get("actionName", here.get("actionName")),
                 "day": node.get("day"), "cityId": city_id,
                 "venueId": node.get("venueId", here.get("venueId")),
                 "venueName": node.get("venueName", here.get("venueName")),
                 "seatingPlanId": node.get("seatingPlanId"),
                 "seatingPlanName": node.get("seatingPlanName"),
                 "placementUrl": node.get("placementUrl")}
            out.append(s)
            return
        for v in node.values():
            rec(v, here)

    rec(tree, {})
    return out


class Inventory:
    def __init__(self, a, client, api_base=None):
        self.a = a
        self.c = client
        self.api_base = api_base
        self.dir = os.path.join(a.lib, "inventory")
        self.raw = os.path.join(self.dir, "raw")
        self.state = load_json(os.path.join(self.dir, "state.json"), {"done": []})

    def ask(self, name, command, **params):
        path = os.path.join(self.raw, name + ".json")
        data = load_json(path)
        if data is not None:
            return data
        data = self.c.call(command, **params)
        if data is not None:
            save_json(path, self.c.scrub(data))
        return data

    def run(self):
        a = self.a
        countries = []
        data = self.ask("GET_COUNTRIES", "GET_COUNTRIES")
        for d in walk(data or {}):
            if d.get("countryId") is not None and not is_russia(d):
                countries.append({"countryId": d["countryId"], "countryName": d.get("countryName", d.get("name"))})
        cities, venues = [], []
        for co in countries:
            data = self.ask("GET_CITIES_%s" % co["countryId"], "GET_CITIES", countryId=co["countryId"])
            for d in walk(data or {}):
                if d.get("cityId") is not None:
                    cities.append({"countryId": co["countryId"], "cityId": d["cityId"],
                                   "cityName": d.get("cityName", d.get("name"))})
        for ci in cities:
            data = self.ask("GET_VENUES_%s_%s" % (ci["countryId"], ci["cityId"]), "GET_VENUES",
                            countryId=ci["countryId"], cityId=ci["cityId"])
            for d in walk(data or {}):
                if d.get("venueId") is not None:
                    venues.append({"countryId": ci["countryId"], "cityId": ci["cityId"], "venueId": d["venueId"],
                                   "venueName": d.get("venueName", d.get("name")), "address": d.get("address"),
                                   "geoLat": d.get("geoLat"), "geoLon": d.get("geoLon"), "type": d.get("type")})
        if a.only_venue is not None:
            venues = [v for v in venues if v["venueId"] == a.only_venue]
        save_json(os.path.join(self.dir, "countries.json"), countries)
        save_json(os.path.join(self.dir, "cities.json"), cities)
        save_json(os.path.join(self.dir, "venues.json"), venues)

        city_ok = {c["cityId"] for c in cities}
        if a.only_venue is not None:
            city_ok &= {v["cityId"] for v in venues}
        actions = collect_actions(self.ask("GET_ALL_ACTIONS", "GET_ALL_ACTIONS") or {})
        pairs = sorted({(cid, act["actionId"]) for act in actions.values()
                        for cid in (act["cityIds"] or ([act["cityId"]] if act["cityId"] is not None else []))
                        if cid in city_ok})
        if a.limit is not None:
            pairs = pairs[: a.limit]
        city_by_id = {c["cityId"]: c for c in cities}
        venue_by_name = {(v["cityId"], v["venueName"]): v["venueId"] for v in venues}
        sessions = []
        for cid, aid in pairs:
            data = self.ask("GET_ACTION_EXT_%s_%s" % (cid, aid), "GET_ACTION_EXT", cityId=cid, actionId=aid)
            for s in collect_sessions(data or {}, aid, cid):
                if s["venueId"] is None:
                    s["venueId"] = venue_by_name.get((cid, s["venueName"]))
                s["countryId"] = city_by_id[cid]["countryId"]
                if a.only_venue is not None and s["venueId"] != a.only_venue:
                    continue
                sessions.append(s)
        seen = {}
        for s in sessions:
            seen[s["actionEventId"]] = s
        sessions = sorted(seen.values(), key=lambda s: s["actionEventId"])
        save_json(os.path.join(self.dir, "sessions.json"), sessions)

        venue_info = {v["venueId"]: v for v in venues}
        country_name = {c["countryId"]: c["countryName"] for c in countries}
        plans = {}
        for s in sessions:
            if not s.get("seatingPlanId"):
                continue
            p = plans.setdefault(s["seatingPlanId"], {"seatingPlanId": s["seatingPlanId"], "sessions": []})
            p["sessions"].append(s)
        plan_list = []
        for pid in sorted(plans):
            ss = plans[pid]["sessions"]
            newest = max(ss, key=lambda s: (str(s.get("day") or ""), s["actionEventId"]))
            v = venue_info.get(newest["venueId"], {})
            plan_list.append({
                "seatingPlanId": pid, "seatingPlanName": newest.get("seatingPlanName"),
                "venueId": newest["venueId"], "venueName": newest.get("venueName") or v.get("venueName"),
                "address": v.get("address"), "geoLat": v.get("geoLat"), "geoLon": v.get("geoLon"),
                "countryId": newest["countryId"], "countryName": country_name.get(newest["countryId"]),
                "cityId": newest["cityId"], "cityName": city_by_id[newest["cityId"]]["cityName"],
                "placementUrl": newest.get("placementUrl"),
                "actionEventIds": sorted(s["actionEventId"] for s in ss),
                "newestActionEventId": newest["actionEventId"]})
        save_json(os.path.join(self.dir, "plans.json"), plan_list)
        summary = {"apiBase": self.api_base, "countries": len(countries), "cities": len(cities), "venues": len(venues),
                   "venuesWithPlan": len({p["venueId"] for p in plan_list}), "plans": len(plan_list),
                   "sessions": len(sessions), "failedCalls": len(self.c.failed), "failed": self.c.failed}
        save_json(os.path.join(self.dir, "summary.json"), self.c.scrub(summary))
        save_json(os.path.join(self.dir, "state.json"),
                  self.c.scrub({"apiBase": self.api_base,
                                "done": ["countries", "cities", "venues", "actions", "sessions", "plans"],
                                "finishedAt": datetime.now(timezone.utc).isoformat()}))
        print("inventory: %d countries, %d cities, %d venues, %d plans, %d sessions, %d failed calls" % (
            len(countries), len(cities), len(venues), len(plan_list), len(sessions), len(self.c.failed)))
        return summary


def probe_session(c, aeid):
    """One call each for a closed session. A failure is a finding, not an error."""
    out = {"actionEventId": aeid}
    for cmd in ("GET_ACTION_EXT", "GET_SCHEMA", "GET_SEAT_LIST", "GET_FILTER"):
        out[cmd] = c.probe(cmd, actionEventId=aeid)
    out["seatingPlanImage"] = c.probe_image(aeid)
    return out


def main(argv=None):
    p = add_common_args(argparse.ArgumentParser(description=__doc__))
    p.add_argument("--probe-action-event", type=int, action="append", default=None, metavar="ID",
                   help="ask once for this closed session and write inventory/probe.json (may be repeated)")
    a = p.parse_args(argv)
    apis = a.api or DEFAULT_APIS
    if not validate_apis(apis):
        return 2
    c = Client(apis[0], a.fid, a.locale, a.token_file, a.pause_ms, a.backoff_base)
    try:
        c.check_token()
        inv = os.path.join(a.lib, "inventory")
        state = load_json(os.path.join(inv, "state.json"), {"done": []})
        base = choose_api(c, apis, saved_api(a.lib))
        if base is None:
            print("error: no API address answers GET_COUNTRIES with resultCode 0", file=sys.stderr)
            return 2
        state["apiBase"] = base
        save_json(os.path.join(inv, "state.json"), c.scrub(state))
        if a.probe_action_event:
            probes = [probe_session(c, i) for i in a.probe_action_event]
            save_json(os.path.join(inv, "probe.json"),
                      c.scrub({"apiBase": base, "probedAt": datetime.now(timezone.utc).isoformat(), "sessions": probes}))
            print("probe: %d session(s) written to inventory/probe.json" % len(probes))
        Inventory(a, c, base).run()
    except TokenError as e:
        print("error: %s" % e, file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
