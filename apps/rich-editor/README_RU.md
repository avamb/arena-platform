# @arena/rich-editor — один редактор текстов для всех интерфейсов

Задача RT-10 спеки 44 (`x_spec44.md`, разделы 3 и 5). Библиотека без React и без других фреймворков на ядре
Tiptap 3 (точная версия в `package.json`, расширения подключены по одному, без StarterKit). Её используют веб-ивент-центр (RT-11), мини-приложение Telegram (RT-12) и WordPress (RT-20).
Редактор отдаёт «текст Arena» (ART) в том виде, который серверный `richtext.Sanitize` принимает без единого
изменения.

## Подключение

### ESM (веб-ивент-центр)

В `apps/event-center/package.json` уже есть `"@arena/rich-editor": "file:../rich-editor"`. Пакет при этом
**должен быть собран**: `npm --prefix apps/rich-editor ci && npm --prefix apps/rich-editor run build`. В образе
event-center (`apps/event-center/Dockerfile`) это делается раньше `npm ci` самого ивент-центра, потому что
из `/app` ссылка `../rich-editor` ведёт в `/rich-editor`.

```ts
import { create } from '@arena/rich-editor'
import '@arena/rich-editor/rich-editor.css'
```

CSS подключает хозяин страницы: редактор ничего не вставляет в страницу сам (`injectCSS: false`).

### IIFE (WordPress, этап 3)

```html
<link rel="stylesheet" href="rich-editor.css">
<script src="arena-rich-editor.iife.js"></script>
<script>
  const editor = ArenaRichEditor.create(document.getElementById('note'), { profile: 'email_note', locale: 'cs' })
</script>
```

Глобальное имя — `ArenaRichEditor`, в нём тот же набор экспортов, что и в ESM.

Обе сборки самодостаточны: Tiptap и ProseMirror внутри, хозяину не нужно ставить их отдельно.

## API

### `create(element, options): RichEditor`

`element` — контейнер, в него редактор кладёт свою разметку (панель, поле, счётчик). Вызов бросает ошибку на
неизвестном профиле.

| Поле `options` | Тип | Смысл |
|---|---|---|
| `profile` | `'description' \| 'email_note' \| 'session_message'` | профиль текста (обязательно), см. таблицу ниже |
| `locale` | `'en' \| 'ru' \| 'cs' \| 'he' \| 'es'` | язык кнопок и сообщений; `ru-RU` читается как `ru`, неизвестный — `en` |
| `value` | `string` | начальный текст: ART или любой HTML (приводится к профилю) |
| `onChange` | `(html, stats) => void` | после каждой правки человеком; `html` — канонический ART; на `setHTML` и начальное значение не вызывается |
| `format` | `({text, profile}) => Promise<{html, ai_applied, reason?, prompt_version?}>` | включает кнопку «Оформить» (см. ниже) |
| `dir` | `'ltr' \| 'rtl'` | направление; по умолчанию `rtl` для `he`, иначе `ltr` |
| `label` / `labelledBy` | `string` | `aria-label` поля или `id` элемента, который его подписывает |
| `disabled` | `boolean` | только чтение: нет ввода, панель, «Оформить» и диалог ссылки выключены, у поля `aria-readonly="true"` |

### Что возвращает `create`

| Метод | Смысл |
|---|---|
| `getHTML()` | канонический ART. Пустой текст — пустая строка, никогда `<p></p>`; ссылки без `rel` и `target` |
| `setHTML(html)` | заменяет текст (HTML приводится к профилю) и **очищает историю отмены** (создаёт новое состояние редактора), `onChange` не зовёт; ответ «Оформить», пришедший после `setHTML`, к новому тексту не применяется |
| `getText()` | обычный текст: абзацы через пустую строку, пункты списков с «- » и «1. » |
| `getStats()` | `{visible, maxVisible, links, maxLinks, htmlLength, maxHtml, empty, schemeWord, overLimit}`; `schemeWord` — в тексте слово-схема (см. ниже), оно входит в `overLimit` |
| `focus()`, `setDisabled(bool)` | фокус и режим «только чтение» (панель обновляется сразу, открытый диалог ссылки закрывается) |
| `destroy()` | убирает редактор и обработчики со страницы |
| `editor` | объект Tiptap для особых случаев. Текст читать только через `getHTML()` этого объекта, не через `editor.getHTML()` |

Чистые функции без редактора: `sanitize(html, profile, {foreignStyles?})`, `measure(html, profile)`,
`cleanHref(href, profile)`, `parseLinkInput(text, profile)`, `fromLegacy(text, profile)`,
`artToPlainText(html, profile)`, а также `PROFILES`, `profileByName`, `MESSAGES`, `resolveLocale`.

