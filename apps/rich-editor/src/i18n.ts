// Button labels and messages in the five interface languages of the platform:
// en, ru, cs, he, es. Hebrew is right-to-left.

export type Locale = 'en' | 'ru' | 'cs' | 'he' | 'es'

export const LOCALES: readonly Locale[] = ['en', 'ru', 'cs', 'he', 'es']

export interface Messages {
  toolbarLabel: string
  editorLabel: string
  bold: string
  italic: string
  underline: string
  link: string
  unlink: string
  bulletList: string
  orderedList: string
  h3: string
  h4: string
  quote: string
  undo: string
  redo: string
  format: string
  formatHint: string
  formatting: string
  formatDone: string
  formatPlain: string
  formatFailed: string
  formatEmpty: string
  /** Added to the status when the text has links or styling that formatting from plain text replaces. */
  formatLossy: string
  /** A paste or drop that was too large for its markup: its plain text went in. */
  pasteFallback: string
  /** The text has a scheme word the strict profile refuses (scheme_not_allowed). */
  schemeWord: string
  linkDialogTitle: string
  linkAddress: string
  linkText: string
  linkHintHttps: string
  linkHintMailto: string
  linkApply: string
  linkCancel: string
  linkInvalidHttps: string
  linkInvalidMailto: string
  linkTooMany: string
  /** {n} characters of {max} */
  counter: string
  counterLabel: string
  /** {n} characters over the limit */
  overLimit: string
  /** {n} links of {max} */
  linksCounter: string
}

const en: Messages = {
  toolbarLabel: 'Text formatting',
  editorLabel: 'Text editor',
  bold: 'Bold',
  italic: 'Italic',
  underline: 'Underline',
  link: 'Link',
  unlink: 'Remove link',
  bulletList: 'Bulleted list',
  orderedList: 'Numbered list',
  h3: 'Heading',
  h4: 'Subheading',
  quote: 'Quote',
  undo: 'Undo',
  redo: 'Redo',
  format: 'Format',
  formatHint: 'Format the text automatically',
  formatting: 'Formatting the text…',
  formatDone: 'The text is formatted. You can undo it.',
  formatPlain: 'Automatic formatting is not available now. The text is kept as it is.',
  formatFailed: 'The text could not be formatted. Try again.',
  formatEmpty: 'There is nothing to format.',
  formatLossy: 'Links and text styling are not sent for formatting: its result replaces them. Undo brings them back.',
  pasteFallback: 'The pasted content is too large to keep its formatting. It was pasted as plain text.',
  schemeWord: 'The text has words like “http:”, “javascript:” or “data:” followed by text. Remove them: this note cannot be saved with them.',
  linkDialogTitle: 'Link',
  linkAddress: 'Link address',
  linkText: 'Link text',
  linkHintHttps: 'An address that starts with https://',
  linkHintMailto: 'An address that starts with https:// or an email address',
  linkApply: 'Apply',
  linkCancel: 'Cancel',
  linkInvalidHttps: 'Enter an address like https://example.com without a login, a port or an IP address.',
  linkInvalidMailto:
    'Enter an address like https://example.com or an email address, without a login, a port or an IP address.',
  linkTooMany: 'Too many links: at most {max}.',
  counter: '{n} of {max} characters',
  counterLabel: 'Characters',
  overLimit: 'The text is {n} characters over the limit.',
  linksCounter: '{n} of {max} links',
}

const ru: Messages = {
  toolbarLabel: 'Оформление текста',
  editorLabel: 'Редактор текста',
  bold: 'Жирный',
  italic: 'Курсив',
  underline: 'Подчёркнутый',
  link: 'Ссылка',
  unlink: 'Убрать ссылку',
  bulletList: 'Маркированный список',
  orderedList: 'Нумерованный список',
  h3: 'Заголовок',
  h4: 'Подзаголовок',
  quote: 'Цитата',
  undo: 'Отменить',
  redo: 'Повторить',
  format: 'Оформить',
  formatHint: 'Оформить текст автоматически',
  formatting: 'Оформляем текст…',
  formatDone: 'Текст оформлен. Это можно отменить.',
  formatPlain: 'Автоматическое оформление сейчас недоступно. Текст оставлен как есть.',
  formatFailed: 'Не удалось оформить текст. Попробуйте ещё раз.',
  formatEmpty: 'Оформлять нечего.',
  formatLossy: 'Ссылки и выделение текста при оформлении не передаются: результат их заменит. Отмена вернёт их.',
  pasteFallback: 'Вставленный фрагмент слишком большой, чтобы сохранить оформление. Он вставлен как обычный текст.',
  schemeWord: 'В тексте есть слова вида «http:», «javascript:» или «data:» с текстом сразу после двоеточия. Уберите их: с ними заметку нельзя сохранить.',
  linkDialogTitle: 'Ссылка',
  linkAddress: 'Адрес ссылки',
  linkText: 'Текст ссылки',
  linkHintHttps: 'Адрес, который начинается с https://',
  linkHintMailto: 'Адрес, который начинается с https://, или адрес электронной почты',
  linkApply: 'Применить',
  linkCancel: 'Отмена',
  linkInvalidHttps: 'Введите адрес вида https://example.com без логина, порта и IP-адреса.',
  linkInvalidMailto: 'Введите адрес вида https://example.com или адрес электронной почты, без логина, порта и IP-адреса.',
  linkTooMany: 'Слишком много ссылок: не больше {max}.',
  counter: '{n} из {max} знаков',
  counterLabel: 'Знаки',
  overLimit: 'Текст длиннее лимита на {n} знаков.',
  linksCounter: '{n} из {max} ссылок',
}

