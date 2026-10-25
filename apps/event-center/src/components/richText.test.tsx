// @vitest-environment jsdom
/**
 * RT-11 (spec 44): the editor in the web screens, run for real in jsdom with the built library
 * (`@arena/rich-editor`, loaded the way the page loads it: a dynamic import). Covers
 *  - RichTextField: label and text, a value that arrives from outside, edits, read-only, the "Format"
 *    button and its neutral line when the server did not use the model, pasted Word/Google Docs
 *    markup cut to the white list, no style attribute anywhere;
 *  - the wizard's description field and the message of a session change;
 *  - the screen "Text in the letter": save, test letter (draft, empty draft, rate limit), delete.
 * The clicks of the whole flow against a real API are the live twin tests (tests/smoke/live).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/endpoints")>()),
  formatText: vi.fn(),
  fetchChangeImpact: vi.fn(),
  saveEmailNote: vi.fn(),
  sendEmailNoteTest: vi.fn(),
  listPromoters: vi.fn(async () => ({ promoters: [] })),
}));

import { EmailNoteSection } from "@/components/EmailNoteSection";
import { RichTextField } from "@/components/RichTextField";
import { SessionCancelPanel } from "@/components/SessionCancelPanel";
import { SessionChangeMessage } from "@/components/SessionChangeMessage";
import { WizardEventSection } from "@/components/WizardEventSection";
import { fetchChangeImpact, formatText, saveEmailNote, sendEmailNoteTest } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import { I18nProvider, type Locale } from "@/lib/i18n";
import { emptyMessage, typeMessage, withSuggested } from "@/lib/sessionChange";
import { newDraft } from "@/lib/wizard/draft";
import { OrgCtx } from "@/screens/OrgLayout";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
let client: QueryClient;

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.mocked(formatText).mockReset();
  vi.mocked(saveEmailNote).mockReset();
  vi.mocked(sendEmailNoteTest).mockReset();
});

afterEach(() => {
  while (roots.length > 0) {
    const r = roots.pop() as Root;
    act(() => r.unmount());
  }
  document.body.innerHTML = "";
});

interface Mounted {
  el: HTMLElement;
  rerender: (ui: ReactElement) => void;
}

function wrap(ui: ReactElement, locale: Locale) {
  return (
    <QueryClientProvider client={client}>
      <I18nProvider locale={locale}>{ui}</I18nProvider>
    </QueryClientProvider>
  );
}

function mount(ui: ReactElement, locale: Locale = "en"): Mounted {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  act(() => root.render(wrap(ui, locale)));
  return { el, rerender: (next) => act(() => root.render(wrap(next, locale))) };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The editor is loaded by a dynamic import: wait until its text area is on the page. */
async function ready(el: HTMLElement): Promise<HTMLElement> {
  for (let i = 0; i < 300; i++) {
    const box = el.querySelector<HTMLElement>(".ProseMirror");
    if (box !== null) return box;
    await act(async () => {
      await sleep(10);
    });
  }
  throw new Error("the editor did not load");
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

const action = (el: HTMLElement, name: string) => el.querySelector<HTMLButtonElement>(`button[data-action="${name}"]`);

async function press(button: HTMLElement | null) {
  expect(button).not.toBeNull();
  await act(async () => {
    (button as HTMLElement).click();
    await sleep(0);
  });
}

/** A paste the way a browser makes it: the data is read from clipboardData. */
function paste(dom: HTMLElement, html: string, text = ""): void {
  const ev = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "clipboardData", {
    value: { types: ["text/html", "text/plain"], files: [], items: [], getData: (t: string) => (t === "text/html" ? html : t === "text/plain" || t === "Text" ? text : "") },
  });
  act(() => {
    dom.dispatchEvent(ev);
  });
}

