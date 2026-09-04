# Ticket Tailor — конкурентная разведка и сравнение с arena

Дата: 2026-09-04. Источник: живой аккаунт Lampyris Events (app.tickettailor.com) под логином
владельца, публичная витрина tickets.lampyrisevents.com, публичный чекаут до шага «Details».
Инвентарь arena — по openapi.yaml, миграциям 0001–0089, apps/admin-web, apps/widget,
docs/vinoandco_gap_analysis_2026-08-13.md и бэклогам 09_autoforge.

Не просмотрено: Broadcasts (требует Stripe Identity-верификации аккаунта), Account settings
(требует 5-значный код из письма), шаг «Payment» чекаута (нужно вводить персональные данные),
редактор схемы зала (функция не активирована на аккаунте, платная).

---

## 1. Резюме

Ticket Tailor (TT) — self-service SaaS «бокс-офис для организатора»: один тенант = один
box office, оплата per-ticket кредитами (€0.70 PAYG, скидка при предоплате), деньги идут
напрямую организатору через его Stripe/PayPal/Square. Продукт заточен на одного организатора
с несколькими событиями, без сетей операторов, без агентских квот и без интеграции с внешними
сканер-системами.

arena сильнее TT в инфраструктурном слое (мультитенантность и сети операторов, RBAC на ~115
прав, схемы залов с версионированием, seat-level ценовые категории, отложенные цены, MACS/Bil24,
reconciliation, биллинг организаторов, WordPress-плагин, embed-виджет как Web Component).

TT сильнее arena во всём «операционном» слое организатора — там, где организатор работает
руками каждый день:

1. Заказы: поиск по имени/email/ID/коду билета, фильтры по статусам, массовые действия,
   карточка заказа с Resend / Edit / Cancel-refund / Print / Invoice / Notes / «новый заказ этому
   клиенту».
2. Возвраты и отмены: единый экран «Cancel order» с частичным возвратом, удержанием комиссий,
   возвратом на ваучер, письмом покупателю.
3. Письма: глобальный + per-event шаблон подтверждения, merge-теги, conditional content по типу
   билета, Apple Wallet, редактируемый reply-to.
4. Маркетинг: discount codes, voucher codes (батчи, экспорт, refund-ваучеры), referral tags,
   tracking links, QR, пиксели FB/TikTok/GA, cookie consent.
5. Self-serve покупателя: просмотр, повторная отправка, перенос на другую дату, самоотмена
   с политикой удержания.
6. Waitlist, holds (резерв билетов «на руках»), access codes, доступность по времени,
   видимость типа билета в зависимости от продаж другого типа.
7. Add-ons (products/merch), bundles, ticket groups, donations, memberships.
8. Отчёты: revenue / transactions / issued tickets / check-in с фильтрами и печатью, doorlist PDF.

Ниже — карта продукта, домен-за-доменом сравнение и приоритизированный список того, что стоит
переносить в arena.

---

## 2. Карта продукта Ticket Tailor (что есть на экране)