**Вход без редактора `@arena/rich-editor/text`** (RT-11). Те же чистые функции (`sanitize`, `measure`, `fromLegacy`,
`artToPlainText`, `cleanHref`, `parseLinkInput`, `PROFILES`) без Tiptap и ProseMirror: черновик мастера, подсказки афиши
и сообщение о переносе считают и сравнивают тексты, а сам редактор грузится позже, на экране, который его показывает
(`import('@arena/rich-editor')` в отдельный кусок). `sanitize`, `measure` и `artToPlainText` читают HTML через
`<template>`, поэтому им нужен DOM (браузер или jsdom); `fromLegacy` DOM не нужен. `tests/build.test.ts` следит, чтобы в
`dist/text.js` и его общем куске не было ни Tiptap, ни ProseMirror.

**`fromLegacy`** — построчный порт Go `richtext.FromLegacy`: пустая строка — абзац, одиночный перенос — `<br>`, `**x**` —
strong, `_x_` на границе слова — em, блок из двух и более строк «- », «* », «• » или «1. », «2. » — список, `<b>`, набранный
человеком, остаётся текстом; автоссылки на `https://` только у профиля `email_note`. `tests/legacy.test.ts` прогоняет поля
`legacy` и `art` общего корпуса `richtext_legacy.json`: правка `legacy.go` без правки порта ломает сборку редактора.

## Профили и Go

Таблица `PROFILES` в `src/profiles.ts` — копия профилей Go-пакета `apps/backend/internal/platform/richtext`
(`richtext.go`: `ProfileDescription`, `ProfileEmailNote`, `ProfileSessionMessage`). Тест
`tests/profiles.test.ts` **читает `richtext.go`** и сравнивает теги, схемы ссылок и лимиты поле в поле, поэтому
правка в Go без правки таблицы ломает сборку редактора.

| Профиль (`kind` API) | Теги | Схемы ссылок | Видимых знаков | HTML в базе | Ссылок |
|---|---|---|---|---|---|
| `description` | p, br, strong, em, u, a, ul, ol, li, h3, h4, blockquote | https, mailto | 4000 | 16000 | 20 |
| `email_note` | p, br, strong, em, u, a, ul, ol, li | https | 2000 | 8000 | 5 |
| `session_message` | то же | https | 1000 | 4000 | 3 |

Заголовки (H3, H4) и цитата есть **только** в профиле `description` (решение владельца 24.10.2026, §9.1 спеки).
В остальных профилях этих кнопок нет, а заголовок и цитата во входящем тексте становятся абзацами (заголовок —
абзацем с `strong`).

## Канонический вывод

`getHTML()` собирается из той же модели, что и серверный `Sanitize` (порт `doc.go` и `sanitize.go`):
вложенность меток ссылка → strong → em → u, пробелы схлопываются и обрезаются по краям, пустые абзацы, пункты и
ссылки пропадают, `<br>` не стоит на краю абзаца, `&nbsp;` становится пробелом, заголовок живёт только на верхнем
уровне, цитата содержит только абзацы, списки вложены не глубже двух уровней, `<ol start>` не сохраняется.
Запрещённые символы (§3.1 спеки: C0/C1, подмена направления U+202A–U+202E, теги U+E0000…, невидимые операторы)
молча убираются, как и изоляты, U+FEFF, U+200B, U+2060. Метки направления LRM/RLM/ALM, ZWJ/ZWNJ и мягкий перенос
остаются.

Общий корпус `richtext/testdata/richtext_legacy.json` читает `tests/corpus.test.ts`: каждое значение `art`
проходит `setContent` → `getHTML` с профилем из записи и должно вернуться тем же. Исключений нет.

## Вставка

Всё вставленное (из Word, Google Docs, страниц сайтов, перетаскиванием) режется до белого списка профиля **до**
того, как ProseMirror прочтёт его (`transformPastedHTML`): пропадают `style` и `class`, все прочие атрибуты,
`span`, `div`, `img`, `table`, `script`, `style`, `iframe`, `svg`, комментарии. Текст ячеек таблицы и блоков
остаётся, каждый — отдельным абзацем.

Для вставки (и только для неё) читаются стили, которыми Word, Google Docs и сайты рисуют жирный, курсив и
подчёркивание: `font-weight`, `font-style`, `text-decoration`. Обёртка Google Docs
`<b style="font-weight:normal">` не делает весь вставленный текст жирным. Подчёркивание внутри ссылки не
превращается в `u`. Хранимый текст (`setHTML`, `value`) читается ровно так, как его читает Go.

Адрес, вставленный как текст, остаётся текстом: автоссылок нет, ссылку делают кнопкой.

