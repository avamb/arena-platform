# Локальный CLI: поддерживаемые форматы

Одна точка запуска делегирует проверенным адаптерам, не меняя extraction или acceptance.

```powershell
python -m venue_generator xlsx pack --hunter-xlsx C:\local\hunter.xlsx --aribau-xlsx C:\local\aribau.xlsx --out C:\local\runs
python -m venue_generator pdf C:\local\source.pdf --output-dir C:\local\pdf-run --section-label FLOOR --section-label BALCONY --decor-label Stage
python -m venue_generator jpg C:\local\source.jpg --profile C:\local\profile.json --output-dir C:\local\jpg-run
python -m venue_generator jpg C:\local\source.jpg --profile C:\local\profile.json --observations C:\local\observations.json --output-dir C:\local\jpg-labels
```

XLSX ограничен Hunter/Aribau; PRODUCT PASS требует обоих источников и MATCH frozen baseline.
PDF ограничен повторяющимися квадратными vector-кандидатами и числовыми source glyphs; официальные ряды не выдумываются.
JPG/PNG ограничен профилем цветовой маски; это не универсальный OCR. Опциональные наблюдения проверяются по SHA-256 и bbox, сохраняют crop evidence и остаются pending review.
PDF/JPG outputs всегда QUARANTINE, sellable=false, diagnostic SVG; exit 2 означает диагностический пакет, exit 1 — ошибку. Они пока не являются Arena/Bil24 authoring outputs.
Повторная output directory запрещена; manifest определяет завершённость пакета. Клиентские документы/профили/наблюдения и результаты остаются локальными, не в Git.