| Раздел | Подразделы / функции |
|---|---|
| Overview | Getting-started чеклист, последние заказы, продажи по событиям (issued/remaining/revenue), Climate impact |
| Events | Upcoming / Past, фильтр Name/Venue/Status, статус Published/Draft/Sales closed, Add new event, Copy event details from… |
| Event → Summary | Issued, Revenue, Days to go, Checked-in, апселлы (Memberships, Products, White Label, Gift vouchers), Ticket sales по типам |
| Event → Reports | Issued tickets report (фильтры: даты, день недели, время, ticket type, цена; «break out bundles»; печать), Check-in report |
| Event → Issued tickets | Поиск по имени/email/order/code, фильтр по типу, дате, Valid/Void; вкладка Holds; Import tickets (CSV); Export doorlist (PDF/CSV, группировка, сортировка, buyer questions) |
| Event → Waitlist | Sign-ups All/New/Notified, Create Broadcast, Export |
| Event → Email broadcasts | Рассылка держателям билетов (за верификацией) |
| Event → Check-in app | iOS/Android приложение, отдельные пользователи с логином |
| Event → Edit | 5 секций: Event info, Tickets, Add-ons and extras, Event page, Event settings (см. §3) |
| Event → Event confirmation | Global / event-specific письмо; merge-теги; conditional content по типу билета; Apple Wallet |
| Event → Checkout form | Global / event-specific форма; buyer questions (per order) и attendee questions (per ticket) |
| Event → Actions | Share event (link, custom domain, tracking links, QR, embed, соцсети), Duplicate, Delete |
| Orders | Поиск, фильтры (event, даты, статус incl. incomplete/partially refunded), bulk Export / Cancel-refund / Delete personal data, Add new order (box-office режим виджета) |
| Order card | Resend, Edit details, Cancel/refund, Print confirmation, Download invoice, Stripe tx link, Email delivered status, Notes, Create new order for this customer |
| Sales reports | Revenue (фильтры по валюте/датам/статусу/событию; разбивка ticket sales / booking fees / transaction charges / tax), Transactions по способу оплаты |
| Promote | Links & referrals (referral tag, QR), Website embed code (listing widget с фильтрами/лейаутом, event widget, WordPress shortcode), Discount codes, Voucher codes (promo / gift / refund batches) |
| Products | Manage (аналитика), Sold, Store settings (отдельная витрина товаров), Add product (цена, fee, картинка, quantity) |
| Memberships | Типы (validity, redemptions, renewal reminders, photo upload), Issued, Pending, Redemptions, Broadcasts; скидки «By being a member» |
| Box office settings | Basic (имя, TZ, логотип, about, hide logo, CAPTCHA, order notifications), Design studio (темы, цвета, шрифты, лого, header links, соцсети, listings layout/search/filters), Contact preferences (reply-to), Checkout form, Multi-checkout (beta, корзина на несколько событий), Email templates (order / event / memberships / vouchers), Checkout fees & tax (fixed + %, tax inclusive/exclusive, label), Privacy policy (+генератор), Banned emails, Cookie consent, Self-serve |
| Manage | Payment systems (Stripe, PayPal, Square, offline), Seating charts (платно), Integrations (см. §4.9), Team access (роли), Check-in app users, API (keys + webhooks), Custom domain, White Label (add-on), Billing (кредиты, PAYG, add-ons: White label, HubSpot, ActiveCampaign, Constant Contact) |
| Account | Multiple box offices (несколько тенантов под одним логином), Refer and earn (20 %) |

### Редактор события (самый плотный экран)

- **Event info**: name; dates + timezone; Recurring event (occurrences управляются отдельно);
  Online event (Zoom/Meet/YouTube/Hopin/Vimeo/Skype/Other); venue name/postcode/country;
  rich-text description с embed; event page image (+alt), header image (+alt, zoom), header
  additional images (слайдшоу).
- **Tickets**: Add ticket / Add group / Add bundle / Use seating chart. Ticket type: name,
  quantity (с историей change quantity), price + booking fee с калькулятором «buyer will pay /
  you receive», description, status, access code, min/max per order, способ выдачи, hide
  until/after date, hide when sold out, **hide until another ticket type is running low**, show
  quantity remaining (с порогом), exclude from lowest-price calc, **only available with other
  ticket types** (+ текст ошибки). Donations (title, suggested amount, description). Event
  capacity (общий cap поверх типов), low availability status с порогом и лейблом, collapse
  ticket groups.
- **Add-ons and extras**: products (merch, upgrades) на событие.
- **Event page**: layout, label кнопки, hide map / share buttons, remove branding, event page link.
- **Event settings**: payment method per event, custom transaction fee (% + fixed) и sales tax
  per event, tickets become available/unavailable (+сообщения), show lowest price on calendar,
  hide from listings/search engines, access code на событие, waitlist (show only when sold out,
  CTA, texts), redirect order confirmation page.

### Публичный чекаут

Событие → модальный виджет (iframe) → Tickets (qty +/-, booking fee показан отдельно) →
Details (first/last name, email + repeat email, подпись «Signature: draw/type», per-order и
per-attendee вопросы) → Payment. Cookie banner Accept/Decline. Витрина: header-слайдшоу,
search/date/location/sort, list/grid, кнопка «Manage tickets» (self-serve по email-ссылке).

---

## 3. Сравнение по доменам

Статус arena: BUILT / PARTIAL / MISSING (по инвентарю кода).