describe("RichTextField", () => {
  it("shows the label, the text as formatted nodes, and names the text area by its label", async () => {
    const { el } = mount(
      <RichTextField profile="description" label="Description" hint="Optional" value="<p>Hello <strong>bold</strong></p><ul><li>one</li><li>two</li></ul>" onChange={() => {}} testId="f" />,
    );
    const box = await ready(el);
    expect(box.querySelector("strong")?.textContent).toBe("bold");
    expect(box.querySelectorAll("li")).toHaveLength(2);
    const label = el.querySelector("label") as HTMLElement;
    expect(label.textContent).toBe("Description");
    expect(box.getAttribute("aria-labelledby")).toBe(label.id);
    expect(box.getAttribute("role")).toBe("textbox");
    expect(el.textContent).toContain("Optional");
    // The counter is the visible text, not the HTML: "Hello bold", "one", "two" and the breaks between them.
    expect(el.querySelector(".arena-re")?.textContent).toMatch(/\d+ of 4000/);
  });

  it("sets no style attribute anywhere (style-src 'self' holds)", async () => {
    const { el } = mount(<RichTextField profile="email_note" label="L" value="<p>x</p>" onChange={() => {}} orgId="o" />);
    await ready(el);
    expect(el.querySelectorAll("[style]")).toHaveLength(0);
    expect(document.head.querySelectorAll("style")).toHaveLength(0);
  });

  it("takes a value that arrives from outside, and its own output changes nothing", async () => {
    const seen: string[] = [];
    const ui = (value: string) => <RichTextField profile="description" label="L" value={value} onChange={(a) => seen.push(a)} />;
    const m = mount(ui("<p>first</p>"));
    const box = await ready(m.el);
    expect(box.textContent).toBe("first");
    m.rerender(ui("<p>second <em>text</em></p>"));
    await until(() => box.textContent === "second text", "the new value");
    expect(box.querySelector("em")?.textContent).toBe("text");
    // The value that came in is not an edit of the person: onChange is not called for it.
    expect(seen).toEqual([]);
  });

  it("never puts a late echo of its own output back into the editor (the person kept typing)", async () => {
    const seen: string[] = [];
    const ui = (value: string) => <RichTextField profile="description" label="L" value={value} onChange={(a) => seen.push(a)} />;
    const m = mount(ui("<p>one</p>"));
    const box = await ready(m.el);
    // Two edits in a row: the parent has not rendered yet.
    await press(action(m.el, "bulletList"));
    await press(action(m.el, "orderedList"));
    expect(seen).toHaveLength(2);
    expect(box.querySelector("ol")).not.toBeNull();
    // The parent now hands back the FIRST value (a stale echo), then the second.
    m.rerender(ui(seen[0]));
    await act(async () => {
      await sleep(20);
    });
    expect(box.querySelector("ol"), "the stale echo replaced the newer text").not.toBeNull();
    expect(box.querySelector("ul")).toBeNull();
    m.rerender(ui(seen[1]));
    // And a value that did not come from the editor still goes in.
    m.rerender(ui("<p>from outside</p>"));
    await until(() => box.textContent === "from outside", "the outside value");
  });

  it("reports an edit as canonical ART", async () => {
    const seen: string[] = [];
    const { el } = mount(<RichTextField profile="description" label="L" value="<p>one</p>" onChange={(a) => seen.push(a)} />);
    await ready(el);
    await press(action(el, "bulletList"));
    expect(seen.at(-1)).toBe("<ul><li>one</li></ul>");
  });

  it("is read-only when disabled", async () => {
    const m = mount(<RichTextField profile="description" label="L" value="<p>x</p>" onChange={() => {}} disabled />);
    const box = await ready(m.el);
    expect(box.getAttribute("aria-readonly")).toBe("true");
    expect(action(m.el, "bold")?.disabled).toBe(true);
  });

  it("marks the field invalid and shows the reason", async () => {
    const { el } = mount(<RichTextField profile="description" label="L" value="" error="Too long" onChange={() => {}} testId="f" />);
    await ready(el);
    expect(el.querySelector('[data-testid="f-error"]')?.textContent).toBe("Too long");
    expect(el.querySelector('[role="group"]')?.getAttribute("aria-invalid")).toBe("true");
  });

  it("has the Format button only for a field that knows its organization", async () => {
    const without = mount(<RichTextField profile="description" label="L" value="<p>x</p>" onChange={() => {}} />);
    await ready(without.el);
    expect(action(without.el, "format")).toBeNull();
    const withOrg = mount(<RichTextField profile="description" label="L" value="<p>x</p>" onChange={() => {}} orgId="o-1" />);
    await ready(withOrg.el);
    expect(action(withOrg.el, "format")).not.toBeNull();
  });

  it("uses the server's answer when the model did not run, and says so without any word of failure", async () => {
    vi.mocked(formatText).mockResolvedValue({ html: "<p>Hello <strong>world</strong></p>", ai_applied: false, reason: "not_configured", prompt_version: "rt04-v1" });
    const seen: string[] = [];
    const { el } = mount(<RichTextField profile="description" label="L" value="<p>Hello world</p>" onChange={(a) => seen.push(a)} orgId="o-1" />, "en");
    const box = await ready(el);
    await press(action(el, "format"));
    await until(() => seen.length > 0, "the formatted text");
    expect(formatText).toHaveBeenCalledWith("o-1", { text: "Hello world", kind: "description", locale: "en" });
    expect(box.querySelector("strong")?.textContent).toBe("world");
    const line = (el.querySelector(".arena-re [role=\"status\"]") as HTMLElement).textContent as string;
    expect(line).toContain("not available now");
    expect(line).toContain("kept as it is");
    expect(line).not.toMatch(/error|could not|failed|try again/i);
  });

  it("says the text is formatted when the model was used", async () => {
    vi.mocked(formatText).mockResolvedValue({ html: "<p>Hello <em>world</em></p>", ai_applied: true, prompt_version: "rt04-v1" });
    const { el } = mount(<RichTextField profile="email_note" label="L" value="<p>Hello world</p>" onChange={() => {}} orgId="o-1" />, "ru");
    const box = await ready(el);
    await press(action(el, "format"));
    await until(() => box.querySelector("em") !== null, "the formatted text");
    expect(formatText).toHaveBeenCalledWith("o-1", { text: "Hello world", kind: "email_note", locale: "ru" });
    expect((el.querySelector('.arena-re [role="status"]') as HTMLElement).textContent).toContain("Текст оформлен");
  });

  it("cuts a paste from Word and from Google Docs to the white list of the profile", async () => {
    const seen: string[] = [];
    const { el } = mount(<RichTextField profile="email_note" label="L" value="" onChange={(a) => seen.push(a)} />);
    const box = await ready(el);
    const word =
      "<html><head><style><!-- p.MsoNormal { font-size:11.0pt; } --></style></head><body style='tab-interval:36.0pt'><!--StartFragment-->" +
      "<p class=MsoNormal><b style='mso-bidi-font-weight:normal'><span style='font-size:14.0pt;font-family:Calibri'>Summer concert</span></b><o:p></o:p></p>" +
      '<p class=MsoNormal>Doors at <i>7 pm</i>, <a href="https://example.com/tickets" style="color:blue" onclick="alert(1)">our site</a>.</p>' +
      '<div class="x"><img src="https://evil.example/p.png" onerror="alert(2)"><script>alert(3)</script><table><tr><td>cell</td></tr></table></div><!--EndFragment--></body></html>';
    paste(box, word);
    const html = box.innerHTML;
    expect(html).toContain("<strong>Summer concert</strong>");
    expect(html).toContain("<em>7 pm</em>");
    expect(html).toContain('href="https://example.com/tickets"');
    expect(html).toContain("cell");
    for (const bad of ["style=", "class=", "onclick", "onerror", "<img", "<script", "<table", "<span", "<div", "MsoNormal", "evil.example", "alert("]) {
      expect(html, bad).not.toContain(bad);
    }
    expect(el.querySelectorAll("[style]")).toHaveLength(0);
    // What the page would store is the canonical ART of the same text.
    expect(seen.at(-1)).toMatch(/^<p><strong>Summer concert<\/strong><\/p><p>Doors at <em>7 pm<\/em>, <a href="https:\/\/example.com\/tickets">our site<\/a>\.<\/p>/);

    const google =
      `<meta charset='utf-8'><b style="font-weight:normal;" id="docs-internal-guid-1a2b"><p dir="ltr" style="line-height:1.38;"><span style="font-size:11pt;font-weight:700;">Bold start</span><span style="font-size:11pt;font-weight:400;"> and plain</span></p>` +
      `<ul style="margin-top:0;"><li dir="ltr" style="list-style-type:disc;"><p dir="ltr" role="presentation"><span>One</span></p></li></ul></b>`;
    paste(box, google);
    expect(box.innerHTML).not.toContain("style=");
    expect(box.innerHTML).toContain("<strong>Bold start</strong>");
    // The wrapper <b style="font-weight:normal"> of Google Docs does not make the whole paste bold.
    expect(box.innerHTML).not.toContain("<strong> and plain");
    expect(box.querySelector("ul li")?.textContent).toBe("One");
  });
});

