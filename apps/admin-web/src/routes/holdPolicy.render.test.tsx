/**
 * Render test of the seat hold form (spec 67, section 7.2). The admin-web Vitest environment is Node-only,
 * so the router- and query-free form is rendered with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { HoldProfileForm, type HoldProfileFormProps } from "@/routes/holdPolicy";
import { EventHoldProfileView, choiceToProfile } from "@/routes/eventHoldProfile";
import { buildHoldRequest, formFromProfile, type HoldProfile } from "@/lib/api/holdPolicy";
import { enCatalog, ruCatalog, translate } from "@/lib/i18n/catalog";

const profile: HoldProfile = {
  profile: "high_demand",
  select_seconds: 420,
  payment_seconds: 600,
  extend_seconds: 300,
  extend_count: 1,
  paying_seconds: 600,
  max_seats_per_hold: 10,
  max_active_per_device: 2,
  max_active_per_ip: 6,
  rehold_pause_seconds: 300,
  poll_interval_ms: 1500,
};

const ru = (key: string, params?: Readonly<Record<string, string | number>>) => translate("ru", key, params);
const noop = (): void => {};

function render(over: Partial<HoldProfileFormProps>): string {
  return renderToStaticMarkup(
    <HoldProfileForm
      profile={profile}
      form={formFromProfile(profile)}
      reason=""
      errors={[]}
      serverError={null}
      saved={false}
      saving={false}
      t={ru}
      onField={noop}
      onReason={noop}
      onSubmit={noop}
      {...over}
    />,
  );
}

describe("HoldProfileForm", () => {
  it("shows minutes, not seconds, in Russian", () => {
    const html = render({});
    expect(html).toContain("Повышенный спрос");
    expect(html).toContain("Выбор мест, минут");
    expect(html).toMatch(/data-testid="hold-high_demand-select_minutes"[^>]*value="7"/);
    expect(html).toMatch(/data-testid="hold-high_demand-poll_interval_seconds"[^>]*value="1.5"/);
    expect(html).not.toContain('value="420"');
  });

  it("marks the empty reason as required", () => {
    const built = buildHoldRequest(formFromProfile(profile), "");
    expect(built.ok).toBe(false);
    const html = render({ errors: built.ok ? [] : built.errors });
    expect(html).toContain('data-testid="hold-high_demand-reason-error"');
    expect(html).toContain("Напишите причину изменения.");
  });

  it("shows the server refusal", () => {
    expect(render({ serverError: "superadmin only" })).toContain("superadmin only");
  });
});

describe("the hold mode of an event", () => {
  it("maps the choice to the wire value", () => {
    expect(choiceToProfile("")).toBeUndefined();
    expect(choiceToProfile("reset")).toBeNull();
    expect(choiceToProfile("high_demand")).toBe("high_demand");
  });

  it("renders the select and the reason field", () => {
    const html = renderToStaticMarkup(
      <EventHoldProfileView
        eventId="e1"
        choice=""
        reason=""
        error={null}
        done={false}
        busy={false}
        t={ru}
        onChoice={noop}
        onReason={noop}
        onApply={noop}
      />,
    );
    expect(html).toContain('data-testid="event-hold-profile-select"');
    expect(html).toContain("Повышенный спрос (high_demand)");
    expect(html).toContain('data-testid="event-hold-profile-reason"');
  });
});

describe("catalogs", () => {
  it("have the same hold policy keys in both languages", () => {
    const keys = (c: Readonly<Record<string, string>>) =>
      Object.keys(c)
        .filter((k) => k.startsWith("holdPolicy.") || k === "nav.hold_policy")
        .sort();
    expect(keys(ruCatalog)).toEqual(keys(enCatalog));
    expect(keys(ruCatalog).length).toBeGreaterThan(40);
  });
});