**Большая вставка.** Размер разметки сравнивается с 1 МБ **в байтах**, как в Go. Если разметка больше, из неё до проверки
вырезаются комментарии, `head`, `style`, `script`, `img` и `data:`-адреса (страница с картинками внутри весит мегабайты и
текста почти не несёт); остаток читается как обычно, с оформлением. Если разметка всё равно слишком велика или
вложена глубже 200, вставляется **обычный текст** буфера (`text/plain`, без запрещённых символов), а под полем
появляется сообщение. То же для перетаскивания. В режиме «только чтение» вставка не обрабатывается.

## Слова-схемы в `email_note`

Профиль `email_note` в Go имеет `StrictSchemeWords`: видимый текст со словом `javascript:`, `vbscript:`, `data:`, `file:`,
`blob:`, `about:` или `http:`, за которым сразу идёт непробельный знак, API отвергает (`scheme_not_allowed`). Редактор
переносит регулярное выражение `schemeWordRE` с той же границей слова (`[^\p{L}\p{N}]`) и ASCII-вариантом `\S` (`[^\t\n\f\r ]`), ставит
`schemeWord` и `overLimit`, красит счётчик и пишет под полем сообщение (на всех пяти языках). Текст не меняется:
решает человек. Общая таблица случаев для Go и TS: `apps/backend/internal/platform/richtext/testdata/scheme_words_table.json`.

## Кнопки, ссылка, клавиши

Панель — `role="toolbar"`, одна точка Tab, стрелки (в иврите зеркально), Home и End; у каждой кнопки есть
`aria-label`, у переключателей `aria-pressed`, у всех с клавишей `aria-keyshortcuts`; Escape возвращает фокус в
поле.

| Кнопка | Клавиши |
|---|---|
| Жирный, курсив, подчёркнутый | Ctrl/⌘ + B, I, U |
| Ссылка (диалог), «Убрать ссылку» | Ctrl/⌘ + K |
| Маркированный, нумерованный список | Ctrl/⌘ + Shift + 8, 7; Tab и Shift+Tab — уровень (не глубже двух) |
| H3, H4, цитата (только `description`) | Ctrl/⌘ + Alt + 3, 4; Ctrl/⌘ + Shift + B |
| Отменить, повторить | Ctrl/⌘ + Z, Ctrl/⌘ + Shift + Z (и Ctrl + Y) |
| Перенос строки | Shift + Enter |

**Диалог ссылки** проверяет адрес так же, как `href.go` (`cleanHref`): схема из профиля (`https`, а в
`description` ещё `mailto`), у `https` есть хост, нет user-info, порта, IP-адреса (в том числе десятичного,
шестнадцатеричного и полноширинного), `localhost`, пробелов, bidi и невидимых символов, длина не больше 500
байт; хост приводится к ASCII (IDNA); у `mailto` отбрасывается всё после `?`. Адрес без схемы дополняется:
`example.com` → `https://example.com`, `a@b.com` → `mailto:a@b.com` (только где профиль разрешает `mailto`).
Лимит ссылок профиля тоже проверяется. Без выделения диалог просит ещё и текст ссылки.

## Кнопка «Оформить»

Появляется, если хозяин передал `format`. Редактор вызывает `format({text, profile})`, где `text` — обычный текст из
редактора (`getText()`: абзацы через пустую строку, списки с «- » и «1. », без жирного и ссылок), а `profile` — имя
профиля, то есть `kind` API. Хозяин отправляет их на `POST /v1/organizations/{org_id}/texts/format` (тело
`{text, kind, locale}`) и возвращает ответ `{html, ai_applied, ...}`.

```ts
format: async ({ text, profile }) => {
  const r = await api.post(`/v1/organizations/${orgId}/texts/format`, { text, kind: profile, locale })
  return r // { html, ai_applied, reason?, prompt_version? }
}
```

- Ответ режется до профиля и подставляется **одной транзакцией со своим шагом истории**: «Отменить» возвращает
  прежний текст целиком, даже если человек печатал за долю секунды до нажатия.
- Пока идёт запрос, поле только для чтения, кнопки выключены, у корня `aria-busy="true"`.
- `ai_applied: false` (сервер вернул `FromLegacy(text)`): ответ всё равно подставляется, под полем сообщение
  «Автоматическое оформление сейчас недоступно…». Сбой или пустой ответ оставляют текст как был и пишут сообщение.
- Сообщение о результате пропадает при следующей правке.
- Серверу уходит только обычный текст, поэтому ссылки, жирный, курсив и подчёркивание он не видит, а результат их
  заменяет. Если они есть, статус предупреждает об этом **до** подстановки («Оформляем текст… Ссылки и выделение
  текста при оформлении не передаются…») и повторяет предупреждение после неё; «Отменить» одним шагом возвращает всё.
