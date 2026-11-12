# Venue SVG generator

> Живёт в монорепо arena-platform, `apps/venue-svg-generator`, с 14.10.2026. Первоисточник — архив GitHub-репозитория `avamb/Venue_SVG_Generator` (не удалён и не заархивирован, решение владельца). История коммитов перенесена `git subtree add` без squash.

Python-конвейер: XLSX → `seating-json-v1` → SVG. Цель по ROADMAP M3 — авторский Inkscape-SVG под `seating.ImportSVG` платформы. HTTP-слой (G2a, версия G2b) описан ниже. Подробности: `PRODUCT_SPEC.md`, `ROADMAP.md`, `m1_product/README_RU.md`.

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

Сервис `venue_gen` в корневом `docker-compose.yml` — под профилем `venue-gen` и без публикации порта: к нему ходит только arena-api по сети compose (`http://venue_gen:8090`). Сервис требует `VENUE_GEN_TOKEN` (общий секрет с arena-api), но порт всё равно наружу публиковать нельзя.

## HTTP

Контракт целиком — `HALL_MODEL.md`, раздел 7 (он главный; код и тесты ему подчиняются). Кратко:

- Порт 8090, только внутренняя сеть. Каждый маршрут, кроме `GET /healthz`, требует `Authorization: Bearer <VENUE_GEN_TOKEN>`, иначе 401 `auth.required`. Процесс не стартует с пустым `VENUE_GEN_TOKEN` (исключение для тестов: `VENUE_GEN_ALLOW_NO_TOKEN=test-only`).
- `POST /generate` (multipart: `files`, `kinds`, необязательно `profile`, `raster_profile`, `target_profile`, `decor_labels`) ставит задание в очередь, `GET /jobs/{id}` отдаёт файлы-отчёты, модель `seating-json-v2`, авторский SVG и PNG-превью; `GET /jobs/{id}/svg` и `GET /jobs/{id}/preview.png` отдают их отдельно.
- Синхронные маршруты: `POST /render`, `POST /schematic`, `POST /apply-operations` (с `dry_run` и `confirm_large_change`).
- Состояния нет: задания живут в памяти `VENUE_GEN_JOB_TTL_S` секунд (3600). Одно задание за раз, очередь `VENUE_GEN_MAX_QUEUE` (20), таймаут `VENUE_GEN_JOB_TIMEOUT_S` (120), память `VENUE_GEN_JOB_MEMORY_MB` (2048, только Linux). Файл до `VENUE_GEN_MAX_UPLOAD_MB` (10), все вместе втрое больше; JSON-тело до `VENUE_GEN_MAX_MODEL_MB` (8). Размер тела проверяется во время чтения: ответ 413 приходит, как только лимит превышен, в том числе без `Content-Length`. Завершённых заданий в памяти не больше `VENUE_GEN_MAX_KEPT_JOBS` (50, старые уходят первыми). Синхронных запросов одновременно не больше `VENUE_GEN_SYNC_CONCURRENCY` (2); запрос, не получивший место за `VENUE_GEN_SYNC_WAIT_S` секунд (10), получает 503 `busy`. JSON глубже 64 уровней, `NaN` и `Infinity` дают 400 `request.invalid`. Коммит в ответах берётся из `VENUE_GEN_COMMIT` (в образе — `--build-arg VENUE_GEN_COMMIT=<sha>`).
- Тип файла определяется по содержимому, а не по `kinds` и имени. Имя файла клиента не используется, не пишется в лог и не возвращается.

Пробный запуск без клиентских данных:

```powershell
python -c "from xlsx_seating_adapter.synthetic import build_synthetic_workbook; build_synthetic_workbook('demo.xlsx')"
$env:VENUE_GEN_TOKEN = "dev-token"
uvicorn service.app:app --port 8090
curl.exe -H "Authorization: Bearer dev-token" -F files=@demo.xlsx -F 'kinds=["xlsx"]' http://127.0.0.1:8090/generate
```

Экспортёр отдельно: `python -m arena_export --source model.json --out plan.svg` (формат v1).

Контракт M3 проверяется в CI арены: Go-тест `apps/backend/internal/domain/seating/venue_gen_authoring_test.go` прогоняет `arena_export/testdata/synthetic_authoring.svg` через `seating.ImportSVG`. После намеренного изменения экспортёра файлы пересоздаются командой `VENUE_GEN_REGEN_FIXTURE=1 python -m pytest arena_export` (PowerShell: `$env:VENUE_GEN_REGEN_FIXTURE="1"`).

## M2 packages (ported 2026-11-12)

Source branch: `feature/m2-vector-pdf-adapter`, tip `ee2b73b`, plus the uncommitted row-edge-alignment check in `venue_generator/table_pipeline.py`.

- `pdf_seating_adapter`: reads a vector PDF plan and extracts seats, sections and printed labels.
- `raster_seating_adapter`: reads a JPG or PNG plan with a profile and finds seat marks.
- `venue_generator`: the single CLI over the xlsx, PDF and raster adapters. It also holds the table pipeline, organizer inventory, comments and clarification steps.
- `bil24_svg_exporter`: turns a checked seating model into an SVG for import.

Left behind on purpose:

- `results/` folders: they hold per-client analyses.
- `m2_fixtures/`: copies of client source files.
- Client profiles: they are bound to the SHA-256 of a client file.
- All pdf, xlsx, image and zip files.

The repository is public, so none of these may be committed. A test that needs a private file must carry `@pytest.mark.private_data`. `conftest.py` skips it when the private data is absent. Run all tests with `python -m pytest` from this folder.
