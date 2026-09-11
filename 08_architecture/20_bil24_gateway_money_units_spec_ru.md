# Единицы денег на проводе Bil24-совместимого шлюза — спецификация W1-M

Статус: design authority для мини-волны **W1-M** (AutoForge, фичи #528–#530). Дата: 2026-09-11. Реализовано 2026-09-11, head 767aab4.
Решение владельца: конвенция как у Bil24 — на проводе мажорные единицы валюты, число с ≤ 2 знаками;
в БД bigint в минорных единицах; конвертация на границе шлюза во всех командах, входящих и исходящих.
Исправляет противоречие: спека W1 (`18_…` строки 14–15, 482) и `BEHAVIOR_DIFFERENCES.md` §8 требуют
мажорные единицы, а код `hbil24` отдаёт `price_amount`/`orders.total` как есть (минорные) и запись в
`AGENTS.md` («шлюз НЕ конвертирует деньги на проводе») закрепила это как норму.

## 1. Доказательства

- Реальные заказы Bil24 (`tests/compat/bil24/testdata/wp/bil24_orders_pseudonymized.json`):
  `totalSum: 18.9`, `totalSum: 119.0`, `totalPrice: 23.8`, `charge: 0.9` — дробные мажорные значения.
- PHP сайтов: `bil24-acf-sync.php:944` пишет `price` из `GET_ALL_ACTIONS` напрямую в `_regular_price`
  WooCommerce; `class-bil24-orders.php:974–985, 1204–1226` берёт `totalSum` как сумму заказа WC и шлёт
  тот же float в `PAY_ORDER.amount`. WooCommerce работает в валюте сайта (CZK, ILS), не в центах.
- Импорт (`bil24compat/import_wire.go` `PriceMinorUnits`) и `REFUND_TICKET.refundPrice` уже считают
  провод мажорным и умножают на 100; `bil24wire/encode.go` `major()` делит на 100 для вебхуков. Итог
  сегодня: пакет с `price: 450` → БД 45000 → `GET_ALL_ACTIONS` отдаёт 45000 (тесты #527 и
  `scenario08_import_test.go` закрепили это как «известный разрыв»).

## 2. Нормативное правило

1. **Провод** (все `/compat/bil24/*` команды, вебхуки `bil24_wp`, `order.paid`/`ticket.refunded`
   в MACS): деньги — JSON `number` в мажорных единицах валюты сеанса, округление до 2 знаков,
   без строк и без хвостовых нулей (`525`, `5.25`, `18.9`).
2. **БД и домен**: bigint минорные единицы, без изменений. Все расчёты (сбор канала `fee_percent`,
   скидки промокодов, суммы корзины) выполняются в минорных единицах и конвертируются один раз при
   кодировании ответа.
3. **Единая точка конвертации**: пакет `internal/adapters/bil24compat/money` (новый) с
   `Major(minor int64) float64` (`float64(minor)/100`, затем `math.Round(x*100)/100`) и
   `Minor(major float64) int64` (`int64(math.Round(major*100))`, half away from zero). `bil24wire.major`
   и `bil24compat.PriceMinorUnits`/`refundPriceMinorUnits` переводятся на этот пакет (поведение
   не меняется). Никаких локальных `/100` и `*100` в `hbil24` — статический тест
   `tests/staticanalysis` запрещает литералы `/ 100` и `* 100` в `hbil24/*.go` и `macs/*.go` вне
   пакета `money` (в стиле существующих гардрейлов).
4. **Входящие деньги** (`PAY_ORDER.amount`, `REFUND_TICKET.refundPrice`, `categoryList[].price`
   импорта): `Minor(x)` до любого сравнения или записи. `PAY_ORDER`: сравнение `Minor(amount)` с
   `orders.total` с допуском **1 минорная единица** (заменяет `payAmountToleranceMajor=0.01` в
   смешанных единицах); `amount_mismatch` в аудите/`order_events` пишет обе суммы в минорных.
5. Валюты без дробной части (ISK, JPY, HUF — экспонента 0 по ISO-4217) в волне 1 не поддерживаются
   каналами (CZK, ILS, EUR — экспонента 2); фиксированный масштаб 100 допустим, но `money` должен
   принимать масштаб параметром (`ScaleFor(currency)` с таблицей `{JPY:1, ISK:1, HUF:1, …}` и
   дефолтом 100), чтобы расширение не требовало переписывания вызовов.

## 3. Места, которые меняются (инвентарь 2026-09-11)

| Команда / поверхность | Поля | Файл | Сейчас | Должно быть |
|---|---|---|---|---|
| GET_ALL_ACTIONS (action) | `minPrice`, `maxPrice` | `hbil24/cmd_catalog.go:273-274` | минорные | `Major` |
| GET_ALL_ACTIONS (session) | `minPrice` | `hbil24/cmd_catalog_events.go:227` | минорные | `Major` |
| GET_ALL_ACTIONS (category) | `price` | `hbil24/cmd_catalog_events.go:296` | минорные | `Major` |
| GET_SEAT_LIST | `categoryList[].price`, `seatList[].price` | `hbil24/cmd_seat_list.go:308, 482` | минорные как float64 | `Major` |
| GET_CART | `seatList[].price`, `sum`, `discountAmount`, `chargeAmount`, `totalSum` | `hbil24/cmd_cart_get.go:116, 147-150` | минорные; сбор считается в минорных | расчёт в минорных, `Major` при кодировании |
| RESERVATION (корзина) | `seatList[].price`, `sum`, `discount`, `charge`, `totalSum` | `hbil24/cmd_cart_view.go:436, 455-458` | минорные | `Major` |
| RESERVATION (pricing) | `sum`, `discount`, `charge`, `totalSum` | `hbil24/cmd_cart.go:344-347` (`bil24FinancialFields`) | минорные | `Major` |
| CREATE_ORDER_EXT | `sum`, `discount`, `charge`, `totalSum` | `hbil24/cmd_order_create.go:657-660` | минорные | `Major` |
| GET_ORDER_INFO (legacy `checkout_sessions`) | те же | `hbil24/cmd_order.go:240-243` | минорные | `Major` |
| GET_ORDER_INFO (по строке `orders`) | те же | `hbil24/cmd_order.go:371-374` | минорные | `Major` |
| GET_ORDER_INFO (проекция #505) | `order.*` | `hbil24/cmd_order_wire.go:48` → `bil24wire` | мажорные | без изменений; **одна команда — одна конвенция**: legacy-ветки обязаны совпадать |
| PAY_ORDER (вход) | `amount` | `hbil24/cmd_order_pay.go` (`payRecordAmountMismatch`) | сравнение без конвертации | `Minor(amount)` vs `orders.total`, допуск 1 |
| REFUND_TICKET (вход) | `refundPrice` | `hbil24/cmd_refund.go:118, 254-258` | ×100 локально | через `money.Minor` |
| Вебхуки `bil24_wp` | `sum`, `discount`, `charge`, `totalSum`, `filtered*`, билет `price`, `discount`, `charge`, `totalPrice`, `refundPrice` | `bil24wire/encode.go:154-159, 188, 211-221` | мажорные | без изменений, `major()` → `money.Major` |
| MACS `order.paid`/`ticket.refunded` | `Order.{Sum,Discount,Charge,TotalSum}`, `Ticket.{Price,Discount,Charge,TotalPrice,RefundPrice}` | `macs/export.go:45-48, 73-83, 158-162, 199-203, 228` | минорные int64 | мажорные `number` — MACS принимает тот же формат, что и от настоящего Bil24 (§4) |
| Импорт `bil24-session`/`event-bundle` | `categoryList[].price` | `bil24compat/import_wire.go:128-140` | ×100 локально | через `money.Minor`; `GET_ALL_ACTIONS` после импорта `price: 450` → `450` |

Не меняются: `GET_TICKETS_BY_ORDER` (цен нет), `SCAN_TICKET`, `ADD_PROMO_CODES`/`CHECK_KDP` (скидка
внутренняя, видна через `GET_CART`).

## 4. MACS

MACS (`macs.arenasoldout.com`, `/api/_wh/tickets`) принимает вебхуки в формате Bil24 и от настоящего
Bil24 (мажорные), и от arena (сейчас минорные). Один приёмник — одна конвенция: arena переходит на
мажорные. Типы `macs.Order`/`macs.Ticket` меняют деньги на `float64` с тегами без изменений; комментарии
«minor units» и `17_macs_integration_contract.md` (строки 74, 88) обновляются. Стаб MACS в тестах
проверяет, что `totalSum` ≤ 2 знаков и совпадает с `bil24wire`-полем того же заказа.

## 5. Тесты и фикстуры

- Голден-фикстуры харнесса (`tests/compat/bil24/testdata/wp/golden/**`) сегодня закрепляют
  `sum: 500`, `totalSum: 525`, `minPrice: 900/1250`, `PAY_ORDER.amount: 525` при сидах
  `price_amount = 500/900/1250`. Правило: **голдены остаются как есть** (они уже выглядят как мажорные
  значения), а сиды в `seed_test.go:259, 340` умножаются на 100 (`50000/90000/125000`), так что провод
  после конвертации даёт те же `500`/`525`/`900`/`1250`. Это единственный случай, когда правка сидов,
  а не голденов, — по спеке.
- Новый голден-кейс с дробью: сид `price_amount = 1890` → `price: 18.9`, сбор 5 % → `charge: 0.95`,
  `totalSum: 19.85`; `PAY_ORDER.amount: 19.85` принимается, `19.84` и `19.86` — допуск 1 минорная
  единица, `19.9` → `amount_mismatch`. Проверить сериализацию: `18.9`, не `18.90` и не `18.899999`.
- `event_bundle_527_integration_test.go:195-220` и `scenario08_import_test.go:335-365`: убрать
  «известный разрыв», ожидать мажорные значения (бандл: `450`/`900`; scenario08: цена `900` и сумма с 5 %
  сбором `945` — реализовано в #528).
- Юнит-тесты `money`: округление half away from zero (`0.005` → 1, `-0.005` → −1), обратимость
  `Major(Minor(x)) == x` для x с ≤ 2 знаками, `ScaleFor`.
- Статический гардрейл на литералы `/ 100`, `* 100` в `hbil24`, `macs`, `bil24wire` вне `money`.
- `BEHAVIOR_DIFFERENCES.md` §8 — оставить, добавить ссылку на этот документ; `AGENTS.md` — заменить
  запись «шлюз НЕ конвертирует деньги на проводе» на правильную (провод мажорный, БД минорная,
  конвертация только в `bil24compat/money`; PAY_ORDER сравнивает `Minor(amount)` с допуском 1).

## 6. Разбиение на фичи AutoForge (W1-M, #528–#530)

| # | Код | Суть | Сложность |
|---|---|---|---|
| 528 | W1-M1 [MAJOR] | Пакет `money`, все 13 мест выдачи `hbil24` + `PAY_ORDER`/`REFUND_TICKET` вход + импорт через `money`; сиды ×100, голдены без изменений, дробный голден-кейс; правка тестов #527/scenario08; `AGENTS.md`; статический гардрейл | 3 |
| 529 | W1-M2 | MACS `export.go` на мажорные через `money`, контракт `17_…` и комментарии, стаб-тесты MACS, `bil24wire` на `money` | 2 |
| 530 | W1-M [EPIC-VERIFY] | Полный гейт `-count=1` (unit, integration compat/himports/macs, lint, drift, admin), сквозной тест: event-bundle `450` → `GET_ALL_ACTIONS` `450` → RESERVATION `totalSum` → `PAY_ORDER.amount` тот же float → `order.paid` в wpstub и macs-стаб с тем же `totalSum`; прогресс-нота с командами и кодами выхода | 2 |

Зависимости: 529 → 528; 530 → 529.