describe("the description in the wizard", () => {
  function section(onDispatch: (a: unknown) => void, description: string) {
    const draft = newDraft("d", new Date("2026-10-25T10:00:00Z"));
    const withText = { ...draft, event: { ...draft.event, description } };
    const access = { data: undefined, isLoading: false, error: null, can: () => false };
    return (
      <OrgCtx.Provider value={{ orgId: "o-1", orgName: "Org", access }}>
        <WizardEventSection draft={withText} dispatch={onDispatch} touch={() => {}} errorOf={() => undefined} />
      </OrgCtx.Provider>
    );
  }

  it("is the editor with the description profile (headings and quote), and keeps what the person writes as ART", async () => {
    const actions: unknown[] = [];
    const { el } = mount(section((a) => actions.push(a), "<p>A ballet</p>"));
    const box = await ready(el);
    expect(el.querySelector('[data-testid="wizard-description"]')).not.toBeNull();
    // The description profile has headings and a quote; the other profiles do not.
    expect(action(el, "h3")).not.toBeNull();
    expect(action(el, "quote")).not.toBeNull();
    expect(box.textContent).toBe("A ballet");
    expect(el.querySelector("textarea")).toBeNull();
    // The Format button is there: the wizard knows its organization.
    expect(action(el, "format")).not.toBeNull();
    await press(action(el, "bulletList"));
    expect(actions.at(-1)).toEqual({ type: "event", field: "description", value: "<ul><li>A ballet</li></ul>" });
  });
});

