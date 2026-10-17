/**
 * The review of what the poster said (poster-first creation). Nothing is
 * filled in until the person presses "Fill in the selected". A field that is
 * empty is ticked by default; a field that holds typed text is NOT: ticking it is
 * the person's explicit confirmation to replace it, and typing in it after the
 * review was drawn cancels that tick (lib/wizard/posterHints.ts, `Choice.basis`).
 * A venue is chosen from the organization's own list on purpose, a price needs its
 * category and currency confirmed, and what cannot be applied is listed as such.
 *
 * Every value is shown as plain text: the model's words never become markup.
 */
import { useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listVenues } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import type { WizardDraft, DraftAction, VenueChoice } from "@/lib/wizard/draft";
import {
  NO_SELECTION,
  buildActions,
  buildPlan,
  isRowOn,
  priceBasis,
  venueBasis,
  type Choice,
  type HintKey,
  type Plan,
  type PlanNote,
  type PosterFacts,
  type Selection,
} from "@/lib/wizard/posterHints";
import { useOrg } from "@/screens/OrgLayout";

const ROW_LABEL: Record<HintKey, MessageKey> = {
  name: "wizard.f.name",
  description: "wizard.f.description",
  age: "wizard.f.age",
  date: "wizard.poster.review.lbl.date",
  time: "wizard.poster.review.lbl.time",
  venueNew: "wizard.f.venueName",
  city: "wizard.f.city",
};

export const FIELD_LABEL: Record<string, MessageKey> = {
  name: "wizard.poster.fld.name",
  description: "wizard.poster.fld.description",
  age: "wizard.poster.fld.age",
  date: "wizard.poster.fld.date",
  time: "wizard.poster.fld.time",
  venueNew: "wizard.poster.fld.venueNew",
  city: "wizard.poster.fld.city",
  venue: "wizard.poster.fld.venue",
  price: "wizard.poster.fld.price",
};

function noteText(n: PlanNote, t: (k: MessageKey, p?: Record<string, string | number>) => string): string {
  switch (n.code) {
    case "venueName":
      return t("wizard.poster.review.note.venueName", { value: n.value });
    case "city":
      return t("wizard.poster.review.note.city", { value: n.value });
    case "priceSetup":
      return t("wizard.poster.review.note.priceSetup");
    case "priceCurrencyDiffers":
      return t("wizard.poster.review.note.priceCurrencyDiffers", { poster: n.poster, form: n.form });
    case "priceNoCurrency":
      return t("wizard.poster.review.note.priceNoCurrency");
    case "moreCategories":
      return t("wizard.poster.review.note.moreCategories");
  }
}

