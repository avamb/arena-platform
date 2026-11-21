"""Shared read-only client for the old Bil24/Forzum API (stdlib only).

The token is read from a file at call time. It is never printed, logged or
written anywhere: it exists only in the request body that goes on the wire.
"""

import argparse
import http.client
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_API = "https://api.forzum.ru/json"
DEFAULT_TOKEN_FILE = r"C:\Arena\forzum_token.txt"
DEFAULT_LIB = r"C:\Arena\forzum-library"
TRIES = 5
ALLOWED_PREFIX = "GET_"
RETRY_ERRORS = (urllib.error.URLError, OSError, ValueError, http.client.HTTPException, json.JSONDecodeError)
RUSSIA_NAMES = {"россия", "russia", "ru", "russian federation", "рф", "российская федерация", "rossiya"}


class TokenError(Exception):
    pass


def check_command(command):
    """The one gate of every send path: only read commands (GET_*) may leave this process."""
    if not isinstance(command, str) or not command.startswith(ALLOWED_PREFIX):
        raise RuntimeError("command %r refused: only %s* commands are allowed (CREATE_USER is never sent)"
                           % (command, ALLOWED_PREFIX))


def is_russian(name, code=None):
    n = str(name or "").strip().casefold()
    c = str(code or "").strip().casefold()
    return n in RUSSIA_NAMES or c == "ru"


def validate_apis(apis):
    """True when every address is https (or http to localhost/127.0.0.1). Prints one line otherwise."""
    for api in apis or []:
        u = urllib.parse.urlparse(str(api))
        if u.scheme == "https" or (u.scheme == "http" and u.hostname in ("localhost", "127.0.0.1")):
            continue
        print("error: --api must be https (http only for localhost/127.0.0.1): %s" % api, file=sys.stderr)
        return False
    return True


DEFAULT_APIS = [DEFAULT_API, "https://api.bil24.pro/json"]


def add_common_args(p):
    p.add_argument("--api", action="append", default=None,
                   help="API address, may be repeated: the first one that answers GET_COUNTRIES is used")
    p.add_argument("--fid", type=int, default=2558)
    p.add_argument("--locale", default="en-GB")
    p.add_argument("--token-file", default=os.environ.get("FORZUM_TOKEN_FILE", DEFAULT_TOKEN_FILE))
    p.add_argument("--lib", default=os.environ.get("FORZUM_LIBRARY_DIR", DEFAULT_LIB))
    p.add_argument("--pause-ms", type=int, default=400)
    p.add_argument("--only-venue", type=int, default=None)
    p.add_argument("--limit", type=int, default=None)
    p.add_argument("--backoff-base", type=float, default=2.0, help=argparse.SUPPRESS)
    return p


def read_token(path):
    try:
        with open(path, "r", encoding="utf-8-sig") as f:
            tok = f.read().strip()
    except OSError:
        raise TokenError("token file not found or unreadable: " + str(path))
    if not tok:
        raise TokenError("token file is empty: " + str(path))
    return tok


