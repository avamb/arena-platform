// @vitest-environment jsdom
/**
 * RT-12b (spec 44): the editor page of the Telegram mini app, run for real in jsdom with the built library.
 * Covers the description and the letter text (prefilled, saved with ONE call, the window closed after a save),
 * a save outside Telegram ("Saved" and the link to the event), a refused save, the event of another
 * organization and a person without the right to edit (the same calm line), and the Telegram back button.
 * The clicks against a real API are the twin test (tests/smoke/live/tg-edit.spec.ts).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/endpoints")>()),
  listEvents: vi.fn(),
  fetchOrgMe: vi.fn(),
  saveDescription: vi.fn(),
  saveEmailNote: vi.fn(),
  sendEmailNoteTest: vi.fn(),
  formatText: vi.fn(),
}));

import { fetchOrgMe, listEvents, saveDescription, saveEmailNote, sendEmailNoteTest } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import { I18nProvider, type Locale } from "@/lib/i18n";
import { resetTelegramScript } from "@/lib/tgEdit";
import { TgEditView, type TgEditState } from "@/screens/TgEditScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const EVENT = "22222222-2222-4222-8222-222222222222";
const roots: Root[] = [];
let client: QueryClient;

const DESCRIPTION = "<p>An evening of <strong>jazz</strong>.</p>";
const NOTE = "<p>Bring a <strong>warm</strong> jacket.</p>";

function eventItem(over: Record<string, unknown> = {}) {
  return {
    id: EVENT,
    org_id: ORG,
    name: "Jazz night",
    description: DESCRIPTION,
    description_format: "art",
    email_note: NOTE,
    email_note_format: "art",
    ...over,
  };
}

function allow(permissions: string[] = ["event.update", "event.read"]) {
  vi.mocked(fetchOrgMe).mockResolvedValue({ permissions } as never);
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  for (const fn of [listEvents, fetchOrgMe, saveDescription, saveEmailNote, sendEmailNoteTest]) vi.mocked(fn).mockReset();
  vi.mocked(listEvents).mockResolvedValue({ events: [eventItem()] } as never);
  allow();
  delete (window as { Telegram?: unknown }).Telegram;
  resetTelegramScript();
});

afterEach(() => {
  while (roots.length > 0) {
    const r = roots.pop() as Root;
    act(() => r.unmount());
  }
  document.body.innerHTML = "";
  delete (window as { Telegram?: unknown }).Telegram;
  resetTelegramScript();
  document.head.querySelectorAll("script").forEach((s) => s.remove());
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function mount(state: TgEditState, onClose: () => void = () => {}, locale: Locale = "en"): HTMLElement {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  const ui: ReactElement = (
    <QueryClientProvider client={client}>
      <I18nProvider locale={locale}>
        <TgEditView state={state} onClose={onClose} />
      </I18nProvider>
    </QueryClientProvider>
  );
  act(() => root.render(ui));
  return el;
}

async function until(check: () => boolean, what: string) {
  for (let i = 0; i < 300; i++) {
    if (check()) return;
    await act(async () => {
      await sleep(10);
    });
  }
  throw new Error("timed out: " + what);
}

/** The text area of the editor, once the library has loaded. */
async function editor(el: HTMLElement): Promise<HTMLElement> {
  await until(() => el.querySelector(".ProseMirror") !== null, "the editor loads");
  return el.querySelector(".ProseMirror") as HTMLElement;
}

async function press(button: Element | null) {
  expect(button).not.toBeNull();
  await act(async () => {
    (button as HTMLElement).click();
    await sleep(0);
  });
}

const bullets = (el: HTMLElement) => el.querySelector('button[data-action="bulletList"]');
const ready = (kind: "description" | "email_note"): TgEditState => ({ phase: "ready", kind, orgId: ORG, eventId: EVENT });

