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
  /** Label of each event card's link to the per-event page. */
  ticketsCta: string;
  /** Replaces the tickets label on a date that has already happened. The
   * row stays a link — the event page still shows what took place. */
  eventPast: string;
  /** Back link shown on the per-event page, pointing at the org's
   * promoter page. */
  backToPromoter: string;
}

const STRINGS: Record<PageLocale, PageStrings> = {
  en: {
    loading: 'Loading event…',
    notFoundTitle: 'Event not found',
    notFoundBody: "We couldn't find the page you were looking for. It may have been unpublished or the link may be incorrect.",
    notFoundHome: 'Go to Arena Sold Out',
    errorTitle: 'Something went wrong',
    errorBody: 'We could not load this event right now. Please try again in a moment.',
    errorRetry: 'Retry',
    footerRights: 'Arena Sold Out. All rights reserved.',
    promoterEmptyTitle: 'No upcoming dates yet',
    promoterEmptyBody: 'This organizer has not published any events right now. Please check back later.',
    promoterPickDate: 'Choose a date',
    ticketsCta: 'Tickets',
    eventPast: 'Took place',
    backToPromoter: 'All dates',
  },
  ru: {
    loading: 'Загрузка события…',
    notFoundTitle: 'Событие не найдено',
    notFoundBody: 'Мы не смогли найти запрошенную страницу. Возможно, она была снята с публикации, или ссылка неверна.',
    notFoundHome: 'На главную Arena Sold Out',
    errorTitle: 'Что-то пошло не так',
    errorBody: 'Не удалось загрузить событие. Пожалуйста, попробуйте ещё раз через момент.',
    errorRetry: 'Повторить',
    footerRights: 'Arena Sold Out. Все права защищены.',
    promoterEmptyTitle: 'Пока нет ближайших дат',
    promoterEmptyBody: 'Этот организатор пока не опубликовал ни одного события. Загляните позже.',
    promoterPickDate: 'Выберите дату',
    ticketsCta: 'Билеты',
    eventPast: 'Уже прошло',
    backToPromoter: 'Все даты',
  },
  cs: {
    loading: 'Načítání akce…',
    notFoundTitle: 'Akce nenalezena',
    notFoundBody: 'Požadovanou stránku se nepodařilo najít. Mohla být zrušena nebo je odkaz nesprávný.',
    notFoundHome: 'Na Arena Sold Out',
    errorTitle: 'Něco se pokazilo',
    errorBody: 'Akci se nepodařilo načíst. Zkuste to prosím za chvíli znovu.',
    errorRetry: 'Zkusit znovu',
    footerRights: 'Arena Sold Out. Všechna práva vyhrazena.',
    promoterEmptyTitle: 'Zatím žádné nadcházející termíny',
    promoterEmptyBody: 'Tento pořadatel zatím nezveřejnil žádnou akci. Zkuste to prosím později.',
    promoterPickDate: 'Vyberte termín',
    ticketsCta: 'Vstupenky',
    eventPast: 'Již proběhlo',
    backToPromoter: 'Všechny termíny',
  },
  he: {
    loading: 'טוען את האירוע…',
    notFoundTitle: 'האירוע לא נמצא',
    notFoundBody: 'לא הצלחנו למצוא את הדף המבוקש. ייתכן שהוא הוסר מהפרסום או שהקישור שגוי.',
    notFoundHome: 'לעמוד הבית של Arena Sold Out',
    errorTitle: 'משהו השתבש',
    errorBody: 'לא הצלחנו לטעון את האירוע כרגע. נסו שוב בעוד רגע.',
    errorRetry: 'נסו שוב',
    footerRights: 'Arena Sold Out. כל הזכויות שמורות.',
    promoterEmptyTitle: 'אין עדיין תאריכים קרובים',
    promoterEmptyBody: 'המפיק הזה עדיין לא פרסם אירועים. נסו שוב בקרוב.',
    promoterPickDate: 'בחרו תאריך',
    ticketsCta: 'כרטיסים',
    eventPast: 'כבר התקיים',
    backToPromoter: 'כל התאריכים',
  },
  es: {
    loading: 'Cargando evento…',
    notFoundTitle: 'Evento no encontrado',
    notFoundBody: 'No pudimos encontrar la página que buscabas. Puede que se haya despublicado o que el enlace sea incorrecto.',
    notFoundHome: 'Ir a Arena Sold Out',
    errorTitle: 'Algo salió mal',
    errorBody: 'No pudimos cargar este evento en este momento. Inténtalo de nuevo en un momento.',
    errorRetry: 'Reintentar',
    footerRights: 'Arena Sold Out. Todos los derechos reservados.',
    promoterEmptyTitle: 'Aún no hay fechas próximas',
    promoterEmptyBody: 'Este organizador todavía no ha publicado ningún evento. Vuelve a consultarlo más tarde.',
    promoterPickDate: 'Elige una fecha',
    ticketsCta: 'Entradas',
    eventPast: 'Ya se celebró',
    backToPromoter: 'Todas las fechas',
  },
};

export function t(locale: PageLocale): PageStrings {
  return STRINGS[locale];
}
