# Библиотека схем залов — таблица соответствия «файл → площадка»

Дата: 21.09.2026. Статус: **черновик для проверки владельцем, в Arena ничего не загружено.**

Замысел (решение владельца 21.09.2026): схемы залов лежат в служебной организации **abhteam**
(организация №1) как общие шаблоны (`seating_plans.visibility = public_template`); организатор
копирует нужную себе (`POST /v1/seating-plans/{id}/fork`, с указанием своей площадки). Связанная
отложенная спецификация — `08_architecture/24_shared_venues_spec_ru.md` (бэклог B-11).

Источники схем:
- **A** — исходники владельца `OneDrive\AA_Actual_c\BIL24 Europe\Venues_svg` (формат Inkscape; 24 из 26
  читаются импортёром Arena `seating.ImportSVG` как есть);
- **B** — схемы, снятые из Bil24 21.09.2026 по ещё существующим сеансам
  (`C:\Users\andre\arena-backups\bil24-plans\`, формат sbt 1.1). Прошедшие сеансы Bil24 удаляет, поэтому
  таких площадок всего пять.

Как сопоставлялось: подписи и надписи внутри SVG, число мест и рядов против проданных мест в выгрузке
заказов Bil24, название в справочнике площадок Bil24 (`GET_VENUES`). «ID Bil24» приведён для справки —
в библиотеку площадки заводятся **без** него, чтобы не занять ID у организатора, который позже
импортирует свой сеанс из Bil24.

## 1. Исходники владельца (источник A)

| Файл | Мест | Площадка | Город | ID Bil24 | Часовой пояс | Уверенность | На чём основано |
|---|---|---|---|---|---|---|---|
| `Palac_Akropolis.svg` | 260 | Palác Akropolis — полная рассадка (партер + балкон) | Прага | 10549 | Europe/Prague | высокая | имя файла; совпадает со схемой «2-test event» (260 мест) |
| `Palac_Akropolis_GA.svg` | 90 | Palác Akropolis — балкон с местами + стоячий партер | Прага | 10549 | Europe/Prague | высокая | имя файла; это схема концерта IVO DIMCHEV 09.10.2026 |
| `hybernia_2.svg` | 844 | Divadlo Hybernia | Прага | 7078 | Europe/Prague | высокая | надписи PŘÍZEMÍ / BALKON / LOŽE; продажи Lampyris 2025 |
| `divadlo_broadway.svg` | 789 | Divadlo Broadway | Прага | 7076 (дубль 9515) | Europe/Prague | высокая | имя файла, надписи PŘÍZEMÍ / BALKON / JEVIŠTĚ |
| `lucerna.svg` | 453 | Kino Lucerna (большой зал) — **не** Lucerna Music Bar | Прага | 9475 | Europe/Prague | средняя | надписи SCREEN, Side Boxes, BALCONY — это кинозал; проверить |
| `praque_kino_1.svg` | 140 | Městská knihovna v Praze — Malý sál | Прага | 4643 | Europe/Prague | высокая | надпись «Plánek MALÉHO SÁLU»; 14 рядов × 10 = продажи Lampyris 03.11.2024 (ряд до 14, место до 10) |
| `praque_kino_2.svg` | 386 | Městská knihovna v Praze — Velký sál | Прага | 4643 | Europe/Prague | средняя | надпись «Plánek VELKÉHO SÁLU», парный файл к предыдущему; проверить |
| `Brno.svg` | 220 | Cityhouse Brno | Брно | 4644 | Europe/Prague | высокая | 220 мест = 11 рядов × 20, как в продажах Lampyris 04.11.2024 |
| `maly_sal.svg` | 243 | Spa Hotel Thermal — Malý sál | Карловы Вары | 7401 | Europe/Prague | средняя | файл от 21.02.2025, сеанс Romashka 29.04.2025, места до 24–26; проверить |
| `aforo_aribau.svg` | 1178 | Aribau Multicines (Mooby) | Барселона | 9459 | Europe/Madrid | высокая | имя файла; сеанс Somewhere Show 28.09.2025 |
| `valencia_hol.svg` | 44 | Veles e Vents — столы, вариант 1 | Валенсия | 3485 | Europe/Madrid | высокая | «Vip Zone, table N» = сектора в продажах ABH TEAM / Mazart 2024 |
| `valencia_hol_2.svg` | 44 | Veles e Vents — столы, вариант 2 | Валенсия | 3485 | Europe/Madrid | высокая | то же |
| `valencia_hol_3.svg` | 50 | Veles e Vents — столы, вариант 3 (VIP BAR) | Валенсия | 3485 | Europe/Madrid | высокая | то же |
| `La Plantación comb2.svg` | 82 | Hotel Restaurante La Plantación — комбинированная (VIP 1–3, балкон, фан-зона) | Аликанте | 3813 | Europe/Madrid | высокая | имя файла; сеансы ABH TEAM 2024 |
| `La Plantación.svg` | ≈165 | Hotel Restaurante La Plantación — столы (Zone VIP / P / G) | Аликанте | 3813 | Europe/Madrid | высокая | «Zone VIP, Table N» = сектора в продажах. **Не читается**: 2 места нарисованы не кругом |
| `kings_horiz.svg` | 420 | Kings Place, Hall One — горизонтальная раскладка | Лондон | 5260 | Europe/London | высокая | надписи Hall One / Stalls / Balcony; сеанс Cultlab 21.12.2024 |
| `kings_vert.svg` | 420 | Kings Place, Hall One — вертикальная раскладка | Лондон | 5260 | Europe/London | высокая | то же; одна из двух — лишняя, выбрать |
| `merkin_hall.svg` | 445 | Merkin Hall, Kaufman Music Center | Нью-Йорк | 3983 | America/New_York | высокая | имя файла, Orchestra / Balkony; сеанс 14.09.2024 |
| `plaza_club.svg` | 40 | Plaza Club (лаунжи) | Цюрих | 10275 | Europe/Zurich | высокая | имя файла; сектор «Lounge 3» в продажах 16.02.2026 |
| `Stary_Maneż.svg` | 180 | Stary Maneż (ложи Box 1L–9L) | Гданьск | 9239 | Europe/Warsaw | высокая | имя файла = запись справочника Bil24 |
| `Klub2progi.svg` | 32 | Klub 2progi (VIP-столы + стоячий зал) | Познань | 9244 | Europe/Warsaw | высокая | имя файла = запись справочника Bil24 |
| `escape_bar_seats.svg` | 100 | Escape Bar — продажа по местам за столами | Хайфа | 10559 | Asia/Jerusalem | высокая | имя файла, «Стол N»; сеансы Vino&Co |
| `escape_bar_tables.svg` | 32 | Escape Bar — продажа столами целиком | Хайфа | 10559 | Asia/Jerusalem | высокая | то же; совпадает со схемой из Bil24 (32 места) |
| `kuca.svg` | 50 | Kuća Umetnica | Белград | 3486 | Europe/Belgrade | низкая | только по имени файла (единственное «Kuca» в справочнике); 5 рядов × 10; продаж с местами нет — **подтвердить** |
| `montenegro.svg` | 45 | Lazure Marina & Hotel — VIP-места | Херцег-Нови | 1632 | Europe/Podgorica | низкая | только по слову «montenegro» и дате (09.2024, сеанс Cultlab); **подтвердить** |
| `riga_arena2.svg` | ≈5168 | Xiaomi Arēna (Arēna Rīga) | Рига | 5206 | Europe/Riga | высокая | сектора 1xx / 3xx. **Не читается**: 1 место не кругом — взять версию из Bil24 (источник B) |

`escape_bar.pdf` — не схема, в библиотеку не идёт.

## 2. Схемы, снятые из Bil24 (источник B)

| Файл в `bil24-plans\` | Мест | Площадка | Город | ID Bil24 | Часовой пояс | Примечание |
|---|---|---|---|---|---|---|
| `5206_7017424.svg` | 5149 | Xiaomi Arēna (Arēna Rīga) | Рига | 5206 | Europe/Riga | заменяет нечитаемый `riga_arena2.svg` |
| `1595_*.svg` (2 сеанса) | 11948 | Sportovní hala Fortuna (бывш. Tipsport Arena) | Прага | 1595 | Europe/Prague | в папке владельца нет |
| `2859_*.svg` | 432 | Kulturní dům Ládví | Прага | 2859 | Europe/Prague | в папке владельца нет |
| `10549_*.svg` (4 сеанса) | 90 / 260 | Palác Akropolis | Прага | 10549 | Europe/Prague | дублируют исходники владельца — брать исходники |
| `10559_*.svg` (3 сеанса) | 32 | Escape Bar | Хайфа | 10559 | Asia/Jerusalem | дублирует `escape_bar_tables.svg` |

Для источника B нужна небольшая доработка Arena: вход «версия схемы» (`POST …/seating-plans/{id}/versions`)
сегодня понимает только формат Inkscape; формат sbt читается лишь внутри импорта мероприятия. Нужны две
схемы — Fortuna и Ládví (Рига — если не поправить исходник).

## 3. Площадки с продажами по местам, для которых схем нет нигде

По выгрузке заказов Bil24 места продавались на 47 площадках; схемы есть для 15 из них, для
остальных 32 схемы не сохранились ни у
владельца, ни в Bil24 (сеансы прошли и удалены). Самые заметные: Love & Healing Church, Торревьеха
(19 сеансов), Teatro de Yulia Vashchenko, Валенсия (36 сеансов), Santander 10 и Centro Cultural Virgen
del Carmen (Торревьеха), Teatr WAM (Варшава), Zuiderkerk (Амстердам), Divadlo Na Fidlovačce, Atrium
Žižkov, Nová Spirála (Прага), Hunter College (Нью-Йорк), Indigo at The O₂, Marylebone Theatre (Лондон).
Если исходники этих схем есть где-то ещё (почта, другой диск, кабинет Bil24 → редактор залов) —
доложить в `Venues_svg` до отключения Bil24; после отключения их придётся рисовать заново.

## 4. Что нужно от владельца

1. Подтвердить или поправить строки с уверенностью «средняя» и «низкая»: `lucerna.svg`,
   `praque_kino_2.svg`, `maly_sal.svg`, `kuca.svg`, `montenegro.svg`.
2. Выбрать одну из двух раскладок Kings Place (`kings_horiz` / `kings_vert`) или оставить обе.
3. Решить, нужны ли в библиотеке разовые схемы столов (Veles e Vents ×3, La Plantación ×2, Plaza Club,
   Klub 2progi) — или только постоянные залы.
4. Поправить в Inkscape `La Plantación.svg` (2 элемента) и, при желании, `riga_arena2.svg` (1 элемент):
   место должно быть кругом (`<circle>`).
