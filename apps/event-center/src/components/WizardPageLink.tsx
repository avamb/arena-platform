/**
 * "Fill in from your event page" (spec 59). The organizer pastes the address of
 * their OWN event page; the SERVER reads it (the browser never fetches it) and
 * answers suggestions. They are reviewed exactly like a poster's reading
 * (WizardPosterReview): nothing is filled in before "Fill in the selected", typed
 * text is never replaced without its own tick, a venue is chosen from the
 * organization's list. The page's first date goes to the first date; the other
 * dates are listed for the person to add. The page's picture is offered as the
 * poster with its own button.
 *
 * Every value the page gave is shown as plain text, never as markup.
 */
import { useEffect, useId, useRef, useState } from "react";
import { FIELD_LABEL, WizardPosterReview } from "@/components/WizardPosterReview";
import type { WizardPoster } from "@/components/useWizardPoster";
import { readEventPageFacts } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import type { DraftAction, WizardDraft } from "@/lib/wizard/draft";
import {
  classifyPageFailure,
  pageUrlOf,
  parsePageReading,
  type PageFailure,
  type PageReading,
} from "@/lib/wizard/pageLink";
import { useOrg } from "@/screens/OrgLayout";

type View =
  | { phase: "idle" }
  | { phase: "reading" }
  | { phase: "ready"; reading: PageReading; key: number }
  | { phase: "failed"; reason: PageFailure }
  | { phase: "done"; applied: readonly string[]; reading: PageReading };

const WARNING_TEXT: Record<string, MessageKey> = {
  "event_page.some_dates_skipped": "wizard.page.warn.someDatesSkipped",
  "event_page.past_dates_skipped": "wizard.page.warn.pastDatesSkipped",
  "event_page.cancelled_dates_skipped": "wizard.page.warn.cancelledDatesSkipped",
  "event_page.online_event": "wizard.page.warn.online",
  "event_page.price_range_only": "wizard.page.warn.priceRange",
  "event_page.model_unavailable": "wizard.page.warn.modelUnavailable",
  "event_page.model_quota": "wizard.page.warn.modelQuota",
  "event_page.image_unavailable": "wizard.page.warn.imageUnavailable",
  "event_page.javascript_page": "wizard.page.warn.javascript",
};