| Домен | Ticket Tailor | arena | Вердикт |
|---|---|---|---|
| События: CRUD, статусы, даты, TZ, venue | Есть, один экран-визард | BUILT (events + sessions, venues с гео) | паритет |
| Recurring events / occurrences | Есть (occurrences, reschedule в self-serve) | MISSING (нет rrule) | gap |
| Online events (платформа + ссылка) | Есть | MISSING | gap (низкий приоритет) |
| Медиа события | image + header + доп. слайды, alt-тексты | BUILT (5 постеров, 20 gallery, видео) | arena шире |
| Схемы залов | Add-on, платно, live seat-picker | BUILT (versions, fork, seat categories, GA hybrid) | arena сильнее |
| Ticket types: цена, qty, окно продаж | Есть | BUILT (tiers, price schedule) | паритет |
| Min/max per order | Есть на типе и группе | MISSING | gap |
| Скрытие типа по условиям (дата, sold-out, «другой тип заканчивается») | Есть | MISSING | gap |
| «Только вместе с другим типом» (add-on ticket) | Есть | MISSING | gap |
| Ticket groups / bundles (family pass) | Есть | MISSING | gap |
| Event capacity поверх типов + low-availability бейдж | Есть | PARTIAL (capacity на сессии, бейджа нет) | gap |
| Donations | Есть | PARTIAL (pay-what-you-want tier) | почти паритет |
| Booking fee + transaction fee + tax, per-event override, калькулятор «you receive» | Есть | PARTIAL (глобальные константы, нет UI) | gap |
| Discount codes | %/fixed, face-value vs booking-fee, expiry, redemption limit, membership-based | BUILT (promo codes) | паритет, минус membership |
| Voucher codes (gift / promo batches / refund vouchers) | Есть | MISSING | gap |
| Access code на событие / тип | Есть | MISSING | gap |
| Waitlist | Есть + broadcast + export | MISSING | gap |
| Holds (резерв билетов админом) | Есть | PARTIAL (complimentary + allocations агентам) | другая модель |
| Import tickets (CSV) | Есть | MISSING | gap (низкий) |
| Products / merch / store | Есть + отдельная витрина | MISSING | gap |
| Memberships | Есть | MISSING | gap (низкий для текущих клиентов) |
| Multi-event cart | Beta | MISSING | gap (средний) |
| Чекаут: guest, форма, custom questions per order / per ticket | Есть, signature | PARTIAL (только name/phone флаги) | gap |
| Embed-виджет | iframe-виджет, listing + event, WP shortcode | BUILT (Web Component, WP plugin) | arena лучше технически |
| Публичная витрина организатора (listing page с поиском/фильтрами/дизайном) | Есть + design studio + custom domain | MISSING (только feed + виджет + WP) | gap |
| Orders: поиск, фильтры, bulk | Есть | MISSING (только superadmin list без поиска) | **критический gap** |
| Order card: resend / edit / cancel-refund / notes / invoice / print | Есть | PARTIAL (resend API есть, UI карточки нет) | **критический gap** |
| Refund/cancel flow с частичным возвратом, удержанием комиссий, ваучером | Есть | PARTIAL (API есть, UI read-only, provider refund не подключён — AB-49) | **критический gap** |
| Invoice покупателю (PDF) | Есть | MISSING | gap |
| Delete personal data (GDPR bulk) | Есть | PARTIAL (self-service /me/data-delete) | gap для оператора |
| Email: order confirmation + event confirmation, global/per-event, merge-теги, conditional content | Есть | PARTIAL (Go-шаблоны, не редактируются) | gap |
| Apple Wallet | Есть (integration) | MISSING | gap |
| Reply-to организатора, order notifications на несколько email | Есть | PARTIAL (sender DNS есть, notifications нет) | gap |
| Broadcasts держателям билетов | Есть (за верификацией) | MISSING | gap |
| Self-serve покупателя (view/resend/reschedule/cancel) | Есть с политиками | MISSING (только /recover) | gap |
| Check-in | Своё приложение + пользователи + отчёт | PARTIAL (scan API, MACS как внешний сканер — стратегическое решение AB-50) | по плану |
| Doorlist export PDF/CSV | Есть | PARTIAL (macs-export JSON) | gap |
| Отчёты: revenue/transactions/issued/check-in с фильтрами и печатью | Есть | PARTIAL (event report, reconciliation; /reports placeholder) | gap |
| Экспорт заказов CSV | Есть (bulk) | MISSING | gap |
| Пиксели FB/TikTok/GA, cookie consent, referral tags, tracking links, QR | Есть | MISSING | gap |
| CRM-интеграции (Mailchimp, HubSpot, ActiveCampaign, Constant Contact, Audience Republic, Blackbaud) | Есть | MISSING (Brevo только транзакционно) | gap |
| Ticket resale (Tixel, TicketSwap), GetYourGuide | Есть | MISSING | не приоритет |
| Team roles | 4 роли + гранулярные флаги | BUILT (RBAC 115 прав, сети) | arena сильнее |
| Public API + API keys + webhooks | Есть | PARTIAL (webhooks есть; API-ключей нет, только JWT/feed tokens) | gap |
| Custom domain для витрины | Есть (add-on) | MISSING | gap |
| Banned emails, CAPTCHA toggle | Есть | MISSING | gap (низкий) |
| Multiple box offices под одним логином | Есть | BUILT (organizations, networks) | arena шире |
| Payments | Stripe / PayPal / Square / offline, деньги напрямую организатору | BUILT (Stripe + Connect, AllPay) + биллинг организаторов | паритет, у arena свой биллинг |
| Mobile-first витрина | Да | PARTIAL (mobile waves M-2…M-8 открыты) | gap |