/** A stand-in for the Telegram script: close and the back button. */
function telegram() {
  const handlers: Array<() => void> = [];
  const app = {
    close: vi.fn(),
    enableClosingConfirmation: vi.fn(),
    disableClosingConfirmation: vi.fn(),
    BackButton: {
      show: vi.fn(),
      hide: vi.fn(),
      onClick: vi.fn((h: () => void) => handlers.push(h)),
      offClick: vi.fn((h: () => void) => {
        handlers.splice(handlers.indexOf(h), 1);
      }),
    },
  };
  (window as unknown as { Telegram: { WebApp: typeof app } }).Telegram = { WebApp: app };
  return { app, handlers };
}

describe("the description in the mini app", () => {
  it("opens with the saved description in the editor and the name of the event, and nothing to save yet", async () => {
    const el = mount(ready("description"));
    const box = await editor(el);
    expect(box.querySelector("strong")?.textContent).toBe("jazz");
    expect(el.querySelector('[data-testid="tg-edit-event"]')?.textContent).toBe("Event: Jazz night");
    expect((el.querySelector('[data-testid="tg-description-save"]') as HTMLButtonElement).disabled).toBe(true);
    expect(el.querySelector('[data-testid="tg-edit-saved"]')).toBeNull();
  });

  it("reads a description kept in the old notation through the converter, never with its marks", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [eventItem({ description: "Hello **world**\n\n- one\n- two", description_format: "text" })] } as never);
    const el = mount(ready("description"));
    const box = await editor(el);
    expect(box.querySelector("strong")?.textContent).toBe("world");
    expect(box.querySelectorAll("li")).toHaveLength(2);
    expect(box.textContent).not.toContain("**");
  });

  it("saves ART with ONE call that names the event and nothing else, then closes the Telegram window", async () => {
    const { app } = telegram();
    vi.mocked(saveDescription).mockResolvedValue({} as never);
    const el = mount(ready("description"));
    await editor(el);
    await press(bullets(el));
    expect(el.querySelector('[data-testid="tg-description-unsaved"]')).not.toBeNull();
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(saveDescription).toHaveBeenCalledTimes(1);
    expect(saveDescription).toHaveBeenCalledWith(ORG, EVENT, "<ul><li>An evening of <strong>jazz</strong>.</li></ul>");
    expect(app.close).toHaveBeenCalledTimes(1);
    // The window closes itself, so the page does not claim anything else.
    expect(el.querySelector('[data-testid="tg-edit-saved"]')).toBeNull();
    // The list is read again after the save (the web card and the bot read the same list).
    expect(listEvents).toHaveBeenCalledTimes(2);
  });

  it("outside Telegram says Saved and links to the event in the web event center", async () => {
    vi.mocked(saveDescription).mockResolvedValue({} as never);
    const el = mount(ready("description"), () => {}, "ru");
    await editor(el);
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    const done = el.querySelector('[data-testid="tg-edit-saved"]');
    expect(done?.textContent).toContain("Сохранено.");
    const link = el.querySelector<HTMLAnchorElement>('[data-testid="tg-edit-open-event"]');
    expect(link?.getAttribute("href")).toBe(`/o/${ORG}/events/${EVENT}`);
  });

  it("keeps the window open and says what to do when the save is refused", async () => {
    const { app } = telegram();
    vi.mocked(saveDescription).mockRejectedValue(new ApiError(422, "event.invalid_description", "no", undefined, { reason: "too_long" }));
    const el = mount(ready("description"));
    await editor(el);
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(app.close).not.toHaveBeenCalled();
    expect(el.querySelector('[data-testid="tg-description-error"]')).not.toBeNull();
    // The draft is still there and can be saved again.
    expect((el.querySelector('[data-testid="tg-description-save"]') as HTMLButtonElement).disabled).toBe(false);
  });

  it("refuses an over-long description on the screen and sends nothing", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [eventItem({ description: "<p>" + "x".repeat(4001) + "</p>" })] } as never);
    const el = mount(ready("description"));
    await editor(el);
    await press(bullets(el));
    expect(el.querySelector('[data-testid="tg-description-field-error"]')?.textContent).toContain("4000");
    expect((el.querySelector('[data-testid="tg-description-save"]') as HTMLButtonElement).disabled).toBe(true);
    expect(saveDescription).not.toHaveBeenCalled();
  });
});

