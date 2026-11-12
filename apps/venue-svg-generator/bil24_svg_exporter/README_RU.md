# Ограниченный Bil24 authoring exporter

Один SVG содержит посадочные circles, `g inkscape:label="#Сектор"/title` рядов,
`circle/title` номеров, PriceCategory, PriceCategoryText, Legend, LegendText,
сцену и видимые подписи. Это авторский SVG, не готовый BSS; `sbt:*` не создаётся.

```powershell
python -m venue_generator bil24 seating.json --profile presentation.json --review export-review.json --output-dir new-run
```

Вход — неизменённый source-only seating-json-v1. Независимый review receipt
`bil24-export-review-v1` обязателен: PASS, SHA-256 канонического JSON модели,
SHA-256 источника, evidence и полный список пар `[row, number]` строками.
Receipt — доверенное локальное решение ревьюера, не криптографическая подпись
и не средство авторизации коммерческого использования. QUARANTINE исходника
сохраняется в provenance; без проверки каждой пары экспорт запрещён.

Профиль `bil24-presentation-v1` задаёт единственный сектор и его evidence,
canvas <=2000px, радиус, обратимое affine-преобразование source pixels,
расположение сцены/подписей и TEST_ONLY категорию с `%price%`. Цены не назначаются.
Преобразование изменяет только представление, не исходные координаты/номера.
Нет алгоритмов поиска мест, OCR, AI/API или чтения эталонного SVG.

Выход в новую директорию: authoring.svg, validation-summary.json,
provenance.json, manifest.json (последним). Существующая директория не перезаписывается.
Наличие manifest с COMPLETE_TEST_ONLY означает завершение локального экспорта,
а не успешный импорт в Bil24. Импорт и поведение платформы проверяет владелец
на тестовом мероприятии. Не поддерживаются несколько секторов, реальные цены,
автоматическое распределение категорий, BSS и непроверенные identities.


## Выравнивание для отображения

По умолчанию source_affine сохраняет положение каждого source coordinate.
Опциональный layout.mode=uniform_rows выравнивает подтверждённые непрерывные
ряды: общий seat_pitch, центр ряда из крайних source points, общая высота из
средней source y. Порядок берётся по source x, номера не присваиваются/меняются.
Нужны contiguous_rows_confirmed=true и evidence; при gaps или одинаковых source x
преобразование запрещено. Исходные координаты остаются в модели и data-source-x/y,
метаданные отдельно хранят affine scale и регуляризацию (она не обратима).
Это нормализация presentation, не исправленная точная физическая Geometry.

seat_stroke_width настраивается (по умолчанию 3 px по локальному руководству).
Тонкая обводка 1 px соответствует пожеланию владельца и наблюдаемому стилю
ручных схем; при её выборе соответствие правилу 3 px НЕ заявляется, поведение
импортёра Bil24 необходимо проверить вручную. Минимальные/максимальные зазоры
каждого ряда включены в validation-summary.json.


## Общая настройка через CLI

```powershell
python -m venue_generator bil24 seating.json --profile presentation.json --review export-review.json --output-dir new-run --uniform-rows-evidence "Исходник проверен: ряды непрерывные, проходов внутри рядов нет"
```

Код profiles.uniform_contiguous_rows задаёт общие параметры: контур 1px, видимый
зазор 9px, шаг центров `2*seat_radius + stroke_width + gap`. Это работает для
любой модели в текущем ограниченном контракте экспортёра, не только Thermal.
Числа мест/рядов и названия площадок в настройке отсутствуют. По желанию:
`--seat-gap 12 --seat-stroke-width 0.5`. Профиль и Seating JSON не переписываются.
Без подтверждения непрерывности сохраняется исходный layout профиля; при gaps
нормализация запрещена. Это не универсальная обработка проходов и блоков.


## Структурная совместимость Arena ImportSVG

Подписи PriceCategoryText и LegendText находятся внутри соответствующих служебных
групп. Функциональные ряды содержат только title и circles. Видимые номера вынесены
в отдельную группу SeatNumberLabels без inkscape:# метки; в standalone SVG она
рисуется после кругов. Это предотвращает row_missing_title и seat_not_circle.
Изменение проверено настоящим локальным Arena ImportSVG для Thermal (243 места).
Декор сохраняется в DecorSVG, но Admin Preview его намеренно не рисует. Widget
рисует декор под живыми креслами, поэтому импорт не доказывает видимость цифр
поверх live-seat fill. Проверка в UI и импорт Bil24 — отдельные критерии.


