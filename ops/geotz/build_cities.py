"""Builds apps/backend/internal/platform/geotz/cities.tsv.gz from GeoNames.

Source: https://download.geonames.org/export/dump/cities15000.zip (CC BY 4.0,
see the NOTICE next to the data file). Usage:

    python ops/geotz/build_cities.py path/to/cities15000.zip

The output has two kinds of line (tab separated):

  Z  CC  zone  city      each zone of a country with its largest city, the
                         label of the zone's button; largest first
  C  CC  zone  names     one city of a country that has several zones: its
                         normalised names (name, ASCII name and the Latin,
                         Cyrillic and Hebrew alternates) joined by '|'; largest
                         city first, so the lookup lets the biggest city win a
                         name clash inside one country

  T  CC  city  pop       one of the largest cities of a country the bot offers
                         as a button on the city question (TOP_PER_COUNTRY of
                         them, largest first)

Countries that geotz.countryZones already answers with a single zone are left
out of Z and C: the bot never asks there. T covers SUGGEST_COUNTRIES, which
does include them.

Normalisation MUST stay identical to geotz.normalize (Go): lower-case,
decompose, drop combining marks, every run of non letter/digit characters is
one space.
"""
import gzip
import io
import re
import sys
import unicodedata
import zipfile

ROOT = __file__.replace("\\", "/").rsplit("/ops/geotz/", 1)[0]
GEOTZ = ROOT + "/apps/backend/internal/platform/geotz"
TOP_PER_COUNTRY = 30

# Europe, the United States, Mexico, South America, and Israel (clients there).
SUGGEST_COUNTRIES = set((
    "AL AD AM AT AZ BY BE BA BG HR CY CZ DK EE FO FI FR GE DE GI GR HU IS IE IM IT XK LV LI LT LU MT MD MC ME NL MK NO PL PT "
    "RO RU SM RS SK SI ES SE CH TR UA GB VA US MX AR BO BR CL CO EC GY PY PE SR UY VE IL"
).split())
LETTERS = re.compile("^[A-Za-zÀ-ɏЀ-ӿא-ת .'-]+$")


def normalize(s):
    s = unicodedata.normalize("NFD", s.lower())
    s = "".join(c for c in s if unicodedata.category(c) != "Mn")
    out, space = [], True
    for c in s:
        if c.isalpha() or c.isdigit():
            out.append(c)
            space = False
        elif not space:
            out.append(" ")
            space = True
    return "".join(out).strip()


def single_zone_countries():
    src = open(GEOTZ + "/geotz.go", encoding="utf-8").read()
    block = src.split("var countryZones", 1)[1].split("}", 1)[0]
    return set(re.findall(r'"([A-Z]{2})":', block))


def main(zip_path):
    skip = single_zone_countries()
    rows = []
    every = []
    with zipfile.ZipFile(zip_path) as z:
        with z.open("cities15000.txt") as f:
            for line in io.TextIOWrapper(f, encoding="utf-8"):
                p = line.rstrip("\n").split("\t")
                cc, tz, pop = p[8], p[17], int(p[14] or 0)
                every.append((pop, cc, p[1]))
                if cc in skip or not tz:
                    continue
                rows.append((pop, cc, tz, p[1], p[2], p[3].split(",")))
    rows.sort(key=lambda r: -r[0])
    zones = {}
    for pop, cc, tz, name, ascii_name, _ in rows:
        zones.setdefault(cc, {}).setdefault(tz, ascii_name or name)
    out = []
    for cc in sorted(zones):
        for tz, city in zones[cc].items():
            out.append("Z\t%s\t%s\t%s\n" % (cc, tz, city))
    for pop, cc, tz, name, ascii_name, alts in rows:
        if len(zones[cc]) < 2:
            continue
        names = []
        for raw in [name, ascii_name] + [a for a in alts if LETTERS.match(a)][:60]:
            n = normalize(raw)
            if n and n not in names:
                names.append(n)
        out.append("C\t%s\t%s\t%s\n" % (cc, tz, "|".join(names)))
    every.sort(key=lambda r: -r[0])
    taken = {}
    for pop, cc, name in every:
        if cc in SUGGEST_COUNTRIES and taken.get(cc, 0) < TOP_PER_COUNTRY:
            taken[cc] = taken.get(cc, 0) + 1
            out.append("T\t%s\t%s\t%d\n" % (cc, name, pop))
    data = "".join(out)
    with open(GEOTZ + "/cities.tsv.gz", "wb") as raw:
        with gzip.GzipFile(fileobj=raw, mode="wb", mtime=0) as gz:
            gz.write(data.encode("utf-8"))
    print(len(out), "lines,", len(data), "bytes plain")


main(sys.argv[1])
