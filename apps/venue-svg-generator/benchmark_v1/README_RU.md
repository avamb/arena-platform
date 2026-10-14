# Benchmark v1

Минимальная инфраструктура принимает готовый Seating JSON от любого конвертера; сама конвертеры, SDK и API не запускает. Модель и провайдер не участвуют в оценке. Только один scoring adapter: существующий independent evaluator Akropolis. Другие результаты принимаются как `accepted_unscored`, метрики сравнения — `unknown`. Это не универсальный scorer и не подтверждение универсальности продукта.

## Структура и граница доступа

- `benchmark.py`: подготовка реестров, проверка split и единый отчёт.
- `converter_inputs/sources.json`: исходная allowlist, включая JPG/PNG/JPEG, PDF, XLSX и DOCX. Это каталог, не разрешение читать весь архив.
- `converter_inputs/akropolis-260/`: самостоятельный пакет одного PDF и input.json, без ссылок на reference SVG.
- `evaluator_only/`: эталоны, версии и классификация пар. Доступ только отдельному процессу оценки.
- `control-submission.json`: пример провайдер-независимого результата.
- `output/`: split/readiness, shortlist, benchmark-result и подробный контроль evaluator.

Каталоги и allowlist **не обеспечивают Windows ACL или изоляцию произвольного кода**. Benchmark не запускает генератор в текущем checkout. При следующем эксперименте конвертер должен выполняться в отдельной песочнице с доступом только к выбранному source bundle и пустому output; архив, evaluator, PoC, dataset manifests и эталоны не монтируются. При невозможности такой изоляции запуск блокируется. Закрытый test остаётся предложением: требуется идентификация, фиксация конкретных версий и отдельное хранение. Ранее просмотренный автором архив нельзя назвать ретроспективно невиденным test set.

## Единый контракт

Envelope `benchmark-submission-1`: `case_id`, `converter` (id, version, method, provider, model), `seating_model_path` относительно проекта, `processing` (duration_seconds, api_cost_usd, input_tokens, output_tokens). Неизвестные значения — JSON null, в оценке `unknown`; стоимость требует указания evidence, время конвертера отдельно от времени evaluator. API-ключи и сырые логи SDK не передаются.

Seating JSON использует существующий `phase3b-1`: pages с source_page/width_pt/height_pt; geometric_rows с техническим ID, source_page, seat_count, seat_keys_left_to_right; seats с уникальным seat_key, source_page, position_pt, geometric_row_id, original_number_text, official_row_label/official_sector_label (null если неизвестны), confidence/evidence. Семантические ID не подменяются геометрическими полосами. Оригинальный текст номера сохраняется; числовое original_seat_number и source_bbox_pt необходимы текущему Akropolis adapter. Прочим адаптерам нельзя навязывать целочисленную нумерацию.

Канонические координаты: PDF points, top-left, right/down. Для растров/таблиц нормализация в points должна быть явной и обратимой; записываются исходные единицы и affine forward/inverse. Неизвестный DPI не позволяет придумывать физический масштаб. Для таблиц без координат официального плана геометрическая оценка остаётся unknown.

Общая структурная проверка вынесена из существующего валидатора в `seating_poc/model_validation.py`; исходный validator продолжает использовать её. Source-evidence validation и evaluator не переписаны: для контроля повторно используются их функции. Evaluator получил аргументы путей, значения по умолчанию сохранены; извлечение и генерация не импортируют evaluator.

## Метрики и правила

Количество: total, absolute error, страницы и эталонные сектора с явной гипотезой сопоставления. Номера: multiset внутри контекстного родителя, расхождения и сохранённые пропуски; одинаковый номер в разных группах не ошибка. Ряды: распределение геометрических групп и отдельная оценочная проекция, без объявления официальных рядов. Официальные ряды/сектора остаются unknown при отсутствии исходной семантики. Геометрия: единственное similarity alignment на страницу, transform/inverse, anchors/rank, median/max residual, нормализация на шаг соседних мест; пороги PoC 0.25/0.5 шага. Ненадёжное выравнивание — unknown, не match. Overlaps и ошибки — из существующего валидатора. Историческое время конвертации неизвестно; локальная API стоимость 0, время оценки измеряется отдельно. Суммарный «процент качества» не вычисляется, unknown не засчитывается успехом.

Геометрически проверенная пара Akropolis фиксируется на A0016 → A0143 (260 сидячих); A0144 — другая конфигурация и не подменяет эталон. 55 контекстных связей и 16 предположительных нельзя автоматически оценивать без выбора версии, конфигурации, групп и якорей. Недостаточный source не штрафуется за семантику, которой там нет; неполные изображения требуют отдельного coverage scope. Предполагаемые алиасы Santander/Zwezda и Teatro Yulia/рисунок уже удержаны внутри development; известные варианты Broadway, Kings Place, Kino MKP, Indigo/O2, Akropolis — внутри своих кластеров. Неизвестные идентичности остаются quarantine.

## Локальные команды

Использовать подготовленный Python, без установки пакетов:

```powershell
$py = 'C:\Users\andre\.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe'
& $py -B benchmark_v1\benchmark.py prepare
& $py -B benchmark_v1\test_benchmark.py
& $py -B benchmark_v1\benchmark.py run --submission benchmark_v1\control-submission.json
```

Последняя команда только оценивает существующий результат, ничего не конвертирует. При несовместимой группировке adapter сообщает `architecture_decision_required`; выбирать универсальное отображение рядов автоматически запрещено. Отрицательные тесты инфраструктуры не выполняют evaluator. Полный контроль этого этапа выполняется один раз.

Следующий эксперимент: Ládví JPG (A0149), отдельный source-only bundle, диагностика читаемости и детерминированное обнаружение мест/OCR без API. Независимый evaluator заранее уточняет конфигурацию reference A0254, но не передаёт сведения конвертеру. Затем фиксируются ошибки, unknown и время; AI comparison только после согласования подхода и бюджета.
