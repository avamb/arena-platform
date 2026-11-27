# 68. Воронка событий Meta и GA4 по списку таргетолога (v2)

Статус: задание, 27.11.2026. Основа: ответ таргетолога на наши вопросы и сверка с кодом `origin/master` 02a9694d.
Связано: спека 65 (встроенная оплата Stripe и события), спека 66 (приложение Meta).

## 1. Что хочет таргетолог

| Событие Meta | GA4 | Когда | Параметры |
|---|---|---|---|
| ViewContent | `view_item` | открыта страница мероприятия (один город или тур — одно событие) | `content_name`, `event_id` |
| TicketsClick (своё) | `tickets_click` | нажата кнопка «Tickets»: переход к выбору билетов или в стороннюю кассу | `content_name`, `event_id` |
| AddToCart | `add_to_cart` | выбрано место на схеме или количество — в корзине появилась сумма | `content_name`, `value`, `currency`, `event_id` |
| InitiateCheckout | `begin_checkout` | нажата кнопка перехода к оформлению после выбора билетов | `content_name`, `value`, `currency`, `event_id` |
| AddPaymentInfo | `add_payment_info` | данные карты введены полностью (не нажатие «Pay») | `content_name`, `value`, `currency`, `event_id` |
| Purchase | `purchase` | оплата подтверждена | `content_name`, `value`, `currency`, `event_id` из ID заказа |

Плюс от владельца (27.11): на шаге оплаты обязательная галочка согласия с условиями продажи, и её отметка — отдельное событие AcceptTerms (`accept_terms`).

`content_name` = «Название мероприятия — Город»; для тура, где город ещё не выбран, — только название.

## 2. Что есть сейчас (master 02a9694d)

Все события отправляет сервер (Conversions API Meta, Measurement Protocol GA4), только при явном согласии покупателя на cookies (`buyer_context.tracking_consent=true`).

| Событие | Когда сейчас | Где в коде |
|---|---|---|
| AddToCart `atc:<cart>` | открыта корзина («Continue to payment») — запрос `checkout/quote` | `hfeed/public_feed_quote.go` `queueQuoteAddToCart`; виджет `ArenaTickets.svelte` (~537, `postTrackedQuote`) |
| InitiateCheckout `ic:<checkout_session>` | введён e-mail и нажат «Pay» (buyer attach); в классическом потоке — `checkout/start` | `hfeed/checkout_buyer_track.go`, `checkout_buyer_attach.go`, `public_feed_checkout.go` ~1817 |
| Purchase, `event_id` = ID заказа | оплата подтверждена | `conversions/senders.go` `MetaSender.Send` |
| ViewContent | только статические сайты организаторов (`POST /v1/public/feeds/{token}/track`, ключ сайта); без `content_name` | `hsite/track.go`, `senders.go` `SendEvent` |
| TicketsClick | нет; клик в стороннюю кассу со статического сайта идёт как InitiateCheckout | `hsite/track.go` (`outbound_ticket_click`) |
| AddPaymentInfo, AcceptTerms | нет | — |

Галочка согласия с условиями уже есть (P2): `settings.hosted_page.consent_required` канала, по умолчанию выключена, показывается только при заполненном профиле продавца; отметка записывается в `checkout_consents`. Виджет: `lib/consent.ts`, `components/BuyerForm.svelte`.

`content_name` сейчас «Название — Город, ГГГГ-ММ-ДД», дата в UTC (`senders.go` `contentName`).

Страница мероприятия tickets.arenasoldout.com: окно согласия на cookies (`tickets-page/src/lib/tracking.ts`, localStorage `arena_tracking_consent`), анонимная статистика (`/v1/public/pages/{org}/{event}/track`, `/v1/public/funnel`) — в Meta ничего не уходит.

## 3. Что сделать

### 3.1 Сдвиг AddToCart и InitiateCheckout на шаг раньше (виджет + hfeed)
- **AddToCart** — когда в выборе впервые появилась сумма (место на схеме или количество > 0) и выбор не менялся 1,5 с. Одно событие на корзину (`atc:<add_to_cart_id>`); повторно только если покупатель очистил корзину и начал заново (новый id). Не по событию на каждый клик по месту: перебор мест уже передаётся в `seats_considered`. `value` — сумма корзины в этот момент.
- **InitiateCheckout** — по нажатию «Continue to payment» (сейчас это момент AddToCart). `event_id` = `ic:<add_to_cart_id>`, `value` — итог корзины с сервисным сбором.
- Убрать InitiateCheckout из buyer attach и из `checkout/start`, чтобы событие не было двойным.

