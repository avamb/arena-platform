# raster_seating_adapter

Ограниченный адаптер растра: настраиваемый детектор синих компонентов-мест. Это **не OCR**.

- Вся геометрия берётся из профиля (ROI, пороги цвета, площади, допуск полос). Размеры, номера и количества в коде не зашиты.
- Автоматического распознавания номеров нет. По умолчанию все номера и ряды `unknown`.
- Необязательные наблюдения Claude Vision (`raster-label-observations-v1`) — недоверенные данные. Они привязаны к SHA256 источника, SHA256 профиля и точному bbox кандидата; весь пакет проверяется целиком до применения.
- Применённые номера получают `ai_observed_pending_review`; все места остаются `QUARANTINE`, `sellable=false`. Пакет не может сам объявить проверку или PASS.
- Геометрические полосы (`band-001`) — не официальные ряды.
- Диагностический SVG: жёлтый — наблюдаемый номер, серый `?` — неизвестный.

API: `extract_image(path, profile, observations=None)`, `render_diagnostic(model, path)`.

Статус: тесты не запускались автором; приёмка и CLI — за Codex.

## Воспроизводимый локальный запуск
`python -m raster_seating_adapter source.jpg --profile local-profile.json --output-dir new-run`
Опционально `--observations local-observations.json`: SHA-256 источника и профиля, candidate ID, band и bbox должны совпасть. Evidence crop/observer/raw values сохраняются, низкая confidence не превращается в присвоенный номер. Все места QUARANTINE и sellable=false.
Это детектор цветовых компонентов, не OCR. Номера из последовательности не назначаются. CLI не вызывает AI; получение Vision observations — отдельный явно разрешённый этап.
Проверки: `python -m pytest pdf_seating_adapter/tests raster_seating_adapter/tests -q`.
Существующая output directory запрещена; итоговый manifest публикуется после всех файлов. FAIL оставляет INCOMPLETE и диагностические JSON, не готовую схему.
