/**
 * holdI18n.ts — the words of the seat hold on click (spec 67 §8.2).
 *
 * Glossary: hold, seat, seating plan. Looked up by language subtag like the
 * cart-limit words (lib/cartLimits.ts): "es-ES" reads as es, an unknown
 * language reads English. The cs, he, es, de and fr texts are to be checked by
 * a native speaker or a separate agent before merge (spec 67 §8.2).
 */

export interface HoldStrings {
  /** The countdown. {time} is M:SS. */
  hold_countdown: string;
  /** The extend button. {min} is the extension in minutes. */
  hold_extend: string;
  /** After an extension. {min} is the extension in minutes. */
  hold_extended: string;
  /** The hold ended. */
  hold_expired: string;
  /** The button under it. */
  hold_rebook: string;
  /** Some seats were taken meanwhile. {seats} is the list. */
  hold_rebook_partial: string;
  /** The seat was taken, free seats of the same price are lit. */
  seat_taken_near: string;
  /** The seat was taken, there is no free seat of the same price nearby. */
  seat_taken_none: string;
  /** hold.too_many_seats. {n} is the cap. */
  hold_too_many_seats: string;
  /** hold.too_many_active. */
  hold_too_many_active: string;
  /** hold.rehold_paused. {min} is the wait in minutes. */
  hold_paused: string;
  /** Payment is going on. */
  hold_paying: string;
  /** Anything else that went wrong with a click. */
  hold_failed: string;
  /** hold.in_payment. */
  hold_in_payment: string;
}

export type HoldKey = keyof HoldStrings;

export const HOLD_KEYS: readonly HoldKey[] = [
  'hold_countdown',
  'hold_extend',
  'hold_extended',
  'hold_expired',
  'hold_rebook',
  'hold_rebook_partial',
  'seat_taken_near',
  'seat_taken_none',
  'hold_too_many_seats',
  'hold_too_many_active',
  'hold_paused',
  'hold_paying',
  'hold_failed',
  'hold_in_payment',
] as const;

