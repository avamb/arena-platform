/**
 * i18n.ts - the storefront's Russian strings, copied from Dawn 11's
 * locales/ru.json (the live shop's language). Russian is the only language of
 * this version; English and Hebrew come later (spec 47 section 1).
 */

export const T = {
  skipToContent: 'Перейти к контенту',
  skipToProductInfo: 'Перейти к информации о продукте',
  announcement: 'Объявление',
  menu: 'Меню',
  search: 'Поиск',
  searchReset: 'Удалить поисковый запрос',
  close: 'Закрыть',
  login: 'Войти',
  contacts: 'Контакты',
  cart: 'Корзина',
  loading: 'Загрузка…',
  continueShopping: 'Продолжить покупки',
  addToCart: 'Добавить в корзину',
  soldOut: 'Продано',
  onSale: 'Распродажа',
  chooseOptions: 'Выберите варианты',
  regularPrice: 'Обычная цена',
  salePrice: 'Цена со скидкой',
  unitPrice: 'Цена за единицу',
  unitPriceSeparator: 'за',
  fromPrice: 'От',
  taxesIncluded: 'Сумма налога включена.',
  quantity: 'Количество',
  variantSoldOut: 'Вариант распродан или недоступен',
  viewFullDetails: 'Просмотреть всю информацию',
  galleryViewer: 'Средство просмотра галереи',
  galleryModal: 'Галерея мультимедиа',
  slider: 'Ползунок',
  sliderPrev: 'Листать назад',
  sliderNext: 'Листать вперед',
  sliderOf: 'из',
  itemAdded: 'Товар добавлен в корзину.',
  viewCart: 'Просмотреть корзину',
  checkout: 'Оформить заказ',
  cartEmpty: 'Корзина пуста.',
  cartLoginTitle: 'Уже есть учетная запись?',
  cartTaxNote: 'Сумма налога, скидки и стоимость доставки, рассчитанные при оформлении заказа',
  estimatedTotal: 'Ориентировочная общая сумма',
  product: 'Товар',
  total: 'Всего',
  paymentMethods: 'Способы оплаты',
  searchResults: 'Результаты поиска',
  searchProducts: 'Товары',
  allProducts: 'Все товары',
  collectionLabel: 'Коллекция: ',
  collectionProducts: 'Продукты',
  notFoundTitle: 'Страница не найдена',
  notFoundButton: 'Продолжить покупки',
  platformPlaceholder: 'Магазины организаторов открываются по их адресам',
  poweredBy: 'На платформе Arena Sold Out',
  share: 'Share',
  shareUrl: 'Ссылка',
  shareClose: 'Свернуть функцию "Поделиться"',
  shareCopy: 'Копировать ссылку',
  youMayAlsoLike: 'Вам также может понравиться',
  contactName: 'Имя',
  contactEmail: 'Адрес электронной почты',
  contactPhone: 'Номер телефона',
  contactComment: 'Комментарий',
  contactSend: 'Отправить',
  contactFormTitle: 'Форма для связи',
  contactEmailLabel: 'Электронная почта:',
} as const;

/** Russian plural form of Dawn's sections.header.cart_count ("Элементов: 3"). */
export function cartCountLabel(count: number): string {
  return count === 1 ? `Элементы: ${count}` : `Элементов: ${count}`;
}

export function resultsLabel(count: number, terms: string): string {
  return `${count} рез. по запросу «${terms}»`;
}

/**
 * What the buyer reads for the API's error codes (the `shop.*` codes of the public
 * shop API). The server's own message is English and for logs: it is never shown.
 */
export const ERR = {
  'shop.age_confirmation_required': 'Подтвердите, что вам исполнилось нужное количество лет, чтобы оформить заказ.',
  'shop.invalid_item': 'Одного из товаров в корзине больше нет в продаже. Мы обновили корзину, проверьте её.',
  'shop.invalid_items': 'В заказе слишком много штук одного товара. Уменьшите количество и попробуйте ещё раз.',
  'shop.invalid_buyer': 'Проверьте имя, адрес электронной почты и номер телефона.',
  'shop.invalid_address': 'Проверьте адрес доставки.',
  'shop.invalid_body': 'Не удалось прочитать данные заказа. Обновите страницу и попробуйте ещё раз.',
  'shop.stock_changed': 'Наличие товаров изменилось, пока вы оформляли заказ. Попробуйте ещё раз.',
  'shop.too_many_pending':
    'У вас уже несколько неоплаченных заказов в этом магазине. Оплатите один из них или подождите около получаса, пока резерв освободится.',
  'shop.rate_limited': 'Слишком много попыток подряд. Подождите минуту и попробуйте ещё раз.',
  'shop.captcha_required': 'Подтвердите, что вы не робот, и нажмите «Оплатить» ещё раз.',
  'shop.captcha_failed': 'Проверка не пройдена. Попробуйте ещё раз.',
  'shop.captcha_unavailable': 'Не удалось проверить, что вы не робот. Подождите минуту и нажмите «Оплатить» ещё раз.',
  'shop.not_found': 'Магазин не найден.',
  'shop.order_not_found': 'Заказ не найден.',
} as const;

export const MSG = {
  /** Network, 5xx and 429 on any read: the page may well exist, the shop just cannot be reached. */
  unreachableTitle: 'Не удалось связаться с магазином',
  unreachableText: 'Это временная неполадка. Попробуйте ещё раз.',
  retry: 'Попробовать ещё раз',
  unreachableCheckout: 'Не удалось связаться с магазином, попробуйте ещё раз.',
  checkoutFailed: 'Не удалось оформить заказ. Попробуйте ещё раз.',
  invalidData: 'Проверьте введённые данные и попробуйте ещё раз.',
  captchaWait: 'Подтвердите, что вы не робот, и нажмите «Оплатить» ещё раз.',
  captchaLoad: 'Не удалось загрузить проверку «Я не робот». Отключите блокировщики рекламы или попробуйте позже.',
  // order status page
  checking: 'Проверяем платёж…',
  checkingText: 'Обычно это занимает меньше минуты. Страницу можно не закрывать.',
  waitingTitle: 'Ожидаем оплату',
  timeoutTitle: 'Проверка затянулась',
  timeoutText: 'Проверка затянулась, обновите страницу или проверьте письмо.',
  returnToPayment: 'Вернуться к оплате',
  // resume panel before a new checkout
  resumeTitle: 'У вас уже есть неоплаченный заказ',
  resumeText: 'Вы можете вернуться к его оплате, не создавая новый заказ.',
  resumeNew: 'Оформить новый заказ',
} as const;

export function resumeOrderText(number: string | number | null | undefined, total: string | null | undefined): string {
  const n = number != null && number !== '' ? ` № ${number}` : '';
  return total ? `Заказ${n} на сумму ${total} ждёт оплаты.` : `Заказ${n} ждёт оплаты.`;
}
