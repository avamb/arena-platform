# Venue SVG generator: XLSX -> seating-json-v1 -> SVG.
# Runtime: openpyxl + FastAPI/uvicorn (requirements.txt). The HTTP service listens on 8090
# inside the compose network only; the CLI is still available: `python -m m1_product --help`.
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /app

COPY requirements.txt ./
RUN pip install -r requirements.txt

COPY xlsx_seating_adapter ./xlsx_seating_adapter
COPY seating_svg_renderer ./seating_svg_renderer
COPY m1_product ./m1_product
COPY arena_export ./arena_export
COPY service ./service

RUN useradd --system --no-create-home --uid 10001 venuegen
USER venuegen

# Smoke check at build time: the pipeline packages and the HTTP app import.
RUN python -c "import xlsx_seating_adapter, seating_svg_renderer, m1_product, arena_export, service.app"

EXPOSE 8090
CMD ["uvicorn", "service.app:app", "--host", "0.0.0.0", "--port", "8090"]
