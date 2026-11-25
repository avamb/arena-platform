/**
 * Strings of the "Seat hold" screen and of the "Hold mode" block of an event (spec 67, section 7.2).
 * Spread into ruCatalog and enCatalog (catalog.ts); the catalog test keeps the two key sets equal.
 * Profile names (standard, high_demand) are technical identifiers and stay as the API sends them.
 */

export const holdPolicyRu: Readonly<Record<string, string>> = {
  "nav.hold_policy": "Бронь мест (Seat hold)",

  "holdPolicy.title": "Бронь мест",
  "holdPolicy.subtitle":
    "Сколько времени место остаётся за покупателем и какие есть лимиты. Правка действует на новые брони. Брони, которые уже есть, не меняются.",
  "holdPolicy.loading": "Загружаем настройки…",
  "holdPolicy.loadError": "Не удалось загрузить настройки: {message}. Обновите страницу.",
  "holdPolicy.profile.standard": "Обычный режим (standard)",
  "holdPolicy.profile.high_demand": "Повышенный спрос (high_demand)",
  "holdPolicy.profileHint":
    "Режим «повышенный спрос» включают для мероприятий, где места уходят за минуты. Он выбирается в карточке мероприятия.",
  "holdPolicy.updatedAt": "Изменено: {at}",

  "holdPolicy.field.select_minutes": "Выбор мест, минут",
  "holdPolicy.field.payment_minutes": "Оплата, минут",
  "holdPolicy.field.extend_minutes": "Одно продление, минут",
  "holdPolicy.field.extend_count": "Сколько раз можно продлить",
  "holdPolicy.field.paying_minutes": "Пока идёт оплата, минут",
  "holdPolicy.field.max_seats_per_hold": "Мест в одной брони, не больше",
  "holdPolicy.field.max_active_per_device": "Активных броней с одного устройства",
  "holdPolicy.field.max_active_per_ip": "Активных броней с одного IP-адреса",
  "holdPolicy.field.rehold_pause_minutes": "Пауза после неоплаченной брони, минут",
  "holdPolicy.field.poll_interval_seconds": "Как часто виджет обновляет схему, секунд",

  "holdPolicy.reason": "Причина изменения",
  "holdPolicy.reasonHint": "Латиницей, по-английски. Её увидят в журнале аудита.",
  "holdPolicy.save": "Сохранить",
  "holdPolicy.saving": "Сохраняем…",
  "holdPolicy.saved": "Сохранено. Новые настройки действуют на новые брони.",
  "holdPolicy.saveError": "Не сохранено: {message}",

  "holdPolicy.err.reason": "Напишите причину изменения.",
  "holdPolicy.err.number": "Введите число, например 10 или 2.5.",
  "holdPolicy.err.integer": "Введите целое число.",
  "holdPolicy.err.range": "Введите число от {min} до {max}.",
  "holdPolicy.err.wholeSeconds": "Время должно быть кратно одной секунде.",
  "holdPolicy.err.wholeMillis": "Нужна точность до одной миллисекунды.",

  "holdPolicy.event.label": "Режим брони",
  "holdPolicy.event.hint":
    "Текущий режим здесь не показывается. Выберите режим и нажмите «Применить».",
  "holdPolicy.event.none": "Не менять",
  "holdPolicy.event.standard": "Обычный (standard)",
  "holdPolicy.event.high_demand": "Повышенный спрос (high_demand)",
  "holdPolicy.event.reset": "Вернуть обычный режим",
  "holdPolicy.event.reason": "Причина",
  "holdPolicy.event.apply": "Применить",
  "holdPolicy.event.applying": "Применяем…",
  "holdPolicy.event.done": "Режим брони изменён.",
  "holdPolicy.event.error": "Не изменено: {message}",
  "holdPolicy.event.needReason": "Напишите причину изменения.",
  "holdPolicy.event.needChoice": "Выберите режим.",
};

export const holdPolicyEn: Readonly<Record<string, string>> = {
  "nav.hold_policy": "Seat hold",

  "holdPolicy.title": "Seat hold",
  "holdPolicy.subtitle":
    "How long a seat stays with the buyer and which limits apply. A change applies to new holds. Holds that already exist do not change.",
  "holdPolicy.loading": "Loading the settings…",
  "holdPolicy.loadError": "The settings did not load: {message}. Refresh the page.",
  "holdPolicy.profile.standard": "Standard mode (standard)",
  "holdPolicy.profile.high_demand": "High demand (high_demand)",
  "holdPolicy.profileHint":
    "Use the high demand mode for events where the seats go in minutes. You select it on the event card.",
  "holdPolicy.updatedAt": "Changed: {at}",

  "holdPolicy.field.select_minutes": "Seat selection, minutes",
  "holdPolicy.field.payment_minutes": "Payment, minutes",
  "holdPolicy.field.extend_minutes": "One extension, minutes",
  "holdPolicy.field.extend_count": "How many times the buyer can extend",
  "holdPolicy.field.paying_minutes": "While a payment is in progress, minutes",
  "holdPolicy.field.max_seats_per_hold": "Seats in one hold, at most",
  "holdPolicy.field.max_active_per_device": "Active holds from one device",
  "holdPolicy.field.max_active_per_ip": "Active holds from one IP address",
  "holdPolicy.field.rehold_pause_minutes": "Pause after an unpaid hold, minutes",
  "holdPolicy.field.poll_interval_seconds": "How often the widget refreshes the map, seconds",

  "holdPolicy.reason": "Reason for the change",
  "holdPolicy.reasonHint": "In English. It goes to the audit log.",
  "holdPolicy.save": "Save",
  "holdPolicy.saving": "Saving…",
  "holdPolicy.saved": "Saved. The new settings apply to new holds.",
  "holdPolicy.saveError": "Not saved: {message}",

  "holdPolicy.err.reason": "Write the reason for the change.",
  "holdPolicy.err.number": "Enter a number, for example 10 or 2.5.",
  "holdPolicy.err.integer": "Enter a whole number.",
  "holdPolicy.err.range": "Enter a number from {min} to {max}.",
  "holdPolicy.err.wholeSeconds": "The time must be a whole number of seconds.",
  "holdPolicy.err.wholeMillis": "Use a precision of one millisecond at most.",

  "holdPolicy.event.label": "Hold mode",
  "holdPolicy.event.hint":
    "The current mode is not shown here. Select a mode and click Apply.",
  "holdPolicy.event.none": "Do not change",
  "holdPolicy.event.standard": "Standard (standard)",
  "holdPolicy.event.high_demand": "High demand (high_demand)",
  "holdPolicy.event.reset": "Return to standard mode",
  "holdPolicy.event.reason": "Reason",
  "holdPolicy.event.apply": "Apply",
  "holdPolicy.event.applying": "Applying…",
  "holdPolicy.event.done": "The hold mode is changed.",
  "holdPolicy.event.error": "Not changed: {message}",
  "holdPolicy.event.needReason": "Write the reason for the change.",
  "holdPolicy.event.needChoice": "Select a mode.",
};
