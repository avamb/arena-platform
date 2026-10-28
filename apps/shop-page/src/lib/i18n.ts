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
  poweredBy: 'На платформе Arena Sold Out',
  share: 'Share',
  shareUrl: 'Ссылка',
  shareClose: 'Свернуть функцию "Поделиться"',
  shareCopy: 'Копировать ссылку',
  youMayAlsoLike: 'You may also like',
  contactName: 'Имя',
  contactEmail: 'Адрес электронной почты',
  contactPhone: 'Номер телефона',
  contactComment: 'Комментарий',
  contactSend: 'Отправить',
  contactFormTitle: 'Форма для связи',
} as const;

/** Russian plural form of Dawn's sections.header.cart_count ("Элементов: 3"). */
export function cartCountLabel(count: number): string {
  return count === 1 ? `Элементы: ${count}` : `Элементов: ${count}`;
}

export function resultsLabel(count: number, terms: string): string {
  return `${count} рез. по запросу «${terms}»`;
}