export function WizardPosterReview({
  facts,
  draft,
  defaultCategoryName,
  dispatch,
  onDone,
}: {
  facts: PosterFacts;
  draft: WizardDraft;
  defaultCategoryName: string;
  dispatch: (action: DraftAction) => void;
  /** Called with what was filled in (empty = nothing). */
  onDone: (applied: readonly string[]) => void;
}) {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const ids = useId();
  // The poster's day as a person reads it ("5 March 2031"); the form itself keeps the ISO day.
  const show = (key: HintKey, value: string): string => {
    if (key !== "date") return value;
    try {
      return new Intl.DateTimeFormat(locale, { dateStyle: "long", timeZone: "UTC" }).format(new Date(`${value}T00:00:00Z`));
    } catch {
      return value;
    }
  };
  const canList = access.can("venue.read");
  const venues = useQuery({ queryKey: ["venues", orgId], queryFn: () => listVenues(orgId), enabled: canList });
  const choices: VenueChoice[] | null = useMemo(() => {
    if (!canList) return null;
    if (venues.data === undefined) return null;
    return venues.data.venues.map((v) => ({
      id: v.id,
      name: v.name,
      timezone: v.timezone ?? null,
      capacity: v.capacity_default ?? null,
      country: v.country ?? null,
    }));
  }, [canList, venues.data]);

  // The plan is recomputed from the CURRENT form: text typed after the review appeared turns a fill into a replacement.
  const plan: Plan = useMemo(() => buildPlan(facts, { draft, venues: choices, defaultCategoryName }), [facts, draft, choices, defaultCategoryName]);
  const [sel, setSel] = useState<Selection>(NO_SELECTION);
  const venueChosen = sel.venue !== null && sel.venue.basis === venueBasis(draft) ? sel.venue.id : "";
  const priceChosen = sel.price !== null && sel.price.basis === priceBasis(draft) ? sel.price.index : -1;

  const toggle = (key: HintKey, on: boolean, basis: string) => {
    setSel((s) => {
      const rows = new Map<HintKey, Choice>(s.rows);
      rows.set(key, { on, basis });
      return { ...s, rows };
    });
  };

  const built = buildActions(plan, sel, draft);
  const count = built.applied.length;
  const waitingForVenues = canList && venues.isLoading && facts.venueName !== "";

  const apply = () => {
    if (count === 0) return;
    for (const a of built.actions) dispatch(a);
    onDone(built.applied);
  };

  const titleId = `${ids}-title`;
  return (
    <section className="poster-review" aria-labelledby={titleId} data-testid="poster-review">
      <h3 id={titleId}>{t("wizard.poster.review.title")}</h3>
      <p className="field-hint">{t("wizard.poster.review.intro")}</p>
      {waitingForVenues && (
        <p role="status" data-testid="poster-review-loading">
          {t("poster.loading")}
        </p>
      )}
      {!waitingForVenues && plan.nothingNew && (
        <p className="notice" data-testid="poster-review-empty">
          {t("wizard.poster.review.empty")}
        </p>
      )}

      {!waitingForVenues && plan.rows.length > 0 && (
        <ul className="review-list">
          {plan.rows.map((r) => {
            const on = isRowOn(r, sel.rows.get(r.key));
            const boxId = `${ids}-row-${r.key}`;
            return (
              <li key={r.key}>
                <label className="check" htmlFor={boxId}>
                  <input
                    id={boxId}
                    type="checkbox"
                    checked={on}
                    onChange={(e) => toggle(r.key, e.currentTarget.checked, r.current)}
                    aria-describedby={`${boxId}-d`}
                    data-testid={`poster-review-row-${r.key}`}
                  />
                  <span>
                    <strong>{t(ROW_LABEL[r.key])}</strong>
                    {": "}
                    <span className="review-value" data-testid={`poster-review-value-${r.key}`}>
                      {show(r.key, r.value)}
                    </span>
                  </span>
                </label>
                <div id={`${boxId}-d`}>
                  {r.conflict && (
                    <p className="field-hint" data-testid={`poster-review-replaces-${r.key}`}>
                      {t("wizard.poster.review.replaces", { current: r.current })}
                    </p>
                  )}
                  {r.key === "date" && <p className="field-hint">{t("wizard.poster.review.dateNote")}</p>}
                  {r.key === "time" && <p className="field-hint">{t("wizard.poster.review.timeNote")}</p>}
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {!waitingForVenues && plan.venueCandidates.length > 0 && (
        <fieldset className="review-choice" data-testid="poster-review-venue">
          <legend>{t("wizard.poster.review.venue.legend")}</legend>
          <p className="field-hint">{t("wizard.poster.review.venue.intro", { name: plan.venueName })}</p>
          <label className="check">
            <input
              type="radio"
              name={`${ids}-venue`}
              checked={venueChosen === ""}
              onChange={() => setSel((s) => ({ ...s, venue: null }))}
              data-testid="poster-review-venue-none"
            />
            <span>{t("wizard.poster.review.venue.none")}</span>
          </label>
          {plan.venueCandidates.map((c) => (
            <label key={c.id} className="check">
              <input
                type="radio"
                name={`${ids}-venue`}
                checked={venueChosen === c.id}
                onChange={() => setSel((s) => ({ ...s, venue: { id: c.id, basis: venueBasis(draft) } }))}
                data-testid={`poster-review-venue-${c.id}`}
              />
              <span>{c.name}</span>
            </label>
          ))}
          {draft.venue.mode === "existing" && draft.venue.existingId !== "" && (
            <p className="field-hint">{t("wizard.poster.review.venue.replaces")}</p>
          )}
        </fieldset>
      )}

      {!waitingForVenues && plan.prices.length > 0 && (
        <fieldset className="review-choice" data-testid="poster-review-price">
          <legend>{t("wizard.poster.review.price.legend")}</legend>
          <p className="field-hint">{t("wizard.poster.review.price.intro")}</p>
          <label className="check">
            <input
              type="radio"
              name={`${ids}-price`}
              checked={priceChosen === -1}
              onChange={() => setSel((s) => ({ ...s, price: null }))}
              data-testid="poster-review-price-none"
            />
            <span>{t("wizard.poster.review.price.none")}</span>
          </label>
          {plan.prices.map((p) => (
            <div key={p.index}>
              <label className="check">
                <input
                  type="radio"
                  name={`${ids}-price`}
                  checked={priceChosen === p.index}
                  onChange={() => setSel((s) => ({ ...s, price: { index: p.index, basis: priceBasis(draft) } }))}
                  data-testid={`poster-review-price-${p.index}`}
                />
                <span>
                  {p.name === ""
                    ? t("wizard.poster.review.price.optionNoName", { price: p.priceText, currency: p.currency })
                    : t("wizard.poster.review.price.option", { name: p.name, price: p.priceText, currency: p.currency })}
                </span>
              </label>
              {priceChosen === p.index && p.setsCurrency && (
                <p className="field-hint">{t("wizard.poster.review.price.setsCurrency", { currency: p.currency })}</p>
              )}
              {p.conflict && <p className="field-hint">{t("wizard.poster.review.price.replaces")}</p>}
            </div>
          ))}
        </fieldset>
      )}

      {!waitingForVenues && plan.notes.length > 0 && (
        <div className="notice" data-testid="poster-review-notes">
          <p>
            <strong>{t("wizard.poster.review.notes")}</strong>
          </p>
          <ul>
            {plan.notes.map((n, i) => (
              <li key={`${n.code}-${i}`}>{noteText(n, t)}</li>
            ))}
          </ul>
        </div>
      )}

      <div className="actions">
        <button type="button" className="btn btn-primary" onClick={apply} disabled={count === 0 || waitingForVenues} data-testid="poster-review-apply">
          {t("wizard.poster.review.apply", { count })}
        </button>
        <button type="button" className="btn" onClick={() => onDone([])} data-testid="poster-review-dismiss">
          {t("wizard.poster.review.dismiss")}
        </button>
      </div>
    </section>
  );
}