### 3.2 AddPaymentInfo (виджет + новый маршрут)
- Срабатывает по `change` у Stripe Payment Element с `complete: true` (`lib/paymentController.ts` ~628), один раз на checkout. Данные карты никуда не передаём, только сигнал.
- Новый маршрут `POST /v1/public/checkout/{token}/payment-info` (без тела или пустое тело): сервер ставит в очередь `add_payment_info` с `event_id` = `api:<checkout_session>`, согласие и контекст браузера берёт из сохранённого контекста checkout, как `queueAttachedInitiateCheckout`. Повтор — no-op.
- В классическом потоке (переход на страницу Stripe) события нет: мы не видим ввода карты.

### 3.3 Галочка условий и AcceptTerms
- Нужно решение владельца (см. §5): включить галочку для всех платных продаж по умолчанию.
- Событие **AcceptTerms** (своё, GA4 `accept_terms`) ставит в очередь сервер, когда записывает `checkout_consents` (buyer attach / `checkout/start` с `consent.accepted`). `event_id` = `terms:<checkout_session>`.

### 3.4 ViewContent и TicketsClick со страницы мероприятия
- Страница tickets.arenasoldout.com при согласии на cookies шлёт ViewContent при открытии страницы мероприятия (один раз на просмотр; тур — одно событие на страницу, без города, если город не выбран) и TicketsClick по нажатию кнопки перехода к билетам или в стороннюю кассу.
- Маршрут: расширить `/v1/public/pages/{org}/{event}/track` полем `buyer_context` и видами `view_content` / `tickets_click` либо отдельный маршрут — на усмотрение исполнителя, но: без согласия ничего не ставится в очередь и ничего личного не хранится; анонимная статистика страницы работает как раньше; защита от накрутки — существующие лимиты публичных маршрутов.
- `event_id` генерирует страница (случайный, на каждый просмотр/клик).
- Виджет, встроенный в чужой сайт, ViewContent не шлёт: у организатора свой пиксель, будет задвоение.
- Статические сайты (`hsite/track.go`): `outbound_ticket_click` → TicketsClick вместо InitiateCheckout; `checkout_open` остаётся InitiateCheckout.

### 3.5 Параметры
- `content_name` = «Название — Город» без даты; дата сеанса отдельным параметром `session_date` (ГГГГ-ММ-ДД в часовом поясе площадки, не UTC). Остальные параметры (`content_ids`, `contents`, `num_items`, `city`, `venue`, `event_name`) не трогаем.
- `value`/`currency` у AddToCart, InitiateCheckout, AddPaymentInfo, Purchase.
- Новые виды в `conversions/event.go`, имена в `metaEventNames`/`ga4EventNames`: `tickets_click` → `TicketsClick`/`tickets_click`, `add_payment_info` → `AddPaymentInfo`/`add_payment_info`, `accept_terms` → `AcceptTerms`/`accept_terms`.
- Миграция: расширить CHECK `conversion_deliveries_kind_chk` (образец — 0225). Номер — следующий свободный на момент работы (0227 занят открытым PR #471); обновить пин head в тестах.

## 4. Проверка (DONE WHEN)
- Юнит-тесты: payload каждого нового вида (Meta и GA4), `content_name` без даты, `session_date` в поясе площадки; виджет — момент AddToCart (дебаунс), InitiateCheckout на «Continue to payment», AddPaymentInfo один раз на `complete`.
- Интеграционный тест (тег `integration`): без согласия ни одного ряда в `conversion_deliveries`; с согласием — по одному ряду каждого вида, повтор AddPaymentInfo не создаёт второго.
- `go build ./...`, затронутые пакеты, `npm run type-check` и Vitest виджета и страницы — зелёные; OpenAPI и TS-клиент перегенерированы.
- На стенде (ABH TEAM, канал тестовый): одна покупка при открытой вкладке «Тестирование событий» → ViewContent, TicketsClick, AddToCart, InitiateCheckout, AcceptTerms, AddPaymentInfo, Purchase с тестовым кодом; при отказе от cookies — ни одного.

## 5. Решения владельца
1. **Принято 27.11:** галочка условий обязательна для ВСЕХ платных продаж (все каналы, новые и существующие), переключатель `consent_required` больше не нужен для включения. Профиль продавца без ссылки на условия: галочка показывается с именем продавца и политикой платформы; организатору — напоминание в ивент-центре заполнить условия. Продажу неполный профиль не блокирует. Бесплатный заказ (итог 0) галочку не требует.
2. Виджет на чужом сайте ViewContent не шлёт (§3.4) — по рекомендации, владелец явно не подтверждал; если таргетолог попросит — включается отдельной настройкой канала.
