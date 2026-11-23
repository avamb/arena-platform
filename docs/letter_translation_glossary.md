# Глоссарий переводов писем и PDF (uk, pl, hu, et, lv, lt, fi)

Спека 63, задача Л-4. Письма покупателю и PDF-билет переводятся **с английского** по этому глоссарию. Для уже готовых cs, de, es, fr, ru, he и для pl, uk в PDF ориентир — существующие файлы того же вида.

## Правила

1. Переводим только видимый текст. Всё внутри `{{...}}`, HTML-теги, атрибуты, стили и ссылки остаются как в английском файле. Меняется только `lang` в `<html lang="...">` на код языка.
2. Перестановка слов внутри предложения разрешена, потеря или добавление `{{...}}` и тегов — нет. Это проверяет тест `TestTranslationParityWithEnglish` (`apps/backend/internal/platform/delivery/templates/templates_translation_parity_test.go`).
3. Обращение к покупателю — вежливое «вы» своего языка (uk Ви, pl Państwo/Pan i Pani без имени, hu Ön, et teie, lv jūs, lt jūs, fi te/passiivi). Тон спокойный, сервисный, без восклицаний.
4. Бренд `ArenaSoldOut.com` и имя организации не переводим и не склоняем: вставляем как есть.
5. Подпись «Не отвечайте на это письмо» и кнопка «Связаться с организатором» берутся из `partials/contact.<язык>.tmpl`.
6. Даты и суммы приходят готовыми строками из кода, не форматируем.

## Термины

| en | uk | pl | hu | et | lv | lt | fi |
|---|---|---|---|---|---|---|---|
| ticket | квиток | bilet | jegy | pilet | biļete | bilietas | lippu |
| order | замовлення | zamówienie | rendelés | tellimus | pasūtījums | užsakymas | tilaus |
| organizer | організатор | organizator | szervező | korraldaja | organizators | organizatorius | järjestäjä |
| event | захід | wydarzenie | esemény | üritus | pasākums | renginys | tapahtuma |
| session / date | сеанс | termin | időpont | toimumisaeg | norises laiks | seansas | ajankohta |
| venue | місце проведення | miejsce | helyszín | toimumiskoht | norises vieta | vieta | paikka |
| seat | місце | miejsce | ülőhely | koht | vieta | vieta | paikka |
| row | ряд | rząd | sor | rida | rinda | eilė | rivi |
| sector | сектор | sektor | szektor | sektor | sektors | sektorius | sektori |
| category | категорія | kategoria | kategória | kategooria | kategorija | kategorija | kategoria |
| QR code | QR-код | kod QR | QR-kód | QR-kood | QR kods | QR kodas | QR-koodi |
| doors open | вхід з | wejście od | beengedés | uksed avatakse | durvis atver | durys atidaromos | ovet aukeavat |
| refund | повернення коштів | zwrot środków | visszatérítés | tagastus | atmaksa | pinigų grąžinimas | hyvitys |
| invitation | запрошення | zaproszenie | meghívó | kutse | ielūgums | kvietimas | kutsu |
| cancelled (event) | скасовано | odwołane | elmarad | tühistatud | atcelts | atšauktas | peruttu |
| moved (event) | перенесено | przeniesione | áthelyezve | ära viidud | pārcelts | perkelta | siirretty |
| holder | власник | posiadacz | birtokos | omanik | īpašnieks | savininkas | haltija |
| seller | продавець | sprzedawca | eladó | müüja | pārdevējs | pardavėjas | myyjä |
| my tickets | мої квитки | moje bilety | jegyeim | minu piletid | manas biļetes | mano bilietai | omat liput |
| unpaid order reminder | нагадування про неоплачене замовлення | przypomnienie o nieopłaconym zamówieniu | emlékeztető a ki nem fizetett rendelésre | meeldetuletus tasumata tellimuse kohta | atgādinājums par neapmaksātu pasūtījumu | priminimas apie neapmokėtą užsakymą | muistutus maksamattomasta tilauksesta |
| unsubscribe | відписатися | wypisać się | leiratkozás | tellimus lõpetada | atteikties no abonēšanas | atsisakyti prenumeratos | peruuta tilaus |

## PDF-билет

Таблица подписей PDF (`delivery/pdf/labels.go`) заполняется теми же словами: Category, Seat, Price, row, seat, Ticket #, Order, Organizer, Holder, Contact, Doors open, Directions, Invitation. Даты: названия месяцев в форме, которая стоит в дате (в uk, pl, lv, lt, fi — родительный или партитив), названия дней недели с маленькой буквы, кроме hu, et, lv, lt, fi — тоже с маленькой.

## Что не переведено (остаётся английским запасным)

Письма организатору и магазина: `refund_blocked`, `refund_scan_exception`, `sales_alert`, `referral_*`, `shop_order_*`. Lampyris на сайте их не использует.
