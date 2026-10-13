import type { PageLocale } from './locale.ts';

/** Page-chrome strings (loading / not-found / error / footer / promoter
 * page). Event content itself is already locale-resolved by the backend
 * and rendered verbatim. */
export interface PageStrings {
  loading: string;
  notFoundTitle: string;
  notFoundBody: string;
  notFoundHome: string;
  errorTitle: string;
  errorBody: string;
  errorRetry: string;
  footerRights: string;
  /** Promoter landing page (GET /v1/public/pages/{org_slug}): org has a
   * hosted-page channel but zero currently-visible events. */
  promoterEmptyTitle: string;
  promoterEmptyBody: string;
  /** Heading over the promoter page's date list, and the label of the
   * poster block's button that jumps down to it. */
  promoterPickDate: string;
  /** Heading over the poster catalog, where the choice is a show, not a date. */
  promoterPickEvent: string;
  /** Fallback link on a date the list could not mount a picker for,
   * pointing at the event's own page. Every date normally carries its
   * picker open, so this is the exception, not the usual control. */
  ticketsCta: string;
  /** Replaces the tickets label on a date that has already happened. The
   * row stays a link — the event page still shows what took place. */
  eventPast: string;
  /** Back link shown on the per-event page, pointing at the org's
   * promoter page. */
  backToPromoter: string;
  /** Prefix of the doors-open time ("Doors open 19:30"). */
  doorsOpen: string;
  /** Legal block (who sells, who organizes). "Seller" and "Organizer" are two
   * different roles and their wording must never read as the same one. */
  sellerLabel: string;
  organizerLabel: string;
  registrationNo: string;
  /** Tax number labels: the generic one and the per-scheme ones. */
  taxId: string;
  vatId: string;
  einId: string;
}