const cs: Messages = {
  toolbarLabel: 'Formátování textu',
  editorLabel: 'Textový editor',
  bold: 'Tučně',
  italic: 'Kurzíva',
  underline: 'Podtržené',
  link: 'Odkaz',
  unlink: 'Odebrat odkaz',
  bulletList: 'Odrážkový seznam',
  orderedList: 'Číslovaný seznam',
  h3: 'Nadpis',
  h4: 'Podnadpis',
  quote: 'Citace',
  undo: 'Zpět',
  redo: 'Znovu',
  format: 'Formátovat',
  formatHint: 'Formátovat text automaticky',
  formatting: 'Formátujeme text…',
  formatDone: 'Text je naformátovaný. Lze to vrátit zpět.',
  formatPlain: 'Automatické formátování teď není dostupné. Text zůstal beze změny.',
  formatFailed: 'Text se nepodařilo naformátovat. Zkuste to znovu.',
  formatEmpty: 'Není co formátovat.',
  formatLossy: 'Odkazy a zvýraznění textu se k formátování neposílají: výsledek je nahradí. Zpět je vrátí.',
  pasteFallback: 'Vložený obsah je příliš velký, než aby si zachoval formátování. Vložil se jako prostý text.',
  schemeWord: 'Text obsahuje slova jako „http:“, „javascript:“ nebo „data:“ s textem hned za dvojtečkou. Odstraňte je: s nimi se poznámka nedá uložit.',
  linkDialogTitle: 'Odkaz',
  linkAddress: 'Adresa odkazu',
  linkText: 'Text odkazu',
  linkHintHttps: 'Adresa, která začíná https://',
  linkHintMailto: 'Adresa, která začíná https://, nebo e-mailová adresa',
  linkApply: 'Použít',
  linkCancel: 'Zrušit',
  linkInvalidHttps: 'Zadejte adresu ve tvaru https://example.com bez přihlašovacího jména, portu a IP adresy.',
  linkInvalidMailto:
    'Zadejte adresu ve tvaru https://example.com nebo e-mailovou adresu, bez přihlašovacího jména, portu a IP adresy.',
  linkTooMany: 'Příliš mnoho odkazů: nejvýše {max}.',
  counter: '{n} z {max} znaků',
  counterLabel: 'Znaky',
  overLimit: 'Text je o {n} znaků delší než limit.',
  linksCounter: '{n} z {max} odkazů',
}