class Client:
    def __init__(self, api, fid, locale, token_file, pause_ms=400, backoff_base=2.0, sleep=time.sleep):
        self.api = api
        self.fid = fid
        self.locale = locale
        self.token_file = token_file
        self.pause = pause_ms / 1000.0
        self.backoff_base = backoff_base
        self.sleep = sleep
        self.failed = []

    @property
    def host(self):
        a = self.api.rstrip("/")
        return a[: -len("/json")] if a.endswith("/json") else a

    def check_token(self):
        read_token(self.token_file)

    def scrub(self, obj):
        """Replace every occurrence of the current token by *** in any text or JSON-like value."""
        try:
            tok = read_token(self.token_file)
        except TokenError:
            return obj
        return scrub_value(obj, tok)

    def _once(self, command, params):
        check_command(command)
        body = {"command": command, "fid": self.fid, "token": read_token(self.token_file), "locale": self.locale}
        body.update(params)
        req = urllib.request.Request(
            self.api,
            data=json.dumps(body).encode("utf-8"),
            headers={"Content-Type": "application/json", "Accept": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=60) as r:
            text = self.scrub(r.read().decode("utf-8"))
        data = self.scrub(json.loads(text))
        if not isinstance(data, dict):
            raise ValueError("answer is not an object")
        if int(data.get("resultCode", 0)) != 0:
            raise ValueError("resultCode %s" % data.get("resultCode"))
        return data

    def _retry(self, label, fn, record):
        last = ""
        for i in range(TRIES):
            self.sleep(self.pause)
            try:
                return fn()
            except TokenError:
                raise
            except RETRY_ERRORS as e:
                # type name only: never the request body
                last = type(e).__name__ + (" " + str(e) if isinstance(e, ValueError) else "")
                if isinstance(e, urllib.error.HTTPError):
                    last = "HTTP %s" % e.code
                last = self.scrub(last)
                if i < TRIES - 1:
                    self.sleep(self.backoff_base * (2 ** i))
        label = self.scrub(label)
        self.failed.append(self.scrub({"call": label, "error": last, **record}))
        print("failed after %d tries: %s (%s)" % (TRIES, label, last), file=sys.stderr)
        return None

    def call(self, command, **params):
        """One command. Returns the answer dict, or None after 5 failed tries."""
        check_command(command)
        label = command + (" " + json.dumps(params, sort_keys=True) if params else "")
        return self._retry(label, lambda: self._once(command, params), {"command": command, "params": params})

    def probe(self, command, **params):
        """One attempt, no retry. Describes the answer instead of raising. Never exits on failure."""
        check_command(command)
        self.sleep(self.pause)
        res = {"resultCode": None, "description": None, "bytes": 0, "hasSeats": False, "error": None}
        body = {"command": command, "fid": self.fid, "token": read_token(self.token_file), "locale": self.locale}
        body.update(params)
        req = urllib.request.Request(
            self.api, data=json.dumps(body).encode("utf-8"),
            headers={"Content-Type": "application/json", "Accept": "application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                raw = r.read()
            res["bytes"] = len(raw)
            data = self.scrub(json.loads(self.scrub(raw.decode("utf-8"))))
            if isinstance(data, dict):
                res["resultCode"] = data.get("resultCode")
                res["description"] = data.get("description")
                res["hasSeats"] = any(
                    d.get("seatId") is not None or d.get("seat_id") is not None
                    or (isinstance(d.get("seatList"), list) and len(d["seatList"]) > 0)
                    for d in walk(data))
            else:
                res["error"] = "answer is not an object"
        except urllib.error.HTTPError as e:
            res["error"] = "HTTP %s" % e.code
        except RETRY_ERRORS as e:
            res["error"] = type(e).__name__
        return self.scrub(res)

    def probe_image(self, action_event_id):
        url = "%s/image?type=seatingPlan&actionEventId=%s&userId=0&fid=%s&locale=%s" % (
            self.host, action_event_id, self.fid, self.locale)
        self.sleep(self.pause)
        res = {"bytes": 0, "hasSvg": False, "error": None}
        try:
            with urllib.request.urlopen(url, timeout=60) as r:
                data = r.read()
            res["bytes"] = len(data)
            res["hasSvg"] = b"<svg" in data[:2000]
        except urllib.error.HTTPError as e:
            res["error"] = "HTTP %s" % e.code
        except RETRY_ERRORS as e:
            res["error"] = type(e).__name__
        return res

    def fetch_image(self, action_event_id):
        """The plan picture. No token in the URL. Returns bytes of an SVG or None."""
        url = "%s/image?type=seatingPlan&actionEventId=%s&userId=0&fid=%s&locale=%s" % (
            self.host, action_event_id, self.fid, self.locale)

        def once():
            with urllib.request.urlopen(url, timeout=60) as r:
                data = r.read()
            if b"<svg" not in data[:2000]:
                raise ValueError("not an svg")
            return data

        return self._retry("image %s" % action_event_id, once, {"command": "image", "actionEventId": action_event_id}), url


def choose_api(client, apis, saved=None):
    """First address that answers GET_COUNTRIES with resultCode 0 wins. A saved choice is reused.

    Sets client.api and returns it, or returns None when no address answers.
    """
    if saved:
        client.api = saved
        return saved
    check_command("GET_COUNTRIES")
    for api in (apis or DEFAULT_APIS):
        client.api = api
        client.sleep(client.pause)
        try:
            client._once("GET_COUNTRIES", {})
            return api
        except TokenError:
            raise
        except RETRY_ERRORS as e:
            print("api %s does not answer (%s)" % (client.scrub(api), type(e).__name__), file=sys.stderr)
    client.api = (apis or DEFAULT_APIS)[0]
    return None


def saved_api(lib):
    """The apiBase of an earlier inventory run, or None."""
    inv = os.path.join(lib, "inventory")
    for name in ("state.json", "summary.json"):
        v = (load_json(os.path.join(inv, name), {}) or {}).get("apiBase")
        if v:
            return v
    return None


def walk(node):
    """Yield every dict of a JSON tree."""
    if isinstance(node, list):
        for n in node:
            yield from walk(n)
    elif isinstance(node, dict):
        yield node
        for v in node.values():
            yield from walk(v)


def scrub_value(obj, tok):
    """Replace tok by *** in every string (keys included) of a JSON-like tree."""
    if not tok:
        return obj
    if isinstance(obj, str):
        return obj.replace(tok, "***")
    if isinstance(obj, list):
        return [scrub_value(x, tok) for x in obj]
    if isinstance(obj, dict):
        return {scrub_value(k, tok): scrub_value(v, tok) for k, v in obj.items()}
    return obj


def save_json(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)


def load_json(path, default=None):
    try:
        with open(path, "r", encoding="utf-8-sig") as f:
            return json.load(f)
    except (OSError, ValueError):
        return default
