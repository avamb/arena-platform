# Venue SVG generator

> Живёт в монорепо arena-platform, `apps/venue-svg-generator`, с 14.10.2026. Первоисточник — архив GitHub-репозитория `avamb/Venue_SVG_Generator` (не удалён и не заархивирован, решение владельца). История коммитов перенесена `git subtree add` без squash.

Python-конвейер: XLSX → `seating-json-v1` → SVG. Цель по ROADMAP M3 — авторский Inkscape-SVG под `seating.ImportSVG` платформы. HTTP-слой (задача G2a) описан ниже. Подробности: `PRODUCT_SPEC.md`, `ROADMAP.md`, `m1_product/README_RU.md`.

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

Сервис `venue_gen` в корневом `docker-compose.yml` — под профилем `venue-gen` и без публикации порта: к нему ходит только arena-api по сети compose (`http://venue_gen:8090`). Аутентификации нет, поэтому порт наружу публиковать нельзя.

## HTTP

Сервис без состояния: ни базы, ни диска между перезапусками. Задание живёт в памяти процесса `VENUE_GEN_JOB_TTL_S` секунд после завершения (по умолчанию 3600), потом `GET /jobs/{id}` отвечает 404. Одновременно идёт одно задание, остальные ждут в очереди (не больше `VENUE_GEN_MAX_QUEUE`, по умолчанию 20, иначе 503 `queue.full`). Каждое задание работает в отдельном процессе, по таймауту `VENUE_GEN_JOB_TIMEOUT_S` (по умолчанию 120) оно убивается, а память процесса ограничена `VENUE_GEN_JOB_MEMORY_MB` (по умолчанию 2048, `RLIMIT_AS`, только Linux; при нехватке задание падает с `memory_limit`). Поверх этого у контейнера в compose стоит `mem_limit: 3g`. Распаковка архива XLSX сверх 200 МБ отклоняется сразу.

Отчёт возвращает клиенту структуру его же книги (`source.workbook_sheets`, `source.sheets`: названия листов, объединённые ячейки). Название листа входит и в ключи мест (`xlsx-seat-<лист>-<ячейка>`). Имя загруженного файла и пути хоста в ответе нет, набор ключей `source` закреплён тестом.

| Маршрут | Что делает |
| --- | --- |
| `POST /generate` | multipart: `file` (XLSX, до `VENUE_GEN_MAX_UPLOAD_MB`, по умолчанию 10 МБ, иначе 413), `profile` (`hunter-v1`, `aribau-v1`, `synthetic-v1`, иначе 400), `target_profile` (JSON, необязательно). Ответ `202 {job_id, status, status_url}` |
| `GET /jobs/{id}` | `{status: queued\|running\|done\|failed, validation_status, report, svg_url, diagnostic_svg_url, error}` |
| `GET /jobs/{id}/svg` | авторский SVG под `seating.ImportSVG`. Только при `PASS`; при `QUARANTINE` — 409 `job.quarantined` |
| `GET /jobs/{id}/diagnostic.svg` | диагностический рисунок рендерера, карантинные места помечены группой `quarantine` |
| `GET /healthz` | `{"status":"ok"}` |

`report` — это `seating-json-v1` (места с `confidence`/`number_status`, `validation.status` PASS или QUARANTINE, причины в `validation.issues`) плюс `provenance`: хеш и размер загруженного файла. Пути хоста и имя файла клиента в ответы не попадают. Ошибка всегда `{"error": {"code", "message"}}`.

`target_profile` — профиль выхода (по умолчанию `arena_export/profiles/arena-authoring-v1.json`): временная категория (`category.name`, `category.color`), необязательные `canvas` (`width`, `height`), `seat_radius`, `stroke_width_px`, `legend`. Обязательно `"commercial_data": false`. Без `canvas` холст — максимальные координаты мест плюс 2, радиус по умолчанию 0,32; холст не больше 2000×2000.

Пробный запуск без клиентских данных (`synthetic-v1`):

```powershell
python -c "from xlsx_seating_adapter.synthetic import build_synthetic_workbook; build_synthetic_workbook('demo.xlsx')"
uvicorn service.app:app --port 8090
curl.exe -F file=@demo.xlsx -F profile=synthetic-v1 http://127.0.0.1:8090/generate
```

Экспортёр отдельно: `python -m arena_export --source model.json --out plan.svg`.

Контракт M3 проверяется в CI арены: Go-тест `apps/backend/internal/domain/seating/venue_gen_authoring_test.go` прогоняет `arena_export/testdata/synthetic_authoring.svg` через `seating.ImportSVG`. После намеренного изменения экспортёра файлы пересоздаются командой `VENUE_GEN_REGEN_FIXTURE=1 python -m pytest arena_export` (PowerShell: `$env:VENUE_GEN_REGEN_FIXTURE="1"`).