describe("the message of a session change", () => {
  it("shows the suggested plain text as paragraphs and a list, with the no-refund hint, in the session_message profile", async () => {
    // (a JSX attribute string does not read "\n": the text goes through a constant)
    const suggested = "We moved the show.\n\n- bring your ticket\n- doors at 19:00";
    const state = withSuggested(emptyMessage, suggested);
    const { el } = mount(<SessionChangeMessage orgId="o-1" state={state} suggested={suggested} onType={() => {}} onReset={() => {}} />);
    const box = await ready(el);
    expect(box.querySelectorAll("li")).toHaveLength(2);
    expect(box.querySelector("p")?.textContent).toBe("We moved the show.");
    expect(el.textContent).toContain("Do not promise a refund");
    // This profile has no heading and no quote button, and shows 1000 as the limit.
    expect(action(el, "h3")).toBeNull();
    expect(action(el, "quote")).toBeNull();
    expect(el.querySelector(".arena-re")?.textContent).toMatch(/of 1000/);
    // The suggested text is the current one: no "Use the suggested text" button.
    expect(el.querySelector('[data-testid="session-change-message-reset"]')).toBeNull();
  });

  it("offers the suggested text back after the person changed it, and refuses an over-long text on the screen", async () => {
    const onReset = vi.fn();
    const typed = typeMessage("<p>" + "x".repeat(1001) + "</p>");
    const { el } = mount(<SessionChangeMessage orgId="o-1" state={typed} suggested="We moved the show." onType={() => {}} onReset={onReset} />);
    await ready(el);
    expect(el.querySelector('[data-testid="session-change-message-error"]')?.textContent).toContain("1000");
    await press(el.querySelector('[data-testid="session-change-message-reset"]'));
    expect(onReset).toHaveBeenCalledTimes(1);
  });
});

