# Конкурентный анализ (competitive analysis)

Индекс всех материалов по конкурентам Arena Sold Out. Ключевые слова для поиска: конкурентный анализ, конкуренты, билетные платформы, competitive analysis, ticketing competitors.

Правила: в репозитории только структура, выводы и цифры с источниками. Сырые скачанные страницы конкурентов и чужие контакты лежат вне репозитория, в приватных заметках владельца. Пометки в отчётах: [П] проверено на открытой странице, [Р] только из результата поиска, [В] вывод. «Не найдено» не значит «нет».

## По темам

### Профили клиента (ICP)
- [icp_profiles_2026-11-09_ru.md](icp_profiles_2026-11-09_ru.md) — повторный конкурентный анализ по девяти профилям клиента: диаспора, гастроли, мастер-классы, ассоциации, театры, площадки, дегустации с магазином, фестивали, дешёвые массовые события. Цена «всего» для типичного билета каждого профиля и что докрутить в продукте (09.11.2026).

### Цены и тарифы платформ
- [pricing_2026-10/report_ru.md](pricing_2026-10/report_ru.md) — прайсы около 190 билетных платформ, модели direct и сборщики (10.10.2026). Таблица `pricing_2026-10/ticketing_pricing_2026-10-09.xlsx`, исходные данные `pricing_2026-10/raw/`, задание `pricing_2026-10/BRIEF.md`.
- [pricing_2026-10/value_tariff_scenarios_ru.md](pricing_2026-10/value_tariff_scenarios_ru.md) — сценарии тарифов Arena против рынка.
- [platform_price_currency_2026-11-08_ru.md](platform_price_currency_2026-11-08_ru.md) — в какой валюте платформы объявляют свои цены организаторам и подбирают ли её по IP (08.11.2026).
- [currency_display_2026-11-08_ru.md](currency_display_2026-11-08_ru.md) — справочно: валюта цены билета для покупателя у платформ и Stripe Adaptive Pricing.

### Условия, возвраты, юридические страницы
- [legal_pages_structure_2026-10.md](legal_pages_structure_2026-10.md) — структура Terms and Conditions и юридических страниц 34 платформ direct merchant (22.10.2026). Задание, список платформ и скрипты сбора: `legal_pages_2026-10/`.
- [refunds_cancellation_2026-10.md](refunds_cancellation_2026-10.md) — возвраты, сборы при возврате, платные отмены у конкурентов (12.10.2026).

### Функции и продукт
- [../marketing/platform_features_2026-10.md](../marketing/platform_features_2026-10.md) и [../marketing/platform_features_2026-10.html](../marketing/platform_features_2026-10.html) — полный перечень функций Arena с разделами сравнения с Ticket Tailor, Wix Events и другими.
- [other_platforms_features_2026-09-27.md](other_platforms_features_2026-09-27.md) — функции других билетных платформ, что взять в Arena (Н1–Н17).
- [merch_and_shop_to_event_2026-10-22_ru.md](merch_and_shop_to_event_2026-10-22_ru.md) — мерч на мероприятиях и магазин, который зовёт на событие.

### Перепродавцы и дистрибуция
- [reseller_integration_scenarios_2026-11-09_ru.md](reseller_integration_scenarios_2026-11-09_ru.md) — перепродавцы билетов (FridayTicket, Kontramarka и др.) как партнёры: 17 сценариев интеграции, что уже есть в Arena, риски, первые шаги (09.11.2026).

### Продажи в соцсетях и мессенджерах
- [social_messenger_selling_2026-10-24_ru.md](social_messenger_selling_2026-10-24_ru.md) — продажи внутри Facebook, Instagram, Messenger, Telegram, WhatsApp: что есть у конкурентов и что разрешают платформы (24.10.2026). Связанный бэклог: `09_autoforge/social_selling_backlog.md`.

### Отдельные конкуренты
- [fridayticket_2026-11-09_ru.md](fridayticket_2026-11-09_ru.md) — FridayTicket (fridayticket.com): витрина-перепродавец русскоязычных гастролей, организатор скрыт, ретаргетинг в Facebook; как найти организаторов туров (09.11.2026).
- [xtix_2026-11-03_ru.md](xtix_2026-11-03_ru.md) — XTIX (xtix.ai, Кипр, Израиль), конкурент в русскоязычной нише (03.11.2026).
- [tickettailor_analysis_2026-09-04.md](tickettailor_analysis_2026-09-04.md), [tickettailor_win_features_2026-09-27.md](tickettailor_win_features_2026-09-27.md) — Ticket Tailor: разбор и функции, которые стоит повторить.
- [arena_architecture_fit_2026-09-04.md](arena_architecture_fit_2026-09-04.md) — как найденное ложится на архитектуру Arena.

## Как пополнять
Новый отчёт кладётся сюда под именем `<тема>_<дата>_ru.md` и добавляется строкой в этот индекс в нужный раздел. В начале отчёта указываются дата, источники и что не проверено.
