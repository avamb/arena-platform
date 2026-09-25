# Целевая схема облачных серверов — рекомендация (раскладка по клиентам)

Дата: 17.09.2026, ред. 3 (после требования владельца об изоляции по клиентам).
Основано на `infra_audit_2026-09-17_ru.md` (факты) и `infra_px62_consolidation_options_2026-09-17_ru.md` (PX62 — отменён).
Статус: **рекомендация, изменения не выполнялись.**

## 1. Рамки и новые вводные

- PX62/Bil24 в схеме не участвует; данные владелец выгружает сам; перенос продаж на Arena — отдельная задача.
- Legacy-серверы **не rescale-ить**: lead-parser CPX31 (≈ €17.49; новый CPX32 — €35.49) и arena-macs CX22 (≈ €4.49).
- Цены новых заказов после 15.06.2026: CX23 €5.49 (2/4/40), CX33 €8.49 (4/8/80), CX43 ≈ €16 (8/16/160, уточнить), CPX22 €19.49, CPX32 €35.49. Наличие CX-линейки проверить в консоли; запасной путь для WP-worker'ов — ARM CAX21/CAX31.
- **Apps-worker обязан быть x86**: `ghcr.io/avamb/arena-api` собран только под amd64; кастомные WP-образы Dokploy собирает на самом worker'е, поэтому WP-worker'ы могут быть ARM.
- **Требование изоляции по контрактам (владелец, 17.09):**
  - Vino&Co (vinoandco.events + staging) **вместе с einat.tours и myethiopia.tours** — отдельный сервер;
  - Lampyris (prod + staging) — отдельный сервер;
  - Minimaldeco — отдельный сервер;
  - остальные сайты — собственные; iltabia.com — клиентский отель с малой нагрузкой; arenasoldout.com — собственный, будет работать с платформой Arena (тесты, возможно продажи для других клиентов).
- **Источник «ещё 8 WordPress»**: cPanel-хостинг iphoster (`cpplx.iphoster.net`, WP Toolkit), 8 установок: abhteam.com, arenasoldout.com, bakanovaartists.com, mbakanova.com, einat.tours, iltabia.com, myethiopia.tours, worldballetclass.com. На всех висят непоставленные обновления, у большинства — предупреждение SSL/TLS, у arenasoldout.com и myethiopia.tours — «1 проблема». **arenasoldout.com на cPanel — вероятно, устаревшая копия**: DNS ведёт через Cloudflare на lead-parser, где живой сайт с БД 751 MB и Updraft. Подтвердить и после подтверждения удалить копию на cPanel (старый WP без обновлений = лишняя поверхность атаки).
- Фактическое потребление сегодня: все WP + MySQL ≈ 7.5 GB суммарно, apps ≈ 2.5 GB, Dokploy ≈ 0.8 GB. Лёгкий WP при общем MariaDB — 150–300 MB RAM и 1–3 GB диска.

## 2. Рекомендуемая раскладка

| # | Сервер | Тип / цена | Контур | Что размещается | Оценка RAM |
|---|---|---|---|---|---|
| 1 | arena-macs (есть) | CX22 legacy, €4.49 | **Control plane** | Dokploy UI/API + его Postgres (бэкап наружу), Uptime Kuma + Beszel. Никаких нагрузок | ~1.3 GB |
| 2 | lead-parser (есть) | CPX31 legacy, €17.49 | **Apps + n8n (x86)** | arena-backend (api/worker/pg/redis), arena-admin, MACS (backend/frontend/MongoDB), n8n на Postgres + prune, mindsethappybot, pr-top + umami | ~3 GB |
| 3 | **новый** | CX33 €8.49 (или CAX21) | **Клиент: Vino&Co + tours** | vinoandco.events (prod), staging.vinoandco.events, einat.tours, myethiopia.tours. Общий MariaDB (отдельные БД/пользователи), Redis, лимиты | ~3.5 GB |
| 4 | lampyrisevents (есть) | CX33, €8.49 | **Клиент: Lampyris** | lampyrisevents.com + staging.lampyrisevents.com (уже здесь). Убрать чужой vinoandco-staging, завести staging в Dokploy, общий MariaDB | ~2.5 GB |
| 5 | minimaldeco (есть) | CPX22, €19.49 | **Клиент: Minimaldeco** | minimaldeco.es (уже здесь). Ничего не переносить. **Опция экономии** позже: замена на CX33 €8.49 (−€11/мес, вдвое больше ресурсов) — это переезд магазина, отдельное окно | ~1.5 GB |
| 6 | **новый** | CX33 €8.49 (или CAX21) | **Собственные сайты + iltabia** | arenasoldout.com, marinabakanova.com, mbakanova.com, bakanovaartists.com, abhteam.com, worldballetclass.com, ndarchdesign, **iltabia.com** (клиентский отель, малая нагрузка — см. примечание). Общий MariaDB, Redis, лимиты | ~3–4 GB |
| 7 | Бэкапы | Storage Box BX11 ≈ €3.81 или Object Storage ≈ €5 | offsite | Dokploy Backups всех БД + restic томов + `/etc/dokploy` | — |
| — | cPanel iphoster | ? €/мес (уточнить) | **отменить** после переноса 7 сайтов и удаления копии arenasoldout.com | — |
| | **Итого** | **≈ €71–72/мес** сейчас; **≈ €60/мес** после замены minimaldeco на CX33; минус стоимость cPanel-хостинга | 6 серверов, 16 WP-сайтов (13 prod + 2 staging + 1 витрина) | |