export const HOLD_I18N: Record<string, HoldStrings> = {
  en: {
    hold_countdown: 'Seats held for you: {time}',
    hold_extend: 'Add {min} min',
    hold_extended: 'We added {min} min.',
    hold_expired: 'Your hold has ended.',
    hold_rebook: 'Hold the seats again',
    hold_rebook_partial: 'Some seats are taken now: {seats}. The other seats are held for you again.',
    seat_taken_near: 'Someone has just taken this seat. Free seats at the same price nearby are highlighted.',
    seat_taken_none: 'Someone has just taken this seat. There are no free seats at the same price nearby.',
    hold_too_many_seats: 'One hold can have up to {n} seats.',
    hold_too_many_active: 'You already have open holds. Pay for one or cancel it.',
    hold_paused: 'You can select these seats again in {min} min. Select other seats.',
    hold_paying: 'Payment in progress. Your seats are held until the bank answers.',
    hold_failed: 'We could not select the seat. Try again.',
    hold_in_payment: 'To change seats, go back to the seating plan.',
  },
  ru: {
    hold_countdown: 'Места за вами: {time}',
    hold_extend: 'Продлить на {min} мин',
    hold_extended: 'Добавили {min} мин.',
    hold_expired: 'Время брони вышло.',
    hold_rebook: 'Забронировать снова',
    hold_rebook_partial: 'Часть мест уже заняли: {seats}. Остальные снова за вами.',
    seat_taken_near: 'Это место только что заняли. Свободные места той же цены рядом подсвечены.',
    seat_taken_none: 'Это место только что заняли. Рядом нет свободных мест той же цены.',
    hold_too_many_seats: 'В одной брони может быть до {n} мест.',
    hold_too_many_active: 'У вас уже есть открытые брони. Оплатите или отмените одну из них.',
    hold_paused: 'Эти места можно выбрать снова через {min} мин. Выберите другие места.',
    hold_paying: 'Идёт оплата. Места за вами, пока банк не ответит.',
    hold_failed: 'Не удалось выбрать место. Попробуйте ещё раз.',
    hold_in_payment: 'Чтобы изменить места, вернитесь к выбору мест.',
  },
  cs: {
    hold_countdown: 'Místa držíme pro vás: {time}',
    hold_extend: 'Prodloužit o {min} min',
    hold_extended: 'Přidali jsme {min} min.',
    hold_expired: 'Rezervace vypršela.',
    hold_rebook: 'Rezervovat místa znovu',
    hold_rebook_partial: 'Některá místa už jsou obsazená: {seats}. Ostatní místa jsou pro vás znovu rezervována.',
    seat_taken_near: 'Toto místo právě někdo obsadil. Volná místa za stejnou cenu v okolí jsou zvýrazněná.',
    seat_taken_none: 'Toto místo právě někdo obsadil. V okolí nejsou žádná volná místa za stejnou cenu.',
    hold_too_many_seats: 'Jedna rezervace může mít nejvýše {n} míst.',
    hold_too_many_active: 'Už máte otevřené rezervace. Jednu z nich zaplaťte nebo zrušte.',
    hold_paused: 'Tato místa můžete vybrat znovu za {min} min. Vyberte jiná místa.',
    hold_paying: 'Probíhá platba. Místa jsou pro vás rezervována, dokud neodpoví banka.',
    hold_failed: 'Místo se nepodařilo vybrat. Zkuste to znovu.',
    hold_in_payment: 'Chcete-li změnit místa, vraťte se k plánu sálu.',
  },
  he: {
    hold_countdown: 'המקומות שמורים עבורכם: {time}',
    hold_extend: 'הוסיפו {min} דקות',
    hold_extended: 'הוספנו {min} דקות.',
    hold_expired: 'זמן השמירה הסתיים.',
    hold_rebook: 'לשמור את המקומות שוב',
    hold_rebook_partial: 'חלק מהמקומות נתפסו: {seats}. שאר המקומות שמורים עבורכם שוב.',
    seat_taken_near: 'המקום הזה נתפס זה עתה. מקומות פנויים באותו מחיר בקרבת מקום מודגשים.',
    seat_taken_none: 'המקום הזה נתפס זה עתה. אין בקרבת מקום מקומות פנויים באותו מחיר.',
    hold_too_many_seats: 'אפשר לשמור עד {n} מקומות בבת אחת.',
    hold_too_many_active: 'כבר יש לכם שמירות פתוחות. שלמו עבור אחת מהן או בטלו אותה.',
    hold_paused: 'אפשר לבחור את המקומות האלה שוב בעוד {min} דקות. בחרו מקומות אחרים.',
    hold_paying: 'התשלום מתבצע. המקומות שמורים עבורכם עד שהבנק יענה.',
    hold_failed: 'לא הצלחנו לבחור את המקום. נסו שוב.',
    hold_in_payment: 'כדי לשנות מקומות, חזרו לתרשים הישיבה.',
  },
  es: {
    hold_countdown: 'Asientos reservados para ti: {time}',
    hold_extend: 'Añadir {min} min',
    hold_extended: 'Hemos añadido {min} min.',
    hold_expired: 'Tu reserva ha terminado.',
    hold_rebook: 'Reservar los asientos de nuevo',
    hold_rebook_partial: 'Algunos asientos ya están ocupados: {seats}. Los demás vuelven a estar reservados para ti.',
    seat_taken_near: 'Alguien acaba de ocupar este asiento. Los asientos libres del mismo precio cercanos están resaltados.',
    seat_taken_none: 'Alguien acaba de ocupar este asiento. No hay asientos libres del mismo precio cerca.',
    hold_too_many_seats: 'Una reserva puede tener hasta {n} asientos.',
    hold_too_many_active: 'Ya tienes reservas abiertas. Paga una de ellas o cancélala.',
    hold_paused: 'Podrás elegir estos asientos de nuevo dentro de {min} min. Elige otros asientos.',
    hold_paying: 'Pago en curso. Tus asientos quedan reservados hasta que responda el banco.',
    hold_failed: 'No hemos podido elegir el asiento. Inténtalo de nuevo.',
    hold_in_payment: 'Para cambiar los asientos, vuelve al plano de la sala.',
  },
  de: {
    hold_countdown: 'Plätze für Sie reserviert: {time}',
    hold_extend: '{min} Min. verlängern',
    hold_extended: 'Wir haben {min} Min. hinzugefügt.',
    hold_expired: 'Ihre Reservierung ist abgelaufen.',
    hold_rebook: 'Plätze erneut reservieren',
    hold_rebook_partial: 'Einige Plätze sind inzwischen vergeben: {seats}. Die übrigen Plätze sind wieder für Sie reserviert.',
    seat_taken_near: 'Dieser Platz wurde gerade vergeben. Freie Plätze zum gleichen Preis in der Nähe sind hervorgehoben.',
    seat_taken_none: 'Dieser Platz wurde gerade vergeben. In der Nähe gibt es keine freien Plätze zum gleichen Preis.',
    hold_too_many_seats: 'Eine Reservierung kann bis zu {n} Plätze umfassen.',
    hold_too_many_active: 'Sie haben bereits offene Reservierungen. Bezahlen oder stornieren Sie eine davon.',
    hold_paused: 'Diese Plätze können Sie in {min} Min. wieder wählen. Wählen Sie andere Plätze.',
    hold_paying: 'Die Zahlung läuft. Ihre Plätze bleiben reserviert, bis die Bank antwortet.',
    hold_failed: 'Der Platz konnte nicht gewählt werden. Versuchen Sie es erneut.',
    hold_in_payment: 'Um Plätze zu ändern, gehen Sie zurück zum Saalplan.',
  },
  fr: {
    hold_countdown: 'Places réservées pour vous : {time}',
    hold_extend: 'Ajouter {min} min',
    hold_extended: 'Nous avons ajouté {min} min.',
    hold_expired: 'Votre réservation est terminée.',
    hold_rebook: 'Réserver à nouveau les places',
    hold_rebook_partial: 'Certaines places sont désormais prises : {seats}. Les autres places sont de nouveau réservées pour vous.',
    seat_taken_near: "Quelqu'un vient de prendre cette place. Les places libres au même prix à proximité sont mises en évidence.",
    seat_taken_none: "Quelqu'un vient de prendre cette place. Il n'y a pas de place libre au même prix à proximité.",
    hold_too_many_seats: "Une réservation peut compter jusqu'à {n} places.",
    hold_too_many_active: 'Vous avez déjà des réservations ouvertes. Payez-en une ou annulez-la.',
    hold_paused: "Vous pourrez choisir ces places à nouveau dans {min} min. Choisissez d'autres places.",
    hold_paying: "Paiement en cours. Vos places sont réservées jusqu'à la réponse de la banque.",
    hold_failed: 'Impossible de choisir la place. Réessayez.',
    hold_in_payment: 'Pour modifier les places, revenez au plan de salle.',
  },
};