const he: Messages = {
  toolbarLabel: 'עיצוב טקסט',
  editorLabel: 'עורך טקסט',
  bold: 'מודגש',
  italic: 'נטוי',
  underline: 'קו תחתון',
  link: 'קישור',
  unlink: 'הסרת קישור',
  bulletList: 'רשימת תבליטים',
  orderedList: 'רשימה ממוספרת',
  h3: 'כותרת',
  h4: 'כותרת משנה',
  quote: 'ציטוט',
  undo: 'ביטול',
  redo: 'שחזור',
  format: 'עיצוב',
  formatHint: 'עיצוב הטקסט אוטומטית',
  formatting: 'מעצבים את הטקסט…',
  formatDone: 'הטקסט עוצב. אפשר לבטל.',
  formatPlain: 'העיצוב האוטומטי אינו זמין כרגע. הטקסט נשאר כפי שהוא.',
  formatFailed: 'לא הצלחנו לעצב את הטקסט. נסו שוב.',
  formatEmpty: 'אין מה לעצב.',
  formatLossy: 'קישורים והדגשות אינם נשלחים לעיצוב: התוצאה תחליף אותם. ביטול יחזיר אותם.',
  pasteFallback: 'התוכן שהודבק גדול מכדי לשמור על העיצוב. הוא הודבק כטקסט רגיל.',
  schemeWord: 'בטקסט יש מילים כמו “http:”, “javascript:” או “data:” עם טקסט מיד אחרי הנקודתיים. הסירו אותן: אי אפשר לשמור את ההערה איתן.',
  linkDialogTitle: 'קישור',
  linkAddress: 'כתובת הקישור',
  linkText: 'טקסט הקישור',
  linkHintHttps: 'כתובת שמתחילה ב-https://',
  linkHintMailto: 'כתובת שמתחילה ב-https:// או כתובת דוא"ל',
  linkApply: 'החלה',
  linkCancel: 'ביטול',
  linkInvalidHttps: 'יש להזין כתובת כמו https://example.com, בלי שם משתמש, פורט או כתובת IP.',
  linkInvalidMailto: 'יש להזין כתובת כמו https://example.com או כתובת דוא"ל, בלי שם משתמש, פורט או כתובת IP.',
  linkTooMany: 'יותר מדי קישורים: עד {max}.',
  counter: '{n} מתוך {max} תווים',
  counterLabel: 'תווים',
  overLimit: 'הטקסט חורג מהמגבלה ב-{n} תווים.',
  linksCounter: '{n} מתוך {max} קישורים',
}

const es: Messages = {
  toolbarLabel: 'Formato del texto',
  editorLabel: 'Editor de texto',
  bold: 'Negrita',
  italic: 'Cursiva',
  underline: 'Subrayado',
  link: 'Enlace',
  unlink: 'Quitar enlace',
  bulletList: 'Lista con viñetas',
  orderedList: 'Lista numerada',
  h3: 'Título',
  h4: 'Subtítulo',
  quote: 'Cita',
  undo: 'Deshacer',
  redo: 'Rehacer',
  format: 'Dar formato',
  formatHint: 'Dar formato al texto automáticamente',
  formatting: 'Dando formato al texto…',
  formatDone: 'El texto tiene formato. Puedes deshacerlo.',
  formatPlain: 'El formato automático no está disponible ahora. El texto se queda como está.',
  formatFailed: 'No se pudo dar formato al texto. Inténtalo de nuevo.',
  formatEmpty: 'No hay nada a lo que dar formato.',
  formatLossy: 'Los enlaces y los estilos del texto no se envían para dar formato: el resultado los sustituye. Deshacer los recupera.',
  pasteFallback: 'El contenido pegado es demasiado grande para conservar su formato. Se pegó como texto sin formato.',
  schemeWord: 'El texto tiene palabras como «http:», «javascript:» o «data:» seguidas de texto. Quítalas: la nota no se puede guardar con ellas.',
  linkDialogTitle: 'Enlace',
  linkAddress: 'Dirección del enlace',
  linkText: 'Texto del enlace',
  linkHintHttps: 'Una dirección que empiece por https://',
  linkHintMailto: 'Una dirección que empiece por https:// o un correo electrónico',
  linkApply: 'Aplicar',
  linkCancel: 'Cancelar',
  linkInvalidHttps: 'Escribe una dirección como https://example.com, sin usuario, puerto ni dirección IP.',
  linkInvalidMailto:
    'Escribe una dirección como https://example.com o un correo electrónico, sin usuario, puerto ni dirección IP.',
  linkTooMany: 'Demasiados enlaces: como máximo {max}.',
  counter: '{n} de {max} caracteres',
  counterLabel: 'Caracteres',
  overLimit: 'El texto supera el límite en {n} caracteres.',
  linksCounter: '{n} de {max} enlaces',
}

export const MESSAGES: Readonly<Record<Locale, Messages>> = { en, ru, cs, he, es }

/** Picks a supported locale from a tag like "ru", "ru-RU" or "iw"; English when unknown. */
export function resolveLocale(tag: string | undefined): Locale {
  if (!tag) return 'en'
  const primary = tag.toLowerCase().split(/[-_]/)[0]
  const alias = primary === 'iw' ? 'he' : primary
  return (LOCALES as readonly string[]).includes(alias) ? (alias as Locale) : 'en'
}

/** The text direction of a locale: Hebrew is right-to-left. */
export function directionOf(locale: Locale): 'ltr' | 'rtl' {
  return locale === 'he' ? 'rtl' : 'ltr'
}

export function format(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in values ? String(values[k]) : m))
}