export function WizardPageLink({
  poster,
  canUsePicture,
  draft,
  dispatch,
  defaultCategoryName,
  busy,
}: {
  poster: WizardPoster;
  /** The person may set the poster (`media.write`): only then the picture is offered. */
  canUsePicture: boolean;
  draft: WizardDraft;
  dispatch: (action: DraftAction) => void;
  defaultCategoryName: string;
  busy: boolean;
}) {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const ids = useId();
  const [typed, setTyped] = useState("");
  const [invalid, setInvalid] = useState(false);
  const [view, setView] = useState<View>({ phase: "idle" });
  const abortRef = useRef<AbortController | null>(null);
  const seq = useRef(0);

  useEffect(() => () => abortRef.current?.abort(), []);
  // Another organization: whatever was read belongs to the old one.
  useEffect(() => {
    abortRef.current?.abort();
    setView({ phase: "idle" });
  }, [orgId]);

  if (!access.can("event.update")) return null;

  const read = async () => {
    const url = pageUrlOf(typed);
    if (url === null) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    abortRef.current?.abort();
    const ctl = new AbortController();
    abortRef.current = ctl;
    const mine = ++seq.current;
    setView({ phase: "reading" });
    try {
      const raw = await readEventPageFacts(orgId, url, ctl.signal);
      if (mine !== seq.current) return;
      const reading = parsePageReading(raw);
      setView(reading === null ? { phase: "failed", reason: "unavailable" } : { phase: "ready", reading, key: mine });
    } catch (err) {
      if (mine !== seq.current) return;
      const c = classifyPageFailure(err);
      setView(c === "aborted" ? { phase: "idle" } : { phase: "failed", reason: c });
    }
  };

  const reading = view.phase === "ready" || view.phase === "done" ? view.reading : null;
  const day = (iso: string): string => {
    try {
      return new Intl.DateTimeFormat(locale, { dateStyle: "long", timeZone: "UTC" }).format(new Date(`${iso}T00:00:00Z`));
    } catch {
      return iso;
    }
  };
  const image = reading?.image ?? null;
  const pictureUsed = image !== null && draft.poster.mediaId === image.mediaId;

  return (
    <fieldset className="org-form" data-testid="wizard-page" data-state={view.phase}>
      <legend>{t("wizard.page.title")}</legend>
      <p className="field-hint">{t("wizard.page.hint")}</p>
      <p className="field-hint">{t("wizard.page.ticketingNote")}</p>
      <label htmlFor={`${ids}-url`}>{t("wizard.page.url")}</label>
        <input
          id={`${ids}-url`}
          type="url"
          inputMode="url"
          autoComplete="url"
          maxLength={2048}
          value={typed}
          placeholder="https://"
          aria-invalid={invalid}
          aria-describedby={invalid ? `${ids}-invalid` : undefined}
          disabled={busy || view.phase === "reading"}
          onChange={(e) => {
            setTyped(e.target.value);
            setInvalid(false);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void read();
            }
          }}
          data-testid="wizard-page-url"
        />
        {invalid && (
          <p className="field-error" id={`${ids}-invalid`} role="alert" data-testid="wizard-page-invalid">
            {t("wizard.page.fail.invalid")}
          </p>
        )}
      <div className="actions">
        <button
          type="button"
          className="btn"
          onClick={() => void read()}
          disabled={busy || view.phase === "reading" || typed.trim() === ""}
          aria-busy={view.phase === "reading"}
          data-testid="wizard-page-read"
        >
          {view.phase === "idle" || view.phase === "reading" ? t("wizard.page.read") : t("wizard.page.again")}
        </button>
      </div>
      {view.phase === "reading" && (
        <p role="status" data-testid="wizard-page-reading">
          {t("wizard.page.reading")}
        </p>
      )}
      {view.phase === "failed" && (
        <p className="notice notice-bad" role="alert" data-testid="wizard-page-failed" data-reason={view.reason}>
          {t(`wizard.page.fail.${view.reason}` satisfies MessageKey)}
        </p>
      )}

      {reading !== null && (
        <div data-testid="wizard-page-found">
          {reading.byModel && (
            <p className="notice" data-testid="wizard-page-by-model">
              {t("wizard.page.byModel")}
            </p>
          )}
          {reading.warnings.length > 0 && (
            <ul className="field-hint" data-testid="wizard-page-warnings">
              {reading.warnings.map((w) => (
                <li key={w}>{t(WARNING_TEXT[w] ?? "wizard.page.warn.javascript")}</li>
              ))}
            </ul>
          )}
          {reading.otherDates.length > 0 && (
            <div className="notice" data-testid="wizard-page-other-dates">
              <p>{t("wizard.page.otherDates")}</p>
              <ul>
                {reading.otherDates.map((d) => (
                  <li key={`${d.date}|${d.time}`}>{d.time === "" ? day(d.date) : `${day(d.date)}, ${d.time}`}</li>
                ))}
              </ul>
            </div>
          )}
          {image !== null && canUsePicture && (
            <div className="poster-read" data-testid="wizard-page-image" data-fits={image.fitsPoster}>
              <p>{t("wizard.page.image.title", { width: image.width, height: image.height })}</p>
              {!image.fitsPoster && <p className="field-hint">{t("wizard.page.image.notPoster")}</p>}
              {pictureUsed ? (
                <p role="status" className="notice notice-ok" data-testid="wizard-page-image-used">
                  {t("wizard.page.image.used")}
                </p>
              ) : (
                <div className="actions">
                  <button
                    type="button"
                    className="btn"
                    disabled={busy}
                    onClick={() => poster.adoptStored(image.mediaId)}
                    data-testid="wizard-page-image-use"
                  >
                    {t("wizard.page.image.use")}
                  </button>
                </div>
              )}
              {draft.poster.mediaId !== "" && !pictureUsed && (
                <p className="field-hint">{t("wizard.page.image.replaces")}</p>
              )}
            </div>
          )}
          {view.phase === "ready" && (
            <WizardPosterReview
              key={view.key}
              source="page"
              facts={reading.facts}
              draft={draft}
              defaultCategoryName={defaultCategoryName}
              dispatch={dispatch}
              onDone={(applied) => setView({ phase: "done", applied, reading })}
            />
          )}
          {view.phase === "done" && (
            <p className="notice" role="status" data-testid="wizard-page-done">
              {view.applied.length === 0
                ? t("wizard.page.doneNone")
                : t("wizard.page.doneSome", {
                    fields: view.applied.map((k) => t(FIELD_LABEL[k] ?? "wizard.poster.fld.name")).join(", "),
                  })}
            </p>
          )}
        </div>
      )}
    </fieldset>
  );
}
