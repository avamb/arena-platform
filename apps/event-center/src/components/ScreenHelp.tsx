/**
 * "What is here?": a small link beside the heading of a screen. It opens a short help for THIS screen: one line per
 * item the person sees, in the order of the screen, and one line that tells how to come back here. The lines come
 * from `lib/screenHelp.ts`, built from the models that draw the screens, so a role never reads about an item it does
 * not have.
 *
 * Put it in a `.screen-head` row right after the heading, never inside the heading (the text would join the
 * heading's accessible name). It is a real button: Enter and Space open it, `aria-expanded` and `aria-controls` tell
 * a screen reader what it does, Escape and a second press close it and the focus returns to the button, and so does
 * a press outside. The text sits in the page flow under the row, so it needs no room beside the heading at 375 px.
 * Test ids: `screen-help-<screenId>` for the button, `screen-help-text` for the content.
 */
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { organizationsOf, useMe } from "@/lib/auth";
import { useI18n } from "@/lib/i18n";
import { screenHelp, type EventCardHelp, type ScreenId } from "@/lib/screenHelp";
import { shopModeFor } from "@/lib/shopModule";
import { useOrg } from "@/screens/OrgLayout";

export interface ScreenHelpProps {
  screenId: ScreenId;
  /** The event card only: the event and what the person may press on it. */
  card?: EventCardHelp;
}

export function ScreenHelp({ screenId, card }: ScreenHelpProps) {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const me = useMe();
  const id = useId();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);

  const close = useCallback((refocus: boolean) => {
    setOpen(false);
    if (refocus) button.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    const onPointer = (e: Event) => {
      if (root.current !== null && e.target instanceof Node && !root.current.contains(e.target)) close(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close(true);
    };
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("mousedown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("mousedown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, close]);

  const orgCount = me.data !== undefined ? organizationsOf(me.data.organization_memberships).length : 0;
  const help = screenHelp(screenId, { access, shopMode: shopModeFor(me.data?.organization_memberships, orgId), orgCount }, card);
  if (help === null) return null;

  return (
    <div ref={root} className="screen-help">
      <button
        ref={button}
        type="button"
        className="btn btn-link screen-help-btn"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen((v) => !v)}
        data-testid={`screen-help-${screenId}`}
      >
        {t("screenHelp.link")}
      </button>
      {open && (
        <div id={id} role="region" aria-label={t("screenHelp.panel")} className="screen-help-panel" data-testid="screen-help-text">
          {help.sections.map((s) => (
            <div key={s.id} data-testid={`screen-help-section-${s.id}`}>
              {s.titleKey !== undefined && <h3 className="screen-help-title">{t(s.titleKey)}</h3>}
              <ul className="screen-help-list">
                {s.lines.map((l) => (
                  <li key={l.id} data-testid="screen-help-line" data-line-id={l.id}>
                    <strong>{t(l.labelKey)}</strong> {t(l.textKey)}
                  </li>
                ))}
              </ul>
            </div>
          ))}
          <p className="screen-help-back" data-testid="screen-help-back">
            {t("screenHelp.back", { path: help.back.map((k) => t(k)).join(t("screenHelp.then")) })}
          </p>
        </div>
      )}
    </div>
  );
}
