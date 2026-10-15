# Venue SVG generator

> Живёт в монорепо arena-platform, `apps/venue-svg-generator`, с 14.10.2026. Первоисточник — архив GitHub-репозитория `avamb/Venue_SVG_Generator` (не удалён и не заархивирован, решение владельца). История коммитов перенесена `git subtree add` без squash.

Python-конвейер: XLSX → `seating-json-v1` → SVG. Цель по ROADMAP M3 — авторский Inkscape-SVG под `seating.ImportSVG` платформы. HTTP-слоя ещё нет (задача G2a). Подробности: `PRODUCT_SPEC.md`, `ROADMAP.md`, `m1_product/README_RU.md`.

## Запуск

```powershell
python -m pip install -r requirements.txt
python -m m1_product --help
```

## Тесты

```powershell
python -m pip install -r requirements-dev.txt
python -m pytest -q -rs
```

`benchmark_v1` не собирается: он импортирует `seating_poc`, которого нет в репозитории (папка в `.gitignore`). Девять тестов читают приватные клиентские XLSX и сохранённые результаты (`ChatExport_*`, `*/results`, `seating_poc`). Если этих папок нет рядом с `conftest.py`, такие тесты пропускаются; при их наличии идут все.

## Образ и стенд

```powershell
docker build -t arena_new/arena-venue-gen:dev apps/venue-svg-generator
docker compose --profile venue-gen build venue_gen
```

Сервис `venue_gen` в корневом `docker-compose.yml` — под профилем `venue-gen` и без публикации порта: к нему ходит только arena-api по сети compose (после G2a).
