# AutoForge Project Pack

Обновлено: 2026-06-21

Эта папка предназначена для подготовки AutoForge к работе над проектом.

AutoForge должен использовать документы в таком порядке:

1. `00_AGENT_GUARDRAILS.md` - обязательные правила и архитектурные границы.
2. `01_CLARIFICATION_REGISTER.md` - вопросы, которые нужно уточнить до генерации полного feature backlog.
3. `02_CRITICAL_ARCHITECTURE_AUDIT.md` - P0/P1 архитектурные конфликты и исправления перед master specification.
4. `03_SPECIFICATION_STARTER.md` - стартовый пакет для подготовки master specification.
5. `../08_architecture/11_architecture_decision_log_ru.md` - proposed/accepted/blocking architecture decisions.
6. `../08_architecture/09_domain_state_machines_ru.md` - обязательные state machines и cross-domain invariants.
7. `../08_architecture/10_compliance_security_privacy_ru.md` - compliance, security, privacy и market-readiness baseline.
8. `../08_architecture/12_master_platform_specification_ru.md` - начальный черновик master specification, дополняется после закрытия P0 решений.
9. `../08_architecture/13_backend_go_initial_specification_ru.md` - начальная backend specification для Go stack.
10. `../08_architecture/00_backend_architecture_brief_ru.md` - базовая архитектурная рамка.
11. `../08_architecture/01_api_compatibility_gateway_ru.md` - совместимость с Bil24-style API.
12. `../08_architecture/02_wordpress_integration_contract_ru.md` - контракт нового WordPress-плагина.
13. `../08_architecture/03_platform_management_api_and_permissions_ru.md` - management API, площадки, seating plans и права.
14. `../08_architecture/04_large_venue_performance_strategy_ru.md` - large venue/high-demand стратегия производительности.
15. `../08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md` - типы интерфейсов, POS/external ticketing systems и пригласительные билеты без оплаты.
16. `../08_architecture/06_event_notifications_billing_reporting_ru.md` - event-driven webhooks, social publishing, billing/Stripe invoices и post-event reports.
17. `../08_architecture/07_external_allocations_scanner_ingestion_ru.md` - существующий scanner webhook sync, barcode authorities, внешние квоты, external reports ingestion, AI/MCP normalization.
18. `../08_architecture/08_platform_superadmin_observability_ru.md` - platform superadmin, one-window operations console, observability, logs, errors, load, support access и audit.

Правило запуска: до coding phase AutoForge должен пройти clarification phase, закрыть P0 decisions, подготовить master specification и получить подтверждение ключевых решений.
