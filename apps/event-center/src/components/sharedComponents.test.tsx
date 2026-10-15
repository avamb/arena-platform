/**
 * The shared components, rendered on the server (the test environment has no
 * DOM). The rules that need a click live in the pure state machines, which
 * have their own tests (dateField, dryRun, confirmWords).
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { DateField } from "@/components/DateField";
import { DryRunBody, DryRunConfirm } from "@/components/DryRunConfirm";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ApiError } from "@/lib/api/http";
import { CONFIRM_KINDS, confirmWord } from "@/lib/confirmWords";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";

function render(node: React.ReactNode, locale: Locale = "en"): string {
  return renderToStaticMarkup(<I18nProvider locale={locale}>{node}</I18nProvider>);
}

describe("ConfirmWordDialog", () => {
  for (const kind of CONFIRM_KINDS) {
    for (const locale of LOCALES) {
      it(`${kind} shows its word and an alert dialog in ${locale}`, () => {
        const html = render(<ConfirmWordDialog kind={kind} subject="Order 1042" onConfirm={() => {}} onCancel={() => {}} />, locale);
        expect(html).toContain('role="alertdialog"');
        expect(html).toContain('aria-modal="true"');
        expect(html).toContain(confirmWord(kind, locale));
        expect(html).toContain("Order 1042");
        expect(html).toContain('data-testid="confirm-word-submit"');
      });
    }
  }

  it("does not show another action's word", () => {
    const html = render(<ConfirmWordDialog kind="cancel_order" onConfirm={() => {}} onCancel={() => {}} />, "ru");
    expect(html).toContain("ОТМЕНИТЬ ЗАКАЗ");
    expect(html).not.toContain("ОТМЕНИТЬ СЕАНС");
  });

  it("the session text says the money is not refunded", () => {
    const html = render(<ConfirmWordDialog kind="cancel_session" onConfirm={() => {}} onCancel={() => {}} />);
    expect(html).toContain("does NOT refund");
  });

  it("starts with no note and an empty field", () => {
    const html = render(<ConfirmWordDialog kind="refund" onConfirm={() => {}} onCancel={() => {}} />);
    expect(html).not.toContain('data-testid="confirm-word-note"');
    expect(html).toContain('value=""');
  });

  it("escapes the subject", () => {
    const html = render(<ConfirmWordDialog kind="delete_event" subject={"<b>x</b>"} onConfirm={() => {}} onCancel={() => {}} />);
    expect(html).not.toContain("<b>x</b>");
    expect(html).toContain("&lt;b&gt;x&lt;/b&gt;");
  });
});

describe("DryRunConfirm", () => {
  it("shows the loading line first and no confirm button", () => {
    const html = render(<DryRunConfirm dryRun={() => new Promise(() => {})} onConfirm={() => {}} confirmLabel="Move the session" />);
    expect(html).toContain('data-testid="dry-run-loading"');
    expect(html).toContain("Checking what will change");
    expect(html).not.toContain('data-testid="dry-run-confirm"');
    expect(html).toContain('aria-busy="true"');
  });

  it("shows the title", () => {
    const html = render(<DryRunConfirm title="Move the session" dryRun={() => new Promise(() => {})} onConfirm={() => {}} confirmLabel="Go" />);
    expect(html).toContain("<h3>Move the session</h3>");
  });
});

describe("DryRunBody", () => {
  it("shows the summary, the numbers and the warnings", () => {
    const html = render(
      <DryRunBody
        result={{ summary: "Moves 12 orders", figures: [{ label: "Orders", value: 12 }, { label: "Letters", value: 12 }], warnings: ["The refund stays with you."] }}
      />,
    );
    expect(html).toContain("Moves 12 orders");
    expect(html).toContain("<dt>Orders</dt><dd>12</dd>");
    expect(html).toContain("Please note");
    expect(html).toContain("The refund stays with you.");
    expect(html).not.toContain("dry-run-blocked");
  });

  it("shows the reason when the change is blocked", () => {
    const html = render(<DryRunBody result={{ blocked: "Write to support." }} />);
    expect(html).toContain('data-testid="dry-run-blocked"');
    expect(html).toContain("This cannot be done yet");
    expect(html).toContain("Write to support.");
  });

  it("says nothing will change when the result is empty", () => {
    expect(render(<DryRunBody result={{}} />)).toContain("Nothing will change.");
  });

  it("speaks Russian and Spanish", () => {
    expect(render(<DryRunBody result={{ blocked: "x" }} />, "ru")).toContain("Пока это сделать нельзя");
    expect(render(<DryRunBody result={{ blocked: "x" }} />, "es")).toContain("Todavía no se puede hacer");
  });
});

describe("DateField", () => {
  it("draws the month of today with the days before today dead", () => {
    const html = render(<DateField value={null} onChange={() => {}} today="2026-10-15" />);
    expect(html).toContain("October 2026");
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*data-testid="date-day-2026-10-14"/);
    expect(html).not.toMatch(/<button[^>]*disabled=""[^>]*data-testid="date-day-2026-10-15"/);
    expect(html).toContain('data-testid="date-day-2026-10-31"');
    // The previous month has nothing to pick.
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*data-testid="date-field-prev"/);
    expect(html).not.toMatch(/<button[^>]*disabled=""[^>]*data-testid="date-field-next"/);
  });

  it("starts Monday first", () => {
    const html = render(<DateField value={null} onChange={() => {}} today="2026-10-15" />);
    expect(html.indexOf(">Mo<")).toBeGreaterThan(-1);
    expect(html.indexOf(">Mo<")).toBeLessThan(html.indexOf(">Su<"));
  });

  it("marks the chosen day and spells it", () => {
    const html = render(<DateField value="2026-10-20" onChange={() => {}} today="2026-10-15" />);
    expect(html).toMatch(/aria-pressed="true"[^>]*data-testid="date-day-2026-10-20"|data-testid="date-day-2026-10-20"[^>]*aria-pressed="true"/);
    expect(html).toContain("Selected: Tuesday, 20 October 2026");
  });

  it("shows the range, and days after the last one are dead", () => {
    const html = render(<DateField value={null} onChange={() => {}} today="2026-10-15" max="2026-10-20" />);
    expect(html).toContain("Available from 15.10.2026 to 20.10.2026.");
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*data-testid="date-day-2026-10-21"/);
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*data-testid="date-field-next"/);
  });

  it("says so when there is no day to pick", () => {
    const html = render(<DateField value={null} onChange={() => {}} today="2026-10-15" min="2026-10-25" max="2026-10-20" />);
    expect(html).toContain('data-testid="date-field-empty"');
    expect(html).not.toContain('data-testid="date-day-');
  });

  it("offers a text field whose answer needs a confirmation and no form of its own", () => {
    const html = render(<DateField value={null} onChange={() => {}} today="2026-10-15" />);
    expect(html).toContain('data-testid="date-field-input"');
    expect(html).toContain("We will show you how we read it");
    expect(html).not.toContain("<form");
  });

  it("speaks Russian with Monday first and the weekday in words", () => {
    const html = render(<DateField value="2026-10-20" onChange={() => {}} today="2026-10-15" />, "ru");
    expect(html).toContain("Октябрь 2026");
    expect(html).toContain(">Пн<");
    expect(html).toContain("вторник, 20 октября 2026");
  });

  it("speaks Spanish", () => {
    const html = render(<DateField value="2026-10-20" onChange={() => {}} today="2026-10-15" />, "es");
    expect(html).toContain("Octubre 2026");
    expect(html).toContain("martes, 20 de octubre de 2026");
  });

  it("offers the shortcuts that fit", () => {
    const html = render(<DateField value={null} onChange={() => {}} today="2026-10-15" max="2026-10-16" />);
    expect(html).toContain('data-testid="date-quick-today"');
    expect(html).toContain('data-testid="date-quick-tomorrow"');
    expect(html).not.toContain('data-testid="date-quick-week"');
  });
});

describe("ErrorNotice", () => {
  it("shows the title and the next step, not the server text", () => {
    const html = render(<ErrorNotice error={new ApiError(422, "organization.contact_missing", "raw server text")} />);
    expect(html).toContain('role="alert"');
    expect(html).toContain("The organizer contact is missing.");
    expect(html).toContain("Add the contact e-mail");
    expect(html).not.toContain("raw server text");
  });
});