/** The hold words for a locale ("es-ES" -> es), English when the language has none. */
export function getHoldStrings(locale: string): HoldStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return HOLD_I18N[lang] ?? HOLD_I18N.en!;
}

/** Fills {name} placeholders. */
export function fillHold(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => (name in values ? String(values[name]) : whole));
}

/** The countdown line. */
export function holdCountdownText(locale: string, time: string): string {
  return fillHold(getHoldStrings(locale).hold_countdown, { time });
}

/**
 * The buyer's text for a refusal of a hold call, by its `hold.*` code, or null
 * when the code is not a hold code the widget words (a caller falls back to
 * `hold_failed`). `details` may carry the cap (`max_seats`, `max`, `limit`) or
 * the pause (`retry_after_seconds`).
 */
export function holdErrorText(code: string | undefined, locale: string, details: Record<string, unknown> = {}): string {
  const s = getHoldStrings(locale);
  const num = (...names: string[]): number | null => {
    for (const n of names) {
      const v = details[n];
      if (typeof v === 'number' && Number.isFinite(v) && v > 0) return v;
    }
    return null;
  };
  switch (code) {
    case 'hold.too_many_seats':
      return fillHold(s.hold_too_many_seats, { n: num('max_seats', 'max', 'limit') ?? 10 });
    case 'hold.too_many_active':
      return s.hold_too_many_active;
    case 'hold.rehold_paused': {
      const sec = num('retry_after_seconds');
      return fillHold(s.hold_paused, { min: sec ? Math.max(1, Math.ceil(sec / 60)) : 5 });
    }
    case 'hold.in_payment':
      return s.hold_in_payment;
    case 'hold.expired':
      return s.hold_expired;
    default:
      return s.hold_failed;
  }
}