---

## 4. Что переносить в arena — приоритизированный список

Критерий: ценность для реальных клиентов (Vino&Co, Lampyris), стоимость реализации с учётом
того, что уже есть в бэкенде, и отсутствие конфликта со стратегией (MACS как сканер, сети
операторов, схемы залов).

### P0 — закрывает текущие боли клиентов, бэкенд почти готов

1. **Экран заказов организатора** (org-scoped): поиск по имени / email / номеру заказа / коду
   билета, фильтры event / даты / статус (completed, pending, cancelled, refunded, partially
   refunded, incomplete), пагинация. Backend: новый `GET /v1/organizations/{org_id}/orders`
   с full-text по holder_email/имени; UI orders.tsx. Связано с gap doc §5 и AB-22
   (человекочитаемые номера заказов — сделать в той же волне).
2. **Карточка заказа** по образцу TT: состав, платёж (ссылка на Stripe PI), статус доставки
   письма, кнопки Resend (API уже есть), Edit buyer details, Cancel/refund, Notes (новая
   таблица order_notes), «Создать новый заказ этому клиенту».
3. **Cancel/refund flow** одним экраном: «отменить весь заказ» / «вернуть строки», причина
   (уходит покупателю), чекбокс «отправить письмо», сумма — % или фиксированная, «удержать
   booking fee / transaction fee», метод — provider refund или ваучер. Backend: подключить
   provider RefundPayment (AB-49), добавить refund-voucher. Это то, что Vino&Co просит
   напрямую (AB-49/50).
4. **Комиссии и налог как настройка организатора**: booking fee per ticket type (fixed / %),
   transaction fee per order (fixed + %), sales tax inclusive/exclusive с лейблом, per-event
   override. В arena PricingRules уже принимают basis points — нужны таблицы
   `org_fee_settings` / `event_fee_overrides` и UI с калькулятором «покупатель платит /
   вы получаете». Отдельно показывать fee в чекауте, как у TT («+ Ft300 booking fee»).
5. **Экспорт заказов и билетов в CSV/XLSX** + doorlist PDF (группировка по имени/типу,
   сортировка, до 5 buyer-вопросов). У arena есть macs-export JSON — добавить табличные
   форматы на том же датасете.
6. **Min/max per order** на ticket tier и общий cap на событие поверх tiers; бейдж
   «осталось мало» с порогом. Мелко, но клиенты спрашивают первым делом.

### P1 — заметный прирост ценности, средняя сложность

7. **Per-event письма подтверждения** с global fallback, merge-теги ({event-name},
   {event-full-date}, {event-venue-name}, {add-to-calendar-link}) и **conditional content по
   типу билета**. Gap doc §8 уже помечает как «новая фича». Хранить шаблон в БД, рендерить тем
   же Go-рендерером; редактор — rich text в admin-web.
8. **Custom checkout questions**: per-order (buyer) и per-ticket (attendee), типы text /
   select / checkbox / consent, обязательность, глобальный набор + per-event override, вывод в
   doorlist. Виджет уже имеет BuyerForm; расширить схему checkout/start. Widget backlog §7
   помечал out-of-scope — пересмотреть.
9. **Waitlist**: чекбокс на сессии, форма email на виджете при sold-out, список sign-ups
   (new / notified), экспорт, «notify» письмом. Простая таблица `waitlist_signups`.
10. **Access code** на событие и на ticket tier (закрытые продажи, пресейл): проверка в
    checkout/start, скрытие tier'а в feed без кода.
11. **Условная видимость tier'а**: hide until/after date (есть окна продаж), hide when sold
    out, **показать когда другой tier почти распродан** (auto-«Regular after Early Bird»),
    «только вместе с другим tier'ом» (парковка, афтепати). Правила хранить на tier, применять
    в feed и в checkout валидации.
12. **Self-serve покупателя**: страница по magic-link из письма: посмотреть заказ, повторно
    отправить билеты, (позже) перенос и самоотмена по политике организатора. У arena есть
    `/v1/public/checkout/{token}` — расширить до order self-serve.
13. **Уведомления организатору о новом заказе** на список email + reply-to организатора в
    письмах покупателю (sender identity уже есть, нужна настройка адреса ответа).