describe("the screen Text in the letter", () => {
  const props = { orgId: "o-1", eventId: "e-1", canEdit: true };
  const NOTE = "<p>Bring a <strong>warm</strong> jacket.</p>";

  it("says plainly that the letter adds the greeting, the date and the venue, and promises no substitution", async () => {
    const { el } = mount(<EmailNoteSection {...props} note={null} noteFormat={null} />);
    await ready(el);
    expect(el.textContent).toContain("adds the greeting, the date and the venue");
    expect(el.textContent).not.toMatch(/\{name\}|\{date\}|\{event\}/);
    expect(el.querySelector('[data-testid="email-note-none"]')).not.toBeNull();
    // Nothing to save yet.
    expect((el.querySelector('[data-testid="email-note-save"]') as HTMLButtonElement).disabled).toBe(true);
  });

  it("reads an ART note as it is and an old plain note through the converter, never showing marks or tags", async () => {
    const art = mount(<EmailNoteSection {...props} note={NOTE} noteFormat="art" />);
    const box = await ready(art.el);
    expect(box.querySelector("strong")?.textContent).toBe("warm");
    const plain = mount(<EmailNoteSection {...props} note={"First line\n\n- one\n- two"} noteFormat="text" />);
    const box2 = await ready(plain.el);
    expect(box2.querySelectorAll("li")).toHaveLength(2);
    expect(box2.textContent).not.toContain("- one");
    const unknown = mount(<EmailNoteSection {...props} note={"Plain <b> text"} noteFormat={null} />);
    expect((await ready(unknown.el)).textContent).toBe("Plain <b> text");
  });

  it("saves ART with its format in ONE call, and sends the draft to the test letter", async () => {
    vi.mocked(saveEmailNote).mockResolvedValue({} as never);
    vi.mocked(sendEmailNoteTest).mockResolvedValue({ queued: true, to: "me@example.test" });
    const { el } = mount(<EmailNoteSection {...props} note={NOTE} noteFormat="art" />);
    await ready(el);
    const save = el.querySelector('[data-testid="email-note-save"]') as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    await press(action(el, "bulletList"));
    expect(el.querySelector('[data-testid="email-note-unsaved"]')).not.toBeNull();
    expect(save.disabled).toBe(false);
    await press(save);
    expect(saveEmailNote).toHaveBeenCalledTimes(1);
    expect(saveEmailNote).toHaveBeenCalledWith("o-1", "e-1", "<ul><li>Bring a <strong>warm</strong> jacket.</li></ul>");
    expect(el.querySelector('[data-testid="email-note-status"]')?.textContent).toContain("Saved");

    await press(el.querySelector('[data-testid="email-note-test"]'));
    expect(sendEmailNoteTest).toHaveBeenCalledWith("o-1", "e-1", { note: "<ul><li>Bring a <strong>warm</strong> jacket.</li></ul>", note_format: "art", locale: "en" });
    expect(el.querySelector('[data-testid="email-note-status"]')?.textContent).toContain("me@example.test");
  });

  it("follows the saved text while nothing was typed, and never overwrites what the person typed", async () => {
    const { el, rerender } = mount(<EmailNoteSection {...props} note={NOTE} noteFormat="art" />);
    const box = await ready(el);
    // The bot (or another tab) changed the text and the list was read again: the editor shows it.
    rerender(<EmailNoteSection {...props} note="<p>Changed by the bot.</p>" noteFormat="art" />);
    await until(() => box.textContent === "Changed by the bot.", "the editor follows the saved text");
    expect(el.querySelector('[data-testid="email-note-unsaved"]')).toBeNull();
    // The person types: a later change of the saved text must not wipe the typing.
    await press(action(el, "bulletList"));
    expect(box.querySelectorAll("li")).toHaveLength(1);
    rerender(<EmailNoteSection {...props} note="<p>Changed again.</p>" noteFormat="art" />);
    await act(async () => {
      await sleep(30);
    });
    expect(box.querySelectorAll("li")).toHaveLength(1);
    expect(box.textContent).toBe("Changed by the bot.");
    expect(el.querySelector('[data-testid="email-note-unsaved"]')).not.toBeNull();
  });

  it("clears the unsaved mark after a save even when the server writes the ART a little differently", async () => {
    vi.mocked(saveEmailNote).mockResolvedValue({} as never);
    const { el, rerender } = mount(<EmailNoteSection {...props} note={NOTE} noteFormat="art" />);
    await ready(el);
    await press(action(el, "bulletList"));
    expect(el.querySelector('[data-testid="email-note-unsaved"]')).not.toBeNull();
    await press(el.querySelector('[data-testid="email-note-save"]'));
    // The list is read again: the server's canonical form is not byte for byte the editor's.
    rerender(<EmailNoteSection {...props} note="<ul><li><p>Bring a <strong>warm</strong> jacket.</p></li></ul>" noteFormat="art" />);
    await until(() => el.querySelector('[data-testid="email-note-unsaved"]') === null, "the unsaved mark goes");
    expect((el.querySelector('[data-testid="email-note-save"]') as HTMLButtonElement).disabled).toBe(true);
  });

  it("locks the editor while the delete is running", async () => {
    let finish: (v: never) => void = () => {};
    vi.mocked(saveEmailNote).mockReturnValue(new Promise((resolve) => (finish = resolve as (v: never) => void)));
    const { el } = mount(<EmailNoteSection {...props} note={NOTE} noteFormat="art" />);
    const box = await ready(el);
    await press(el.querySelector('[data-testid="email-note-delete"]'));
    await press(el.querySelector('[data-testid="email-note-delete-dialog-submit"]'));
    expect(saveEmailNote).toHaveBeenCalledWith("o-1", "e-1", null);
    expect(box.getAttribute("aria-readonly")).toBe("true");
    expect((el.querySelector('[data-testid="email-note-test"]') as HTMLButtonElement).disabled).toBe(true);
    await act(async () => {
      finish({} as never);
      await sleep(0);
    });
    await until(() => el.querySelector('[data-testid="email-note-status"]')?.textContent?.includes("deleted") === true, "deleted");
  });

  it("gives the empty-text refusal and the hourly limit their own lines", async () => {
    vi.mocked(sendEmailNoteTest).mockRejectedValueOnce(new ApiError(422, "event.email_note_empty", "no text"));
    const { el } = mount(<EmailNoteSection {...props} note={null} noteFormat={null} />);
    await ready(el);
    await press(el.querySelector('[data-testid="email-note-test"]'));
    // The letter is what the editor holds: an empty editor is an empty draft, not the saved text.
    expect(sendEmailNoteTest).toHaveBeenLastCalledWith("o-1", "e-1", { note: "", note_format: "art", locale: "en" });
    expect(el.querySelector('[data-testid="email-note-error"]')?.textContent).toContain("There is no text to send");
    vi.mocked(sendEmailNoteTest).mockRejectedValueOnce(new ApiError(429, "event.email_note_test_rate_limited", "slow down"));
    await press(el.querySelector('[data-testid="email-note-test"]'));
    expect(el.querySelector('[data-testid="email-note-error"]')?.textContent).toContain("Too many test letters");
    expect(el.querySelector('[data-testid="email-note-error"]')?.textContent).toContain("5 test letters an hour");
    expect(el.querySelector('[data-testid="email-note-status"]')).toBeNull();
  });

  it("deletes only after a question, and Cancel changes nothing", async () => {
    vi.mocked(saveEmailNote).mockResolvedValue({} as never);
    const { el } = mount(<EmailNoteSection {...props} note={NOTE} noteFormat="art" />);
    await ready(el);
    await press(el.querySelector('[data-testid="email-note-delete"]'));
    expect(el.querySelector('[data-testid="email-note-delete-dialog"]')?.textContent).toContain("without this text");
    await press(el.querySelector('[data-testid="email-note-delete-dialog-cancel"]'));
    expect(saveEmailNote).not.toHaveBeenCalled();
    expect(el.querySelector('[data-testid="email-note-delete-dialog"]')).toBeNull();
    await press(el.querySelector('[data-testid="email-note-delete"]'));
    await press(el.querySelector('[data-testid="email-note-delete-dialog-submit"]'));
    expect(saveEmailNote).toHaveBeenCalledTimes(1);
    expect(saveEmailNote).toHaveBeenCalledWith("o-1", "e-1", null);
    expect(el.querySelector('[data-testid="email-note-status"]')?.textContent).toContain("deleted");
  });

  it("shows a person without the right to edit the text, read-only, with no button and no Format", async () => {
    const { el } = mount(<EmailNoteSection {...props} canEdit={false} note={NOTE} noteFormat="art" />);
    const box = await ready(el);
    expect(box.getAttribute("aria-readonly")).toBe("true");
    expect(el.querySelector('[data-testid="email-note-save"]')).toBeNull();
    expect(el.querySelector('[data-testid="email-note-test"]')).toBeNull();
    expect(el.querySelector('[data-testid="email-note-delete"]')).toBeNull();
    expect(action(el, "format")).toBeNull();
    expect(el.textContent).toContain("needs the right to edit the event");
  });
});