Примечания к раскладке:
- **iltabia.com.** По букве принципа «клиент = сервер» ему положен свой CX23 (€5.49). Для отеля с малой нагрузкой и без продаж это избыточно — предлагаю разместить на #6 с лимитами, а в договоре/описании услуги зафиксировать «изолированный стек, общий сервер». Если контракт требует буквальной изоляции — добавить CX23.
- **arenasoldout.com.** Это WP, поэтому место ему на #6, а не на Apps-worker'е: он ходит в `api.arenasoldout.com` по сети, как и любой внешний клиент платформы, что делает его честным тестом интеграции. Если начнутся реальные продажи для других клиентов — вынести на отдельный CX33 как «heavy» (как у Vino/Lampyris).
- **Vino&Co + tours (#3).** Vino — самый тяжёлый магазин (WP 1 GB + MySQL 0.5 GB под нагрузкой, сегодня его MySQL убивается OOM на lead-parser). На CX33 с двумя лёгкими tours-сайтами и staging укладывается в ~3.5 GB; staging держать с лимитом 768 MB и останавливать вне работ.
- **Клиентские серверы получают одинаковый шаблон**: Traefik, общий MariaDB 11 LTS (`mem_limit` 2 GB, buffer pool 1 GB), Redis, стек на сайт с `mem_limit` 512 MB (магазины — 1–1.5 GB), `logging` 20 MB×3, host-cron для wp-cron, Cloudflare спереди. Это и есть «понятный зоопарк»: новый сайт = новый compose из шаблона на нужном worker'е.
- **Перенос с cPanel**: полный бэкап cPanel или экспорт All-in-One WP Migration/UpdraftPlus → импорт в Docker-стек; обновить WP/плагины уже на новом месте; DNS через Cloudflare (если зоны ещё не в CF — завести заранее, это же даст кэш и WAF).

## 3. Что это даёт против сегодняшнего состояния

| | Сегодня | Цель |
|---|---|---|
| Серверы / €/мес | 4 (+cPanel) / ≈ €50 (+cPanel) | 6 / ≈ €60–72 |
| Сайтов | 7 в Docker + 8 на cPanel | 16 в Docker, единый шаблон |
| Control plane | на lead-parser вместе со всем | отдельный CX22 |
| Изоляция клиентов | Vino/Lampyris/Minimaldeco/свои — вперемешку на lead-parser и lampyris-хосте | по контракту: 1 клиент = 1 сервер |
| Бэкапы | Updraft у 4 сайтов, остальное — нет | все БД + тома наружу, restore-тест |
| Мониторинг | нет | Uptime Kuma + Beszel + Telegram |
| OOM/SPOF | 219 OOM/мес на lead-parser | лимиты на каждом контейнере, blast radius = один клиент |

## 4. Порядок работ (каждый шаг — после approval; детали в аудите §11)

1. **Фаза 0 — стабилизация без переездов:** дампы всех БД/томов наружу; Hetzner Cloud Firewall на 4 серверах (закрыть 3000/2377/7946/8080/18088/9876, 80/443 только с Cloudflare); ротация паролей Mongo и Postgres бота; `PasswordAuthentication no`; prune n8n; лимиты MySQL на lead-parser.
2. **Фаза 1 — бэкапы:** BX11/Object Storage, Dokploy Backups + restic, restore-тест на временном CX23.
3. **Фаза 2 — новые хосты:** заказать #3 и #6 (CX33 ×2, проверить наличие; иначе CAX21), hardening, подключить к Dokploy как remote servers, развернуть шаблон (Traefik + MariaDB + Redis).
4. **Фаза 3 — пилот:** ndarchdesign → #6; затем 7 сайтов с cPanel по одному (abhteam, worldballetclass, bakanovaartists, mbakanova, iltabia → #6; einat.tours, myethiopia.tours → #3); подтвердить статус копии arenasoldout.com на cPanel.
5. **Фаза 4 — магазины в окна низкого трафика:** marinabakanova → #6; arenasoldout.com → #6; vinoandco.events (заморозка заказов 15 мин) + staging → #3; lampyris — на месте перевести в общий MariaDB и лимиты, staging завести в Dokploy; убрать vinoandco-staging с #4.
6. **Фаза 5 — apps:** MACS → lead-parser (по календарю событий), n8n → Postgres.
7. **Фаза 6 — control plane** на arena-macs (очистка диска, перезагрузка, удаление двух неизвестных root-ключей), перерегистрация worker'ов, остановка старого Dokploy на lead-parser.
8. **Фаза 7 — decommission:** cPanel iphoster, мёртвые стеки/DNS/тома на lead-parser, `/home/dev`; опционально minimaldeco → CX33 и переустановка ОС lead-parser без rescale.

Cutover каждого сайта — смена origin-IP в Cloudflare, откат тем же способом; старые контейнеры не удаляются до конца фазы 7.

## 5. Что нужно от владельца до старта

1. Консоль Hetzner: наличие CX33 (×2) и цена CX43; счёт за minimaldeco (€19.49?).
2. cPanel iphoster: сколько стоит, когда продление, есть ли SSH/доступ к полным бэкапам; живой ли arenasoldout.com там или это копия.
3. Зоны Cloudflare: заведены ли einat.tours, myethiopia.tours, iltabia.com, abhteam.com, bakanovaartists.com, mbakanova.com, worldballetclass.com (сейчас проверены только зоны из аудита).
4. iltabia.com — допустим ли общий сервер с вашими сайтами или нужен отдельный CX23.
5. Ключи: два безымянных root-ключа на arena-macs удалить? `ava@geekom` на lead-parser — ваш?
6. Бэкап-хранилище: BX11 (SFTP/restic) или Object Storage (S3, нативно для Dokploy).
7. Календарь событий MACS.
8. Судьба мёртвых стеков (Supabase, Postiz, LightRAG, Crawl4AI, TgTicketAgent, dev-бот) и pr-top.
9. Approval на фазу 0.
