# BUDGET_POLICY — Anthropic API

## Gates

- До любого платного вызова: актуальные Console organization/workspace, промобаланс, expiry, auto-reload и spend limit подтверждаются вручную.
- Локальная команда должна иметь явный `-Approved`.
- API-ключ читается только из Windows Credential Manager и передаётся через pipe.
- Не используется Claude Max/Claude Code subscription authentication.

## Лимиты

| Уровень | Лимит |
|---|---:|
| orchestration smoke-test | $1 максимум |
| первый development milestone | $10 максимум |
| одна задача | значение в task packet |
| retries | 0 по умолчанию |

SDK `maxBudgetUsd` является защитным ограничением, но не математической гарантией итогового счёта. Оркестратор обязан также проверять сумму в локальном ledger и останавливать новые задачи при неизвестной стоимости или исчерпании локального лимита.

## Ledger

Для каждого разрешённого запуска сохраняются только model, usage, reported cost, duration, attempt ID, budget и статус. API-ключ, его длина и фрагменты не сохраняются.

## Запрещено

Автоматически повышать Console spend limit, включать auto-reload, запускать повтор после ошибки, удалять latch, использовать production/staging или платить внешним сервисам без отдельного разрешения.