describe("the letter text in the mini app", () => {
  it("opens with the saved text, saves it with the same call as the web card and closes the window", async () => {
    const { app } = telegram();
    vi.mocked(saveEmailNote).mockResolvedValue({} as never);
    const el = mount(ready("email_note"));
    const box = await editor(el);
    expect(box.querySelector("strong")?.textContent).toBe("warm");
    expect(el.querySelector('[data-testid="email-note"]')).not.toBeNull();
    await press(bullets(el));
    await press(el.querySelector('[data-testid="email-note-save"]'));
    expect(saveEmailNote).toHaveBeenCalledTimes(1);
    expect(saveEmailNote).toHaveBeenCalledWith(ORG, EVENT, "<ul><li>Bring a <strong>warm</strong> jacket.</li></ul>");
    expect(app.close).toHaveBeenCalledTimes(1);
  });

  it("sends the test letter with the draft and does NOT close the window", async () => {
    const { app } = telegram();
    vi.mocked(sendEmailNoteTest).mockResolvedValue({ queued: true, to: "me@example.test" });
    const el = mount(ready("email_note"));
    await editor(el);
    await press(el.querySelector('[data-testid="email-note-test"]'));
    expect(sendEmailNoteTest).toHaveBeenCalledWith(ORG, EVENT, { note: NOTE, note_format: "art", locale: "en" });
    expect(app.close).not.toHaveBeenCalled();
  });
});

describe("an event the person may not edit", () => {
  it("says there is no access when the organization's list does not hold the event, and shows no editor", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [eventItem({ id: "33333333-3333-4333-8333-333333333333" })] } as never);
    const el = mount(ready("description"), () => {}, "ru");
    await until(() => el.querySelector('[data-testid="tg-edit-no-access"]') !== null, "the no access line");
    expect(el.textContent).toContain("Нет доступа к этому мероприятию");
    expect(el.querySelector(".ProseMirror")).toBeNull();
    expect(el.querySelector('[data-testid="tg-edit-close"]')).not.toBeNull();
    expect(saveDescription).not.toHaveBeenCalled();
  });

  it("says the same when the list is refused", async () => {
    vi.mocked(listEvents).mockRejectedValue(new ApiError(403, "org.forbidden", "no"));
    const el = mount(ready("email_note"));
    await until(() => el.querySelector('[data-testid="tg-edit-no-access"]') !== null, "the no access line");
    expect(el.querySelector(".ProseMirror")).toBeNull();
  });

  it("says the same to a person without the right to edit, even if the event is in the list", async () => {
    allow(["event.read"]);
    const el = mount(ready("description"));
    await until(() => el.querySelector('[data-testid="tg-edit-no-access"]') !== null, "the no access line");
    expect(el.querySelector(".ProseMirror")).toBeNull();
  });
});

/** Lets the promises of the page (the script load, the effects after it) run. */
async function settle() {
  await act(async () => {
    await sleep(0);
  });
}

