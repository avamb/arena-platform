import type { SupportedLocale } from './locale.ts';

/** Page-chrome strings (loading / not-found / error / footer). Event content
 * itself is already locale-resolved by the backend and rendered verbatim. */
export interface PageStrings {
  loading: string;
  notFoundTitle: string;
  notFoundBody: string;
  notFoundHome: string;
  errorTitle: string;
  errorBody: string;
  errorRetry: string;
  footerRights: string;
}

const STRINGS: Record<SupportedLocale, PageStrings> = {
  en: {
    loading: 'Loading event…',
    notFoundTitle: 'Event not found',
    notFoundBody: "We couldn't find the page you were looking for. It may have been unpublished or the link may be incorrect.",
    notFoundHome: 'Go to Arena Sold Out',
    errorTitle: 'Something went wrong',
    errorBody: 'We could not load this event right now. Please try again in a moment.',
    errorRetry: 'Retry',
    footerRights: 'Arena Sold Out. All rights reserved.',
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
  },
};

export function t(locale: SupportedLocale): PageStrings {
  return STRINGS[locale];
}