- Если за время запроса текст стал «только чтение» или его заменил `setHTML`, ответ не подставляется.

## Счётчик

Под полем: «N из M знаков» (видимые знаки так, как их считает сервер: текст блоков плюс по одному знаку на
границу между блоками и на `<br>`, по кодовым точкам) и, если есть ссылки, «N из M ссылок». Превышение лимита —
красный счётчик и одно сообщение для чтения с экрана. Редактор **не обрезает** текст: последнее слово за API.

## CSP

Работает под `default-src 'none'; script-src 'self'; style-src 'self'` без `unsafe-inline`:

- `injectCSS: false`, стили — файл `dist/rich-editor.css` (без `@import` и шрифтов, шрифт наследуется,
  цвета меняются переменными `--arena-re-*`);
- ни одного `style`-атрибута в разметке редактора, ни одного NodeView;
- в сборке нет `eval` и `new Function`;
- браузер сообщает о нарушении CSP за каждый `style`-атрибут любой разобранной строки, поэтому перед разбором
  `sanitize` переименовывает `style` в `data-arena-style` (текст стиля остаётся доступен для вставки), и вставка
  из Word не засоряет консоль. Проверено в Chromium под такой политикой: 0 сообщений.

## Языки и RTL

Подписи кнопок, диалога и сообщений на en, ru, cs, he, es (`MESSAGES`); тест проверяет одинаковый набор ключей и
плейсхолдеров во всех пяти и что в доступном имени кнопки «Оформить» есть её видимая подпись (WCAG 2.5.3). Для иврита
у корня `dir="rtl"`, стрелки панели зеркальны, у поля адреса `dir="ltr"`. Атрибут `lang` языка интерфейса стоит только
на панели, диалоге и нижней строке, не на самом тексте (английский текст не читается русским голосом). Каждый абзац и
пункт получает `unicode-bidi: plaintext`: абзац на иврите при интерфейсе `ltr` начинается справа.

## Разработка

```
npm --prefix apps/rich-editor ci
npm --prefix apps/rich-editor run type-check
npm --prefix apps/rich-editor run build     # dist/: ESM, IIFE, rich-editor.css, types/
npm --prefix apps/rich-editor test          # vitest в jsdom; build.test.ts проверяет dist/, поэтому после build
```

На этом Windows-хосте `npm run` из git-bash нужен с `--script-shell="C:/Program Files/Git/bin/bash.exe"`.
**`npm install <пакет>` и `npm --prefix … install` из корня репозитория дописывают в `package.json`
зависимость `"arena-new": "file:../.."`** — ставьте из каталога пакета (`cwd`) и проверяйте `git diff`
`package.json` и `package-lock.json` перед коммитом.

Файлы: `src/profiles.ts` (таблица профилей), `src/chars.ts` и `src/href.ts` (политика символов и ссылок, порт
`href.go`), `src/model.ts` и `src/sanitize.ts` (модель текста, канонический вывод, очистка вставки),
`src/extensions.ts` (набор Tiptap 3 из отдельных расширений и собственная метка ссылки: `@tiptap/extension-link` тянет
linkifyjs ради автоссылок, которых тут нет; в сборке нет code, codeBlock, horizontalRule, strike, курсоров и linkify;
подпути пакетов `…/extension-list/item` и `…/extensions/undo-redo` нужны, чтобы индексные файлы не втянули остальное), `src/toolbar.ts`, `src/link-dialog.ts`, `src/editor.ts` (`create`),
`src/i18n.ts`, `src/rich-editor.css`.

Общие с Go таблицы (`apps/backend/internal/platform/richtext/testdata/`): `links_table.json` (`cleanHref`: IDNA, `mailto` со `/` и `#`,
метка длиннее 63 знаков; Go-тест `href_table_test.go`, TS-тест `tests/shared-tables.test.ts`) и `scheme_words_table.json`.
Подсписки: пункт `li` держит абзац и любое число вложенных списков (`paragraph (bulletList | orderedList)*`, как `item.subs`
в Go); случай из двух подсписков проверяют Go-тест `sublists_test.go` и TS-тест, в `richtext_legacy.json` его нет, потому
что `FromLegacy` вложенных списков не делает. Бюджет сборки: `tests/build.test.ts` держит gzip ESM и IIFE в границах.

Не входит в RT-10: подстановка редактора в поля веб-ивент-центра (RT-11), разбор текстов без `<p>` в стиле
WordPress (`AutoParagraph` делает сервер).