describe("the Telegram back button", () => {
  it("is shown while the page is open, closes the page when pressed and is taken away afterwards", async () => {
    const { app, handlers } = telegram();
    const onClose = vi.fn();
    mount({ phase: "noAccess" }, onClose);
    await settle();
    expect(app.BackButton.show).toHaveBeenCalledTimes(1);
    expect(handlers).toHaveLength(1);
    handlers[0]();
    expect(onClose).toHaveBeenCalledTimes(1);
    act(() => roots.pop()?.unmount());
    expect(app.BackButton.hide).toHaveBeenCalledTimes(1);
    expect(handlers).toHaveLength(0);
  });

  it("is bound only once the Telegram script has loaded, not before it (the page is drawn first)", async () => {
    const onClose = vi.fn();
    mount({ phase: "noAccess" }, onClose);
    await settle();
    // The script was asked for and has not arrived: no Telegram object, so nothing to bind to.
    const script = document.head.querySelector<HTMLScriptElement>("script[src*='telegram-web-app']");
    expect(script).not.toBeNull();
    // The stand-in appears the way the real script does: when the file has run.
    const { app, handlers } = telegram();
    expect(app.BackButton.onClick).not.toHaveBeenCalled();
    await act(async () => {
      script?.onload?.(new Event("load"));
      await sleep(0);
    });
    expect(app.BackButton.show).toHaveBeenCalledTimes(1);
    expect(handlers).toHaveLength(1);
    handlers[0]();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not leave a button behind when the page is closed before the script arrives", async () => {
    mount({ phase: "noAccess" });
    await settle();
    const script = document.head.querySelector<HTMLScriptElement>("script[src*='telegram-web-app']");
    act(() => roots.pop()?.unmount());
    const { app, handlers } = telegram();
    await act(async () => {
      script?.onload?.(new Event("load"));
      await sleep(0);
    });
    expect(app.BackButton.onClick).not.toHaveBeenCalled();
    expect(handlers).toHaveLength(0);
  });

  it("is not needed outside Telegram: the close button is on the page", () => {
    const el = mount({ phase: "noAccess" });
    expect(el.querySelector('[data-testid="tg-edit-close"]')).not.toBeNull();
  });
});

describe("the window while there is something to lose", () => {
  it("asks Telegram to confirm a close while the text is not saved, and stops asking once it is saved", async () => {
    const { app } = telegram();
    vi.mocked(saveDescription).mockResolvedValue({} as never);
    const el = mount(ready("description"));
    await editor(el);
    await settle();
    expect(app.enableClosingConfirmation).not.toHaveBeenCalled();
    await press(bullets(el));
    await settle();
    expect(app.enableClosingConfirmation).toHaveBeenCalledTimes(1);
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(app.disableClosingConfirmation).toHaveBeenCalled();
    expect(app.close).toHaveBeenCalledTimes(1);
  });

  it("does not close the page, by the back button or by the close button, while a save is on its way", async () => {
    const { app, handlers } = telegram();
    let finish: (v: never) => void = () => undefined;
    vi.mocked(saveDescription).mockReturnValue(new Promise((res) => (finish = res as typeof finish)));
    const onClose = vi.fn();
    const el = mount(ready("description"), onClose);
    await editor(el);
    await settle();
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    await settle();
    expect((el.querySelector('[data-testid="tg-edit-close"]') as HTMLButtonElement).disabled).toBe(true);
    handlers[0]();
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => {
      finish({} as never);
      await sleep(0);
    });
    expect(app.close).toHaveBeenCalledTimes(1);
  });
});

describe("the canonical description, not the translation", () => {
  const CANON = "<p>The <strong>original</strong> text.</p>";
  const RU = "<p>Русский перевод.</p>";

  function translated(over: Record<string, unknown> = {}) {
    return eventItem({ description: RU, description_base: CANON, description_translated: true, ...over });
  }

  it("opens with the canonical text and writes the canonical text back, never the translation", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [translated()] } as never);
    vi.mocked(saveDescription).mockResolvedValue({} as never);
    const el = mount(ready("description"));
    const box = await editor(el);
    expect(box.textContent).toContain("original");
    expect(box.textContent).not.toContain("Русский");
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(saveDescription).toHaveBeenCalledTimes(1);
    const sentArt = vi.mocked(saveDescription).mock.calls[0][2];
    expect(sentArt).toContain("original");
    expect(sentArt).not.toContain("Русский");
  });

  it("an event with no canonical text opens an empty editor, never the translation", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [translated({ description_base: null })] } as never);
    const el = mount(ready("description"));
    const box = await editor(el);
    expect(box.textContent).not.toContain("Русский");
  });

  it("asks for the list without a language, so it is the same list in every language", async () => {
    mount(ready("description"));
    await settle();
    expect(listEvents).toHaveBeenCalled();
    for (const call of vi.mocked(listEvents).mock.calls) expect(call).toEqual([ORG]);
  });

  it("keeps the draft, the editor and the list when the language of the page changes", async () => {
    const el = mount(ready("description"));
    await editor(el);
    const section = el.querySelector('[data-testid="tg-description"]');
    await press(bullets(el));
    expect(el.querySelector('[data-testid="tg-description-unsaved"]')).not.toBeNull();
    const before = vi.mocked(listEvents).mock.calls.length;
    const select = el.querySelector<HTMLSelectElement>('[data-testid="language-select"]') as HTMLSelectElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set?.call(select, "ru");
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(0);
    });
    await settle();
    // The same editor section, the same draft in a toolbar of the new language, nothing was read again.
    const box = await editor(el);
    expect(el.querySelector('[data-testid="tg-description"]')).toBe(section);
    expect(box.querySelectorAll("li").length).toBeGreaterThan(0);
    expect(el.querySelector('[data-testid="tg-description-unsaved"]')?.textContent).toContain("Пока не сохранено");
    expect(vi.mocked(listEvents).mock.calls.length).toBe(before);
  });
});