const STRINGS: Record<PageLocale, PageStrings> = {
  en: {
    loading: 'Loading event…',
    notFoundTitle: 'Event not found',
    notFoundBody: 'We cannot find this page. The organizer may have taken the event off sale, or the link has a mistake. Check the link or ask the organizer.',
    notFoundHome: 'Go to Arena Sold Out',
    errorTitle: 'Something went wrong',
    errorBody: 'We could not load this event. Try again in a minute.',
    errorRetry: 'Retry',
    footerRights: 'Arena Sold Out. All rights reserved.',
    promoterEmptyTitle: 'No upcoming dates yet',
    promoterEmptyBody: 'This organizer has no events on sale right now. Come back later.',
    promoterPickDate: 'Choose a date',
    promoterPickEvent: 'Choose an event',
    ticketsCta: 'Tickets',
    eventPast: 'Took place',
    backToPromoter: 'All dates',
    doorsOpen: 'Doors open',
    sellerLabel: 'Seller',
    organizerLabel: 'Organizer',
    registrationNo: 'Registration no.',
    taxId: 'Tax ID',
    vatId: 'VAT ID',
    einId: 'EIN',
  },
  ru: {
    loading: 'Загружаем мероприятие…',
    notFoundTitle: 'Мероприятие не найдено',
    notFoundBody: 'Мы не нашли эту страницу. Возможно, организатор снял мероприятие с продажи или в ссылке ошибка. Проверьте ссылку или спросите организатора.',
    notFoundHome: 'На главную Arena Sold Out',
    errorTitle: 'Что-то пошло не так',
    errorBody: 'Не удалось загрузить мероприятие. Попробуйте ещё раз через минуту.',
    errorRetry: 'Повторить',
    footerRights: 'Arena Sold Out. Все права защищены.',
    promoterEmptyTitle: 'Пока нет ближайших дат',
    promoterEmptyBody: 'Сейчас у этого организатора нет мероприятий в продаже. Загляните позже.',
    promoterPickDate: 'Выберите дату',
    promoterPickEvent: 'Выберите мероприятие',
    ticketsCta: 'Билеты',
    eventPast: 'Уже прошло',
    backToPromoter: 'Все даты',
    doorsOpen: 'Вход с',
    sellerLabel: 'Продавец',
    organizerLabel: 'Организатор',
    registrationNo: 'Регистрационный номер',
    taxId: 'Налоговый номер',
    vatId: 'Номер плательщика НДС',
    einId: 'EIN',
  },
  cs: {
    loading: 'Načítání akce…',
    notFoundTitle: 'Akce nenalezena',
    notFoundBody: 'Tuto stránku jsme nenašli. Pořadatel možná akci stáhl z prodeje, nebo je v odkazu chyba. Zkontrolujte odkaz nebo se zeptejte pořadatele.',
    notFoundHome: 'Na Arena Sold Out',
    errorTitle: 'Něco se pokazilo',
    errorBody: 'Akci se nepodařilo načíst. Zkuste to znovu za minutu.',
    errorRetry: 'Zkusit znovu',
    footerRights: 'Arena Sold Out. Všechna práva vyhrazena.',
    promoterEmptyTitle: 'Zatím žádné nadcházející termíny',
    promoterEmptyBody: 'Tento pořadatel zatím nezveřejnil žádnou akci. Zkuste to prosím později.',
    promoterPickDate: 'Vyberte termín',
    promoterPickEvent: 'Vyberte akci',
    ticketsCta: 'Vstupenky',
    eventPast: 'Již proběhlo',
    backToPromoter: 'Všechny termíny',
    doorsOpen: 'Vstup od',
    sellerLabel: 'Prodávající',
    organizerLabel: 'Pořadatel',
    registrationNo: 'Registrační číslo',
    taxId: 'DIČ',
    vatId: 'DIČ',
    einId: 'EIN',
  },
  he: {
    loading: 'טוען את האירוע…',
    notFoundTitle: 'האירוע לא נמצא',
    notFoundBody: 'לא מצאנו את הדף הזה. ייתכן שהמארגן הוריד את האירוע מהמכירה או שיש טעות בקישור. בדקו את הקישור או פנו למארגן.',
    notFoundHome: 'לעמוד הבית של Arena Sold Out',
    errorTitle: 'משהו השתבש',
    errorBody: 'לא הצלחנו לטעון את האירוע כרגע. נסו שוב בעוד רגע.',
    errorRetry: 'נסו שוב',
    footerRights: 'Arena Sold Out. כל הזכויות שמורות.',
    promoterEmptyTitle: 'אין עדיין תאריכים קרובים',
    promoterEmptyBody: 'המפיק הזה עדיין לא פרסם אירועים. נסו שוב בקרוב.',
    promoterPickDate: 'בחרו תאריך',
    promoterPickEvent: 'בחרו אירוע',
    ticketsCta: 'כרטיסים',
    eventPast: 'כבר התקיים',
    backToPromoter: 'כל התאריכים',
    doorsOpen: 'פתיחת דלתות',
    sellerLabel: 'מוכר',
    organizerLabel: 'מארגן',
    registrationNo: 'מספר רישום',
    taxId: 'מזהה מס',
    vatId: 'מספר עוסק במע״מ',
    einId: 'EIN',
  },
  es: {
    loading: 'Cargando evento…',
    notFoundTitle: 'Evento no encontrado',
    notFoundBody: 'No encontramos esta página. Puede que el organizador haya retirado el evento de la venta o que el enlace tenga un error. Revisa el enlace o pregunta al organizador.',
    notFoundHome: 'Ir a Arena Sold Out',
    errorTitle: 'Algo salió mal',
    errorBody: 'No pudimos cargar este evento. Inténtalo de nuevo en un minuto.',
    errorRetry: 'Reintentar',
    footerRights: 'Arena Sold Out. Todos los derechos reservados.',
    promoterEmptyTitle: 'Aún no hay fechas próximas',
    promoterEmptyBody: 'Este organizador todavía no ha publicado ningún evento. Vuelve a consultarlo más tarde.',
    promoterPickDate: 'Elige una fecha',
    promoterPickEvent: 'Elige un evento',
    ticketsCta: 'Entradas',
    eventPast: 'Ya se celebró',
    backToPromoter: 'Todas las fechas',
    doorsOpen: 'Apertura de puertas',
    sellerLabel: 'Vendedor',
    organizerLabel: 'Organizador',
    registrationNo: 'N.º de registro',
    taxId: 'ID fiscal',
    vatId: 'ID de IVA',
    einId: 'EIN',
  },
};

export function t(locale: PageLocale): PageStrings {
  return STRINGS[locale];
}