14. **Аналитика и пиксели**: GA4 / Meta / TikTok ID на уровне организации, cookie consent в
    виджете, UTM/ref-теги в `widget_funnel_events` (таблица уже есть, нужно ловить `ref` и
    utm_* из URL хоста виджета), tracking links и QR в карточке события.
15. **Dashboard организатора** (Overview): последние заказы, продажи по событиям с прогресс-
    баром issued/remaining/revenue, revenue report с разбивкой ticket sales / fees / tax по
    валюте и периоду, transactions по способу оплаты. Сейчас `/reports` — placeholder.
16. **Invoice покупателю** (PDF по заказу, реквизиты организатора уже хранятся — AB-45).
17. **Bulk «удалить персональные данные»** для оператора (у arena есть self-service GDPR,
    нужен админский путь по выбранным заказам).

### P2 — расширение продукта, после стабилизации P0/P1

18. **Discount на booking fee vs face value**, membership-триггер скидки — после появления
    fee-настроек (п.4).
19. **Ticket groups и bundles** (family pass с общей квотой) — комбинированная цена, отдельный
    cap группы.
20. **Products / add-ons** (мерч, апгрейды) в чекауте и отдельная витрина товаров — новая
    сущность с quantity и fee, участвует в order lines и refund.
21. **Voucher codes** (gift cards, батчи для Groupon, refund-vouchers из п.3).
22. **Recurring events / occurrences** с переносом билета на другую дату — у arena есть
    sessions, не хватает генератора occurrences и UI календаря на витрине.
23. **Публичная витрина организатора**: listing page (search / date / venue / sort,
    list / grid / calendar), design studio (тема, цвета, шрифты, лого, header links, соцсети),
    custom domain. Сейчас у arena только виджет + WP; для клиентов без сайта это блокер.
24. **Multi-event cart** (TT beta): корзина на несколько сессий/событий, одно письмо-заказ +
    письма по событиям. У arena reservation уже мульти-item; нужна поддержка нескольких
    сессий в одном checkout.
25. **Apple / Google Wallet** passes.
26. **API keys для организатора** + документация public API, Zapier-подобная интеграция.
27. **CRM-синк**: экспорт покупателей в Brevo-листы (уже есть SDK), затем Mailchimp/HubSpot.
28. **Import tickets (CSV)** для внешних продаж — у arena есть allocations агентам, добавить
    импорт списка держателей для сканирования.
29. **Broadcasts** держателям билетов по событию (с consent-фильтром) — Brevo campaigns.
30. **Memberships** — только если появится клиент с абонементами/клубом.

### Что НЕ копировать

- Свой check-in app: стратегия arena — MACS (AB-50). Вместо этого довести doorlist-экспорт и
  MACS-контракт.
- Кредитную модель биллинга per-ticket: у arena свой биллинг организаторов с тарифами и
  инвойсами; TT-модель имеет смысл только как один из тарифов.
- Climate-impact виджет, refer-and-earn, supplier directory — маркетинг платформы, не продукт.

---

## 5. Где arena уже впереди (не потерять при копировании)

- Мультитенантность с сетями операторов, агентские квоты (allocations), impersonation с
  причиной, аудит.
- Схемы залов: версии, fork, seat categories, hybrid GA, versioned seat-status для виджета.
  У TT это платный add-on без такой глубины.
- Отложенные цены (price schedule) и трёхуровневый каскад цен.
- Reconciliation с line-level review; биллинг организаторов с инвойсами через Stripe.
- Виджет как Web Component с Shadow DOM и CI-гейтом на размер, RTL, 4 локали; WP-плагин с
  CPT-синком.
- Интеграции с внешними сканер-системами (MACS, Bil24) — у TT только своё приложение.
- Sender DNS verification для писем организатора.

---

## 6. Заметки по ходу обхода

- Ticket Tailor прислал на videokontrol@gmail.com письмо с 5-значным кодом: открытие
  «Account settings» требует верификации. Код никуда не вводился; действие безобидное.
- Broadcasts на аккаунте не активированы (нужна верификация через Stripe Identity) — экран
  рассылок не снят.
- В публичном чекауте был добавлен 1 билет Early Bird в корзину и открыт шаг «Details»;
  данные не вводились, оплата не начиналась. Резерв снимается по таймауту TT.
- Черновик события не создавался: после захода на «Add new event» список событий остался
  «Upcoming (1)».
- Скриншоты в файл на диск расширение не сохраняет; все снимки остались в чате этой сессии.
- Аккаунт: Stripe подключён, FB pixel активирован, custom domain tickets.lampyrisevents.com
  активен, 4 trial-кредита, тимейт Dina Malygina (Event manager + Order manager + Overview).