describe("a description a website wrote", () => {
  const SITE = "<p>Vino night</p><br><b>Bring</b> a friend";

  it("is not opened: a calm note, no editor and a Save that cannot be pressed", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [eventItem({ description: SITE, description_base: SITE, description_format: "text" })] } as never);
    const el = mount(ready("description"), () => {}, "ru");
    await until(() => el.querySelector('[data-testid="tg-description-site-html"]') !== null, "the note");
    expect(el.querySelector('[data-testid="tg-description-site-html"]')?.textContent).toBe("Это описание пришло с сайта — правьте его на сайте.");
    expect(el.querySelector(".ProseMirror")).toBeNull();
    expect((el.querySelector('[data-testid="tg-description-save"]') as HTMLButtonElement).disabled).toBe(true);
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(saveDescription).not.toHaveBeenCalled();
  });

  it("does not take a plain text that only has a less-than sign for a site's HTML", async () => {
    vi.mocked(listEvents).mockResolvedValue({ events: [eventItem({ description: "Tickets < 20 EUR", description_base: "Tickets < 20 EUR", description_format: "text" })] } as never);
    const el = mount(ready("description"));
    await editor(el);
    expect(el.querySelector('[data-testid="tg-description-site-html"]')).toBeNull();
  });

  it("does not take ART with tags for a site's HTML", async () => {
    const el = mount(ready("description"));
    await editor(el);
    expect(el.querySelector('[data-testid="tg-description-site-html"]')).toBeNull();
  });
});

describe("a save refused for a translation", () => {
  it("names the language of the translation the server could not convert, and what to do", async () => {
    vi.mocked(saveDescription).mockRejectedValue(new ApiError(422, "event.invalid_description", "no", undefined, { field: "description", reason: "html_too_long", locale: "ru" }));
    const el = mount(ready("description"));
    await editor(el);
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    const text = el.querySelector('[data-testid="tg-description-error"]')?.textContent ?? "";
    expect(text).toContain("ru");
    expect(text).toContain("Nothing was saved");
    expect(text).toContain("web event center");
  });

  it("a refusal of the description itself keeps the general text", async () => {
    vi.mocked(saveDescription).mockRejectedValue(new ApiError(422, "event.invalid_description", "no", undefined, { field: "description", reason: "too_long" }));
    const el = mount(ready("description"));
    await editor(el);
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(el.querySelector('[data-testid="tg-description-error"]')?.textContent ?? "").not.toContain("translation");
  });
});

describe("the link to the web event center", () => {
  it("is not offered inside a Telegram window", async () => {
    // A Telegram client whose window cannot be closed from the page: the page says Saved, with no link out.
    const { app } = telegram();
    (app as { platform?: string }).platform = "android";
    (app as { close?: unknown }).close = undefined;
    vi.mocked(saveDescription).mockResolvedValue({} as never);
    const el = mount(ready("description"));
    await editor(el);
    await press(bullets(el));
    await press(el.querySelector('[data-testid="tg-description-save"]'));
    expect(el.querySelector('[data-testid="tg-edit-saved"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="tg-edit-open-event"]')).toBeNull();
  });
});