describe("who may use the Format button in the message of a session change", () => {
  const impact = {
    blocked: "",
    kinds: ["cancel"],
    orders: 1,
    tickets: 1,
    arena_orders: 1,
    site_orders: 0,
    no_address: 0,
    default_message: "The show is canceled.",
    contact: { complete: true, email: "o@example.test" },
  };

  function panel(permissions: string[]) {
    const access = { data: undefined, isLoading: false, error: null, can: (p: string) => permissions.includes(p) };
    return (
      <OrgCtx.Provider value={{ orgId: "o-1", orgName: "Org", access }}>
        <SessionCancelPanel orgId="o-1" eventId="e-1" sessionId="s-1" startAt="2090-06-15T17:00:00Z" timezone="Europe/Madrid" onDone={() => {}} onClose={() => {}} />
      </OrgCtx.Provider>
    );
  }

  it("offers Format with event.update, and not to a role that has session.update only (the route would answer 403)", async () => {
    vi.mocked(fetchChangeImpact).mockResolvedValue(impact as never);
    const withRight = mount(panel(["session.update", "event.update"]));
    await ready(withRight.el);
    expect(action(withRight.el, "format")).not.toBeNull();
    const without = mount(panel(["session.update"]));
    await ready(without.el);
    expect(action(without.el, "format")).toBeNull();
    // The message itself is still written: only the helper that needs another right is left out.
    expect(without.el.querySelector('[data-testid="session-change-message-box"]')).not.toBeNull();
  });
});
