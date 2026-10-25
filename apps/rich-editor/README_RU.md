# @arena/rich-editor — один редактор текстов для всех интерфейсов

Задача RT-10 спеки 44 (`x_spec44.md`, разделы 3 и 5). Библиотека без React и без других фреймворков на ядре
Tiptap 2. Её используют веб-ивент-центр (RT-11), мини-приложение Telegram (RT-12) и WordPress (RT-20).
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
| `format` | `(text) => Promise<{html, ai_applied, reason?, prompt_version?}>` | включает кнопку «Оформить» (см. ниже) |
| `dir` | `'ltr' \| 'rtl'` | направление; по умолчанию `rtl` для `he`, иначе `ltr` |
| `label` / `labelledBy` | `string` | `aria-label` поля или `id` элемента, который его подписывает |
| `disabled` | `boolean` | только чтение |

### Что возвращает `create`

| Метод | Смысл |
|---|---|
| `getHTML()` | канонический ART. Пустой текст — пустая строка, никогда `<p></p>`; ссылки без `rel` и `target` |
| `setHTML(html)` | заменяет текст (HTML приводится к профилю) и очищает историю отмены |
| `getText()` | обычный текст: абзацы через пустую строку, пункты списков с «- » и «1. » |
| `getStats()` | `{visible, maxVisible, links, maxLinks, htmlLength, maxHtml, empty, overLimit}` |
| `focus()`, `setDisabled(bool)` | фокус и режим «только чтение» |
| `destroy()` | убирает редактор и обработчики со страницы |
| `editor` | объект Tiptap для особых случаев. Текст читать только через `getHTML()` этого объекта, не через `editor.getHTML()` |

Чистые функции без редактора: `sanitize(html, profile, {foreignStyles?})`, `measure(html, profile)`,
`cleanHref(href, profile)`, `parseLinkInput(text, profile)`, а также `PROFILES`, `profileByName`, `MESSAGES`,
`resolveLocale`.

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

Появляется, если хозяин передал `format`. Редактор вызывает `format(text)`, где `text` — обычный текст из
редактора (`getText()`: абзацы через пустую строку, списки с «- » и «1. », без жирного и ссылок). Хозяин
отправляет его на `POST /v1/organizations/{org_id}/texts/format` (тело `{text, kind, locale}`, `kind` = имя
профиля) и возвращает ответ `{html, ai_applied, ...}`.

```ts
format: async (text) => {
  const r = await api.post(`/v1/organizations/${orgId}/texts/format`, { text, kind: 'description', locale })
  return r // { html, ai_applied, reason?, prompt_version? }
}
```

- Ответ режется до профиля и подставляется **одной транзакцией со своим шагом истории**: «Отменить» возвращает
  прежний текст целиком, даже если человек печатал за долю секунды до нажатия.
- Пока идёт запрос, поле только для чтения, кнопки выключены, у корня `aria-busy="true"`.
- `ai_applied: false` (сервер вернул `FromLegacy(text)`): ответ всё равно подставляется, под полем сообщение
  «Автоматическое оформление сейчас недоступно…». Сбой или пустой ответ оставляют текст как был и пишут сообщение.
- Сообщение о результате пропадает при следующей правке.

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
плейсхолдеров во всех пяти. Для иврита у корня `dir="rtl"`, стрелки панели зеркальны, у поля адреса `dir="ltr"`.

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
`src/extensions.ts` (набор Tiptap), `src/toolbar.ts`, `src/link-dialog.ts`, `src/editor.ts` (`create`),
`src/i18n.ts`, `src/rich-editor.css`.

Не входит в RT-10: подстановка редактора в поля веб-ивент-центра (RT-11), разбор текстов без `<p>` в стиле
WordPress (`AutoParagraph` делает сервер).
