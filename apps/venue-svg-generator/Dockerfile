# Venue SVG generator: XLSX -> seating-json-v1 -> SVG.
# Runtime only needs openpyxl (requirements.txt). The HTTP service lands in G2a,
# so for now the image carries the CLI: `python -m m1_product --help`.
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

RUN useradd --system --no-create-home --uid 10001 venuegen
USER venuegen

# Smoke check at build time: the three pipeline packages import.
RUN python -c "import xlsx_seating_adapter, seating_svg_renderer, m1_product"

CMD ["python", "-m", "m1_product", "--help"]
