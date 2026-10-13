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
  /** "Write to the organizer" button and form (contact.ts). */
  contactButton: string;
  contactTitle: string;
  contactIntro: string;
  contactName: string;
  contactEmail: string;
  contactMessage: string;
  contactConsent: string;
  contactSend: string;
  contactCancel: string;
  contactSending: string;
  contactSent: string;
  contactClose: string;
  contactErrName: string;
  contactErrEmail: string;
  contactErrMessage: string;
  contactErrConsent: string;
  contactErrCaptcha: string;
  contactErrRate: string;
  contactErrGeneric: string;
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
    contactButton: "Write to the organizer",
    contactTitle: "Write to the organizer",
    contactIntro: "The organizer will answer you by e-mail.",
    contactName: "Your name",
    contactEmail: "Your e-mail",
    contactMessage: "Message",
    contactConsent: "I agree that Arena Sold Out passes my name, e-mail and message to the organizer so that they can answer me.",
    contactSend: "Send",
    contactCancel: "Cancel",
    contactSending: "Sending…",
    contactSent: "Thank you. Your message is on its way to the organizer. They will answer you by e-mail.",
    contactClose: "Close",
    contactErrName: "Enter your name, up to 100 characters.",
    contactErrEmail: "Enter your e-mail so that the organizer can answer you, for example anna@example.com.",
    contactErrMessage: "Write a message of 10 to 2000 characters.",
    contactErrConsent: "Tick the box to agree that we pass your message to the organizer.",
    contactErrCaptcha: "Complete the anti-robot check, then send the message again.",
    contactErrRate: "Too many messages were sent from here. Try again in an hour.",
    contactErrGeneric: "We could not send your message. Try again in a minute.",
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
    promoterPickEvent: 'Выберите событие',
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
    contactButton: "Написать организатору",
    contactTitle: "Написать организатору",
    contactIntro: "Организатор ответит вам по e-mail.",
    contactName: "Ваше имя",
    contactEmail: "Ваш e-mail",
    contactMessage: "Сообщение",
    contactConsent: "Разрешаю Arena Sold Out передать моё имя, e-mail и сообщение организатору, чтобы он мог мне ответить.",
    contactSend: "Отправить",
    contactCancel: "Отмена",
    contactSending: "Отправляем…",
    contactSent: "Спасибо. Ваше сообщение отправлено организатору. Он ответит вам по e-mail.",
    contactClose: "Закрыть",
    contactErrName: "Укажите имя, до 100 символов.",
    contactErrEmail: "Укажите e-mail, чтобы организатор мог ответить. Например, anna@example.com.",
    contactErrMessage: "Напишите сообщение от 10 до 2000 символов.",
    contactErrConsent: "Отметьте согласие, чтобы мы передали сообщение организатору.",
    contactErrCaptcha: "Пройдите проверку «не робот» и отправьте сообщение ещё раз.",
    contactErrRate: "Отсюда отправлено слишком много сообщений. Попробуйте через час.",
    contactErrGeneric: "Не удалось отправить сообщение. Попробуйте через минуту.",
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
    contactButton: "Napsat pořadateli",
    contactTitle: "Napsat pořadateli",
    contactIntro: "Pořadatel vám odpoví e-mailem.",
    contactName: "Vaše jméno",
    contactEmail: "Váš e-mail",
    contactMessage: "Zpráva",
    contactConsent: "Souhlasím, že Arena Sold Out předá mé jméno, e-mail a zprávu pořadateli, aby mi mohl odpovědět.",
    contactSend: "Odeslat",
    contactCancel: "Zrušit",
    contactSending: "Odesíláme…",
    contactSent: "Děkujeme. Vaše zpráva míří k pořadateli. Odpoví vám e-mailem.",
    contactClose: "Zavřít",
    contactErrName: "Zadejte jméno, nejvýše 100 znaků.",
    contactErrEmail: "Zadejte e-mail, aby vám pořadatel mohl odpovědět, například anna@example.com.",
    contactErrMessage: "Napište zprávu o délce 10 až 2000 znaků.",
    contactErrConsent: "Zaškrtněte souhlas, abychom zprávu předali pořadateli.",
    contactErrCaptcha: "Dokončete ověření, že nejste robot, a zprávu odešlete znovu.",
    contactErrRate: "Odsud bylo odesláno příliš mnoho zpráv. Zkuste to za hodinu.",
    contactErrGeneric: "Zprávu se nepodařilo odeslat. Zkuste to za minutu.",
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
    contactButton: "כתבו למארגן",
    contactTitle: "כתבו למארגן",
    contactIntro: "המארגן יענה לכם במייל.",
    contactName: "השם שלכם",
    contactEmail: "המייל שלכם",
    contactMessage: "הודעה",
    contactConsent: "אני מסכים/ה ש-Arena Sold Out תעביר את שמי, המייל וההודעה שלי למארגן כדי שיוכל לענות לי.",
    contactSend: "שליחה",
    contactCancel: "ביטול",
    contactSending: "שולחים…",
    contactSent: "תודה. ההודעה שלכם בדרך למארגן. הוא יענה לכם במייל.",
    contactClose: "סגירה",
    contactErrName: "הזינו שם, עד 100 תווים.",
    contactErrEmail: "הזינו מייל כדי שהמארגן יוכל לענות, למשל anna@example.com.",
    contactErrMessage: "כתבו הודעה באורך 10 עד 2000 תווים.",
    contactErrConsent: "סמנו את תיבת ההסכמה כדי שנעביר את ההודעה למארגן.",
    contactErrCaptcha: "השלימו את בדיקת האנטי-רובוט ושלחו שוב.",
    contactErrRate: "נשלחו מכאן יותר מדי הודעות. נסו שוב בעוד שעה.",
    contactErrGeneric: "לא הצלחנו לשלוח את ההודעה. נסו שוב בעוד דקה.",
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
    contactButton: "Escribir al organizador",
    contactTitle: "Escribir al organizador",
    contactIntro: "El organizador te responderá por correo.",
    contactName: "Tu nombre",
    contactEmail: "Tu correo",
    contactMessage: "Mensaje",
    contactConsent: "Acepto que Arena Sold Out pase mi nombre, correo y mensaje al organizador para que pueda responderme.",
    contactSend: "Enviar",
    contactCancel: "Cancelar",
    contactSending: "Enviando…",
    contactSent: "Gracias. Tu mensaje va de camino al organizador. Te responderá por correo.",
    contactClose: "Cerrar",
    contactErrName: "Escribe tu nombre, hasta 100 caracteres.",
    contactErrEmail: "Escribe tu correo para que el organizador pueda responderte, por ejemplo anna@example.com.",
    contactErrMessage: "Escribe un mensaje de 10 a 2000 caracteres.",
    contactErrConsent: "Marca la casilla para aceptar que pasemos tu mensaje al organizador.",
    contactErrCaptcha: "Completa la verificación anti-robot y vuelve a enviar el mensaje.",
    contactErrRate: "Se han enviado demasiados mensajes desde aquí. Inténtalo dentro de una hora.",
    contactErrGeneric: "No pudimos enviar tu mensaje. Inténtalo dentro de un minuto.",
  },
};

export function t(locale: PageLocale): PageStrings {
  return STRINGS[locale];
}
