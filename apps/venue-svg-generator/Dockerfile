# Venue SVG generator HTTP service (HALL_MODEL.md section 7): files -> seating-json-v2 -> SVG and PNG.
# Needs no network at run time. Listens on 8090 inside the compose network only. Every route but
# /healthz needs VENUE_GEN_TOKEN; the process refuses to start without it.
FROM python:3.12-slim

ARG VENUE_GEN_COMMIT=unknown

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    VENUE_GEN_COMMIT=${VENUE_GEN_COMMIT}

# The preview PNG draws its texts with /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf.
RUN apt-get update \
    && apt-get install -y --no-install-recommends fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt ./
RUN pip install -r requirements.txt

COPY xlsx_seating_adapter ./xlsx_seating_adapter
COPY pdf_seating_adapter ./pdf_seating_adapter
COPY raster_seating_adapter ./raster_seating_adapter
COPY venue_generator ./venue_generator
COPY bil24_svg_exporter ./bil24_svg_exporter
COPY hall_model ./hall_model
COPY hall_preview ./hall_preview
COPY arena_export ./arena_export
COPY seating_svg_renderer ./seating_svg_renderer
COPY m1_product ./m1_product
COPY service ./service

RUN useradd --system --no-create-home --uid 10001 venuegen
USER venuegen

# Smoke check at build time: every runtime package, the two schemas and the font. The test-only
# switch is set for this one line and is never part of the image environment.
RUN VENUE_GEN_ALLOW_NO_TOKEN=test-only python -c "import xlsx_seating_adapter, pdf_seating_adapter, raster_seating_adapter, venue_generator.schematic, venue_generator.operations, bil24_svg_exporter, hall_model, hall_preview, arena_export, seating_svg_renderer, m1_product, service.app; from hall_model import load_schema; load_schema(); import pathlib; assert pathlib.Path('/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf').is_file(); assert pathlib.Path('hall_model/hall-structure-v1.schema.json').is_file()"

EXPOSE 8090
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8090/healthz', timeout=3)"]
CMD ["uvicorn", "service.app:app", "--host", "0.0.0.0", "--port", "8090"]