## Forzum Legend

После реального отказа `Legend attribute 'None' not found` служебная форма
приближена к принятому ручному SVG: circles Sold/None/Reserved/MyTickets идут
первыми, затем LegendText с #LegendText; PriceCategoryText имеет свою метку.
Цвет/контур/размер текста дублируются в inline style согласованно с атрибутами.
Наличие id=None само по себе не доказывает импортируемость: Forzum исходники
недоступны, точный алгоритм его чтения пока не установлен. Локальная проверка
service representation и реальный Arena ImportSVG не заменяют повторный Forzum
тест владельца. Все места, номера, category TEST_ONLY и интервалы сохранены.


## Размер для Forzum

Реальный сервер отклонил обработанный SVG при 493 байтах/место (лимит 400).
Это ограничение размера сериализованной схемы, а не ошибка кодировки.
CLI теперь автоматически выдаёт compact SVG: функциональные title/inkscape labels,
служебные ID/цвета, сцена, номера и декор сохраняются; длинные ID сокращены,
дублирующиеся диагностические data-* вынесены в provenance.json/seat_element_mapping.
Общие стили номера наследуются от группы, координаты сериализуются с 12 значащими
цифрами (точность проверяется относительно model/presentation). Модель не меняется.
В SVG остаются source/model SHA-256; полная трассировка находится в sidecar.

Перед публикацией обязателен input budget <=250 байт/место с запасом к наблюдаемым
400 для processed BSS. Это отдельные величины: реальный обработанный размер
без Forzum неизвестен. При превышении входного бюджета CLI возвращает FAIL
и не создаёт готовый пакет. Даже этот gate не объявляет реальный серверный PASS.

## Unpriced category template

Profiles may declare category_template="bil24-unpriced-slots-v1", an explicit
category_template_evidence, category_palette (1 to 15 unique label/color slots)
and default_category. Every slot has price_text="%price%": this is an unset-price
placeholder, not a starting price of zero. All seats receive the configured
default category; other slots contain zero seats. Categories are a target-profile
configuration, not extracted commercial data. The source Seating JSON is unchanged.
The previous single TEST_ONLY profile remains supported for isolated experiments.
The CLI validates slot labels/colors, default assignment, placeholder count and
input size before publication. A local importer PASS does not replace a real
Forzum upload test for the new template.


### Подгонка геометрии по исходнику

Необязательный `geometry_regularization` в presentation-profile требует `evidence`
и явных `segments`: упорядоченный список исходных seat keys и `kind: line|arc`.
Прямая подгоняется методом TLS; дуга — общим центром и радиусом. Между концами
подтверждённого участка центры располагаются с равным шагом по прямой или дуге.
Исходные координаты и identities не изменяются. Нумерация из геометрии не выводится.
Для включённой функции нужен numpy; при выключенной функции он не импортируется.

Проходы следует разделять на разные segments. Участок с шагом более 1.65 или менее
0.55 медианного шага не изменяется. Максимальная коррекция по умолчанию — 0.35 шага,
настройка не может превышать 0.5. Превышение, разворот, непроверенная кривизна
оставляют исходную геометрию; причина и максимальное смещение сохраняются
в provenance. Весь П-образный ряд нельзя объявлять одной дугой: углы и проходы
нуждаются в отдельных подтверждённых участках. Проверки пересечений и импорта
остаются обязательными. Перспектива фотографии не исправляется без независимых
опорных точек: иначе реальный наклон ряда можно ошибочно принять за искажение.

Подтверждённые горизонтальные/вертикальные участки получают `axis: horizontal|vertical`.
Это фиксирует точное направление, а не сохраняет малый наклон скана. `axis_group`
объединяет участки одного ряда по общей оси, в том числе по разные стороны прохода.
Координата общей оси — медиана исходных центров всех участков группы. Предел
смещения остаётся обязательным; для дуг и намеренно наклонных рядов axis не задаётся.
