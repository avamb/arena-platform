# PHASE 5B — локальная подготовка Claude Vision

Подготовлен отдельный прямой Messages API runner на установленном официальном `@anthropic-ai/sdk` `0.133.0`. Существующий Agent SDK runner и его latch не переиспользуются. Модель зафиксирована как `claude-sonnet-5-5`; один запрос, `max_tokens=1800`, `maxRetries=0`, timeout 90 секунд. API-ключ читается только через скрытый SecureString-ввод PowerShell и stdin Node-процесса; в окружение, командную строку и файлы ключ не записывается.

Runner передаёт только JPG из `benchmark_v1/phase5a_ladvi/claude-input` и технический индекс координат из отдельного `seating-model.json`. Reference SVG, evaluator output и dataset manifest не входят в request. Structured output закреплён через `output_config.format.type=json_schema`. Prompt требует `null`/`unknown` и запрещает строить номера из последовательности геометрических мест.

AI-ответ сохраняется отдельно в `sdk_experiments/results/ladvi-vision.json`; исходный `seating-model.json` проверяется по SHA-256. После успешного ответа локально запускается отдельный `ladvi-vision-evaluator.mjs`, который записывает `sdk_experiments/results/ladvi-benchmark-evaluation.json`. Он использует reference только на стороне evaluator.

Локальная проверка:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator\sdk_experiments'
npm run check
npm run check:ladvi-vision
npm run test:reporting
```

Результат: исходный локальный check прошёл, Vision runner прошёл, evaluator прошёл, reporting tests 4/4. API calls: 0.

Preflight насчитал около 8 374 входных токенов с координатным индексом и оценку `$0.034748` при ориентире $2/M input и $10/M output. Реальный ответ ожидается примерно в диапазоне `$0.03–0.10`; это оценка, не гарантия. Messages API не принимает жёсткий dollar cap, поэтому защита состоит из одного запроса, отсутствия retry, фиксированного `max_tokens=1800`, timeout и эксклюзивного latch `results/ladvi-attempt.json`. Console и фактический usage остаются источником окончательной стоимости. Текущий грант/auto-reload/workspace проверяются локальным `console-confirmation.json` перед вызовом; доступность конкретного model ID онлайн до разрешения не проверялась.

Точная команда после отдельного разрешения пользователя:

```powershell
& 'C:\Projects\Venue_SVG_Generator\sdk_experiments\run-ladvi-approved-test.ps1' -Approved
```

До получения разрешения команда не выполнялась. Повторный запуск после создания latch блокируется.
