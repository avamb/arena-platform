# Изолированная проверка Claude Agent SDK

Подготовлено 11 октября 2026 года.

**Обновление 11.10.2026:** первый разрешённый запрос уже выполнен,
`success / completed`, модель `claude-sonnet-5-5`, отчётная стоимость
$0.0089545. Ниже сохранены инструкции подготовки. Защита `results/attempt.json`
активна; повторно запускать платную команду нельзя. Ошибка классификации
`completed` исправлена локально, исходный отчёт сохранён; пересчитанная копия —
`results/verification.corrected.json`. Регрессионная проверка: `npm run test:reporting`.

## Установка и окружение

- Node.js 22.13.1; npm 10.9.2.
- Официальный TypeScript SDK `@anthropic-ai/claude-agent-sdk` закреплён на `0.3.296`.
- Используется поставляемый с SDK Windows x64 CLI `2.1.296`, а не глобальный CLI `2.1.119`.
- Runner написан на JavaScript ESM (`.mjs`), поддерживаемом TypeScript SDK: компилятор, tsx и приложение не требуются.
- Все зависимости находятся в локальном `node_modules`; версии и integrity закреплены в `package-lock.json`.
- Системный Python 3.13.9 обнаружен, но создание venv не удалось из-за ошибки ensurepip. Повтор с bundled Python 3.12.14 также не удался; после двух попыток диагностика остановлена и выбрана рабочая Node-среда. Неудачный venv удалён.
- Папка Venue_SVG_Generator не является Git-репозиторием. Репозиторий arena_new имеет ранее существовавшие незакоммиченные изменения; его файлы не изменялись.

Для восстановления зависимостей: `npm ci --ignore-scripts --no-audit --no-fund` из этой папки. Optional dependencies не отключать: они содержат локальный CLI. Не обновлять версии перед первым тестом без повторной локальной проверки.

## Что можно проверить бесплатно

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator\sdk_experiments'
npm run check
```

Эта команда импортирует SDK, проверяет policy/hooks, выполняет только `claude.exe --version` и не вызывает `query()`. Проверены разрешённое чтение, повторное чтение, выход за папку, ADS/UNC, неизвестные инструменты, подагенты и отсутствие наследуемых auth-токенов. Это локальные проверки конфигурации; исполнение hooks внутри реальной SDK-сессии и API-авторизация будут проверены только после разрешённого запроса.

## До оплаты: проверить Console

В нужной организации Anthropic Console:

1. Открыть **Settings → Billing**. Найти настройки **Auto-reload**, выключить автоматическое пополнение и сохранить изменение. Не путать API Billing с Extra Usage подписки Claude.
2. Проверить запись именно вашего промогранта $100: остаток, срок действия (по исходному заданию 24.10.2026) и допустимые услуги Anthropic API/Agent SDK. Общие статьи о подписочных API-кредитах не доказывают условия этого гранта.
3. Открыть **Settings → Workspaces**, проверить имя workspace, его расходные лимиты и принадлежность будущего API-ключа. Создать/выбрать Console API key этого workspace, не подписочный OAuth-токен.
4. Проверить лимиты организации и workspace, зафиксировать баланс до теста. Лимиты и SDK-бюджет не являются математической гарантией окончательного счёта: возможны параллельные расходы и превышение оценки завершающим ответом.
5. Скопировать `console-confirmation.example.json` в `console-confirmation.json`; заполнить фактические organization/workspace, баланс, срок и подтверждения. `checked_at` — ISO 8601 с timezone, например `2026-10-11T15:00:00+02:00`; использовать фактическое время проверки, не пример. Все подтверждения должны быть true. Runner принимает проверку давностью менее суток и отказывается работать в день истечения гранта.

Названия организации/workspace пока неизвестны. JSON содержит подтверждение пользователя, не независимую проверку Console. Через SDK невозможно по одному имени workspace доказать принадлежность ключа; это необходимо проверить в Console перед запуском. Файл подтверждения игнорируется Git и не должен содержать API-ключ.

Источники: [API billing и prepaid credits](https://support.claude.com/en/articles/8977456-how-do-i-pay-for-my-claude-api-usage), [лимиты API](https://platform.claude.com/docs/en/api/rate-limits).

## Безопасная передача ключа и платный запуск

Для постоянного хранения используется Windows Credential Manager. Generic Credential принадлежит текущему пользователю Windows и имеет target `Venue_SVG_Generator/Anthropic/APIKey`; системные и доменные записи не используются. Нативный helper `windows-credential.ps1` вызывает `CredWrite`, `CredRead` и `CredDelete` без модулей и дополнительных зависимостей:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator\sdk_experiments'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\windows-credential.ps1 -Action Set
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\windows-credential.ps1 -Action Status
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\windows-credential.ps1 -Action Delete
```

`Set` заменяет существующую запись после скрытого ввода. `Status` показывает только `Present`/`Missing`, target, тип и область current-user. `GetForPipe` отказывается писать в интерактивный терминал и используется только launcher-ом через redirected stdin; ключ не появляется в командной строке, файле, логе или глобальном окружении. `Delete` ограничен namespace `Venue_SVG_Generator/` и не удаляет другие записи Windows. Отзыв ключа у Anthropic выполняется отдельно в Console.

