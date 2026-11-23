/**
 * "Who sells the tickets" on the card of ONE session (spec 63, L-3): the
 * platform (the default) or a partner box office. The same question the wizard
 * asks, edited with one PATCH of the session.
 *
 * The server owns the hard rule: a session that already has orders, holds,
 * seats or categories cannot be handed to a partner, and a partner session
 * with seats or categories cannot come back (409 `session.sales_mode_locked`).
 * The card says so in plain words before the person tries (`external.session.hint`)
 * and shows the answer's own words when the server refuses (ErrorNotice).
 *
 * Drawn only for a person who may change a session (`session.update`).
 */
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ExternalFields, SalesModeChoice } from "@/components/ExternalSaleFields";
import { updateSessionSalesMode } from "@/lib/api/endpoints";
import type { SessionItem } from "@/lib/api/types";
import { externalFormOf, externalProblems, salesModePatch, type ExternalField, type ExternalForm, type SalesMode } from "@/lib/externalSale";
import { useI18n } from "@/lib/i18n";

export interface SessionSalesModeProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  session: Pick<SessionItem, "sales_mode" | "external" | "currency">;
  /** A canceled session keeps its seller. */
  cancelled?: boolean;
}

export function SessionSalesMode({ orgId, eventId, sessionId, session, cancelled = false }: SessionSalesModeProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const stored: SalesMode = session.sales_mode === "external" ? "external" : "platform";
  const [mode, setMode] = useState<SalesMode>(stored);
  const [form, setForm] = useState<ExternalForm>(() => externalFormOf(session));
  const [reached, setReached] = useState<ReadonlySet<ExternalField>>(new Set());
  const [showAll, setShowAll] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState(false);

  const problems = mode === "external" ? externalProblems(form, session.currency) : {};
  const errorOf = (f: ExternalField) => {
    const key = problems[f];
    return key !== undefined && (showAll || reached.has(f)) ? t(key) : undefined;
  };

  const save = async () => {
    setDone(false);
    setError(null);
    if (Object.keys(problems).length > 0) {
      setShowAll(true);
      return;
    }
    setBusy(true);
    try {
      await updateSessionSalesMode(orgId, eventId, sessionId, salesModePatch(mode, form, session.currency));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["sessions", orgId, eventId] }),
        queryClient.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] }),
        queryClient.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] }),
      ]);
      setDone(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby="session-sales-mode-title" data-testid="session-sales-mode">
      <h2 id="session-sales-mode-title">{t("external.session.title")}</h2>
      <p className="muted">{t("external.session.hint")}</p>
      {cancelled ? (
        <p className="notice" data-testid="session-sales-mode-cancelled">
          {t("external.session.cancelledNote")}
        </p>
      ) : (
        <>
          <SalesModeChoice
            value={mode}
            onChange={(next) => {
              setMode(next);
              setDone(false);
              setError(null);
            }}
            testId="session-sales-mode-choice"
          />
          {mode === "external" && (
            <ExternalFields
              value={form}
              onChange={(field, value) => {
                setDone(false);
                setForm((f) => ({ ...f, [field]: value }));
              }}
              onBlur={(field) => setReached((s) => new Set(s).add(field))}
              errorOf={errorOf}
              testId="session-sales-mode-external"
            />
          )}
          {error !== null && <ErrorNotice error={error} testId="session-sales-mode-error" />}
          {done && (
            <p role="status" className="notice" data-testid="session-sales-mode-done">
              {t("external.session.saved")}
            </p>
          )}
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || (mode === stored && mode === "platform")}
              aria-busy={busy}
              onClick={() => void save()}
              data-testid="session-sales-mode-save"
            >
              {busy ? t("external.session.saving") : t("external.session.save")}
            </button>
          </div>
        </>
      )}
    </section>
  );
}
