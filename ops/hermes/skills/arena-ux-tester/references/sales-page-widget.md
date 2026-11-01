# Виджет билетов на странице продаж (Shadow DOM)

`tickets.arenasoldout.com/<slug>/<event-slug>` рендерит покупку в custom-элементе
`<arena-tickets>`: содержимое живёт в shadow root и не попадает в `innerText` страницы,
дерево доступности и текстовые снапшоты. «Виджета нет» — только если блока нет и на снимке.

## Читать состояние

```js
const sr = document.querySelector('arena-tickets')?.shadowRoot;
// кнопки (stepper'ы подписаны aria-label):
[...sr.querySelectorAll('button')].map(b => ({
  t: (b.innerText || '').trim(), aria: b.getAttribute('aria-label') || ''
}));
// статус заказа: sr.querySelector('[data-testid=order-ref]')
```

- `sr.innerText` бывает `undefined` (падение на `.slice`): всегда `(sr.innerText || '')`
  или работай через `querySelectorAll` / `innerHTML`.
- Категории и их stepper'ы: «Increase/Decrease quantity for <Name>»;
  корзина: «Open cart (N items)»; подтверждение: `data-status="paid"` + `[data-testid=order-ref]`.

## Провести покупку

1. Клик «+» нужной категории по aria-label (программный `el.click()` эквивалентен нажатию;
   координатный клик со снимка — запасной вариант, если shadow-root доступ закрыт).
2. «Open cart» → «Continue to payment».
3. Поля покупателя: Svelte-инпуты заполняются native setter'ом + dispatch `input`/`change`
   (bubbles:true), иначе реактивное состояние не увидит значение:
   `Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(el,v)`.
4. Email и имя — обязательно; телефон опционален (placeholder «+1 555 000 0000»),
   оставлять пустым.
5. Бесплатная категория проходит сразу: «Payment successful!» + номер заказа.
   Платная при неподключённых платежах показывает отказ в форме (UXF-01, см. SKILL.md,
   шаг 12): записать текст, не заводить.

## Снимки и таймауты

`Page.captureScreenshot` иногда падает по таймауту IPC на этой странице. Продолжай
js()-пробами без снимка, а снимок повтори в свежей browser-сессии; не интерпретируй
таймаут снимка как «страница сломалась».

## Сопоставление с ботом

«Free» на странице продаж печатается как цена категории («Free Free» = название + цена) —
известный UXH-07; не заводить. Дата/время и «Doors open» в виджете должны совпадать с
карточкой бота и письмом (сверяй по словарю `checklist.md`).