Только после явного одобрения пользователя:

```powershell
& 'C:\Projects\Venue_SVG_Generator\sdk_experiments\run-approved-test.ps1' -Approved
& 'C:\Projects\Venue_SVG_Generator\sdk_experiments\run-ladvi-approved-test.ps1' -Approved -AttemptId 'workspace-header-v1'
```

Для Ládví перед запуском необходимо создать `workspace-confirmation.json` из
`workspace-confirmation.example.json`, указав фактический `workspace_id` из
Anthropic Console (формат `wrkspc_...`) и `confirmed: true`. Имя `Default` не
является идентификатором. Каждый повтор получает новый `-AttemptId`; старый
`results/ladvi-attempt.json` и старый отчёт не перезаписываются. Runner
останавливается до чтения ключа и до API-вызова, если ID отсутствует или не
подтверждён.

Launcher читает Generic Credential в память и передаёт его Node через redirected stdin; ключ не помещается в командную строку, файл, глобальные настройки Windows или переменные текущего PowerShell. Agent SDK runner передаёт `ANTHROPIC_API_KEY` только локальному дочернему CLI. Vision runner получает ключ непосредственно из stdin. SDK использует официальный endpoint `https://api.anthropic.com`, отдельный `CLAUDE_CONFIG_DIR`; существующая подписочная авторизация не читается и не изменяется.

Локальная проверка без API-ключа:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\windows-credential.ps1 -Action SelfTest
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\test-credential-pipes.ps1
```

Проверка использует только фиктивные targets в `Venue_SVG_Generator/Test/`, после чего удаляет их. Она не затрагивает production target и не выполняет API-запросов.

Не вставлять ключ в чат, не включать трассировку PowerShell/Node или дампы процессов. Ключ кратковременно существует в памяти и окружении дочернего процесса; защита от администратора машины не заявляется. Вывод редактируется, сырой stderr CLI подавляется.

## Ограничения runner

- Модель `claude-sonnet-5-5`, без fallback; доступность этому workspace ещё не проверена.
- `maxTurns: 2`, `maxBudgetUsd: 0.25`, низкий effort, output cap 512 токенов.
- Один вызов `query()`; нет resume, дополнительных сессий и повторов в коде.
- `CLAUDE_CODE_MAX_RETRIES=0`; watchdog выключен, тайм-аут 90 секунд. При тайм-ауте запрос уже мог быть оплачен.
- `tools: ['Read']`, явный deny опасных и MCP-инструментов; `PreToolUse` отклоняет любой инструмент, кроме единственного чтения `workspace/SDK_TEST_CONTEXT_RU.md`.
- Полные/относительные пути проверяются через realpath; другие файлы, ссылки, UNC и ADS запрещены. `allowedTools` не используется как единственная защита.
- `settingSources: []`, нет skills/plugins/agents/MCP, `persistSession: false`, отдельная конфигурационная папка. Системная managed policy, если установлена, может всё равно применяться; этот setup её не изменяет.
- Агент не получает инструментов записи. Сам launcher/CLI могут создавать служебные файлы в изолированной папке; это ограничения инструментов, а не OS-level sandbox Windows.
- `results/attempt.json` создаётся эксклюзивно перед запросом. Повторный запуск блокируется даже после ошибки. Не удалять latch ради автоматического повтора.
- При init runner проверяет источник API-ключа, модель, инструменты и отсутствие MCP; при несовпадении прекращает сессию. Это проверка после старта, не гарантия предотвращения любого уже начавшегося расхода.

Ссылки: [права SDK](https://code.claude.com/docs/en/agent-sdk/permissions), [параметры окружения](https://code.claude.com/docs/en/env-vars), [стоимость SDK](https://code.claude.com/docs/en/agent-sdk/cost-tracking). Конкретные параметры дополнительно сверены с установленным `sdk.d.ts`.

Ориентир первого запуска: примерно **$0.02–0.10** с учётом служебного prompt и cache-токенов. Это оценка, не котировка. Текущая опубликованная базовая цена Sonnet — $2/M входных и $10/M выходных токенов; thinking, cache и фактический контекст влияют на сумму. Бюджет $0.25 отслеживается SDK по оценке и может быть превышен завершающим ответом. [Модели и цены](https://platform.claude.com/docs/en/models/overview).

## После единственного запуска

Runner выводит и сохраняет `results/verification.json`: признак успешной авторизации, реально наблюдавшиеся модели, успешно прочитанный файл, краткое резюме, usage/modelUsage, оценку стоимости и ошибки. Если данные отсутствуют, используются null и пояснение — не нулевая стоимость. Стоимость CLI не подтверждает списание промокредитов.

Сразу остановиться. В Console проверить usage именно ожидаемых организации/workspace и изменение промобаланса; возможна задержка отчётов. Зафиксировать остаток после проверки и не запускать следующий тест без отдельного решения.
