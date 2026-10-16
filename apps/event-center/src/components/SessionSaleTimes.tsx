/**
 * 5-3: the sales end and the doors-open time of ONE session, edited from its
 * card (the bot's `sessions_sale_times.go`).
 *
 * - Sales end closes every category of the session for NEW holds. It defaults
 *   to the start; the card says so.
 * - Doors open is only printed on the ticket and in the letter. It may not be
 *   after the start. "Remove" clears it.
 * - One press of "Save" is one PATCH naming that one time. No journal row and
 *   no letter go out, so there is no extra confirmation (same as the bot).
 *
 * The person types a clock time on the VENUE's clock, on the session's day or,
 * after "Another day", on a day picked with the shared DateField. The
 * conversion to an instant is lib/saleTimes.ts. The block is drawn only for a
 * person who may change a session (`session.update`) and only when the venue
 * zone is known: without a zone the times would be guessed, and a guessed time
 * on a buyer's ticket is worse than none.
 */
import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { DateField } from "@/components/DateField";
import { ErrorNotice } from "@/components/ErrorNotice";
import { updateSessionSaleTimes } from "@/lib/api/endpoints";
import { instantToWall, PRESETS, doorsAfterStart, doorsBody, draftFor, draftInstant, presetInstant, salesEndBody, type Draft, type SaleTimeKind } from "@/lib/saleTimes";
import { displayDate } from "@/lib/dates";
import { formatDateTime, zoneLabel } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";

export interface SessionSaleTimesProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  startAt: string;
  salesEndAt: string;
  doorsOpenAt: string | null;
  /** The venue's IANA zone; null when unknown. */
  timezone: string | null;
}

export function SessionSaleTimes(props: SessionSaleTimesProps) {
  const { t } = useI18n();
  const { timezone } = props;
  return (
    <section aria-labelledby="sale-times-title" data-testid="sale-times">
      <h2 id="sale-times-title">{t("saleTimes.title")}</h2>
      {timezone === null ? (
        <p className="notice" data-testid="sale-times-no-zone">
          {t("saleTimes.noZone")}
        </p>
      ) : (
        <>
          <p className="muted">{t("saleTimes.hint", { zone: zoneLabel(timezone) })}</p>
          <TimeRow kind="salesEnd" current={props.salesEndAt} {...props} timezone={timezone} />
          <TimeRow kind="doors" current={props.doorsOpenAt} {...props} timezone={timezone} />
        </>
      )}
    </section>
  );
}

interface RowProps extends SessionSaleTimesProps {
  kind: SaleTimeKind;
  current: string | null;
  timezone: string;
}

function TimeRow({ kind, current, orgId, eventId, sessionId, startAt, timezone }: RowProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Draft>(() => draftFor(current, startAt, timezone));
  const [problem, setProblem] = useState<MessageKey | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<MessageKey | null>(null);

  const sessionDay = instantToWall(startAt, timezone)?.day ?? "";
  const label = kind === "salesEnd" ? t("saleTimes.salesEnd.label") : t("saleTimes.doors.label");
  const help = kind === "salesEnd" ? t("saleTimes.salesEnd.help") : t("saleTimes.doors.help");
  const valueText =
    current === null
      ? t("saleTimes.doors.none")
      : kind === "salesEnd" && current === startAt
        ? t("saleTimes.salesEnd.atStart", { time: formatDateTime(current, timezone, locale) })
        : formatDateTime(current, timezone, locale);
  const testId = kind === "salesEnd" ? "sales-end" : "doors";

  const open = () => {
    setDraft(draftFor(current, startAt, timezone));
    setProblem(null);
    setError(null);
    setDone(null);
    setEditing(true);
  };

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["sessions", orgId, eventId] }),
      queryClient.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] }),
    ]);

  /** One PATCH naming this one time. `iso` null clears the doors time. */
  const send = async (iso: string | null) => {
    setBusy(true);
    setError(null);
    try {
      const body = kind === "salesEnd" ? salesEndBody(iso as string) : doorsBody(iso);
      await updateSessionSaleTimes(orgId, eventId, sessionId, body);
      await refresh();
      setEditing(false);
      setDone(iso === null ? "saleTimes.removed" : "saleTimes.saved");
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const save = () => {
    setProblem(null);
    const result = draftInstant(draft, startAt, timezone);
    if (!result.ok) {
      setProblem(`saleTimes.error.${result.problem}` as MessageKey);
      return;
    }
    // The server refuses it too (400 session.doors_after_start); saying so before the call saves a trip.
    if (kind === "doors" && doorsAfterStart(result.iso, startAt)) {
      setProblem("saleTimes.error.doorsAfterStart");
      return;
    }
    void send(result.iso);
  };

  const timeId = `${testId}-time`;

  return (
    <div className="sale-time" data-testid={`${testId}-row`}>
      <h3>{label}</h3>
      <p data-testid={`${testId}-value`}>{valueText}</p>
      <p className="muted">{help}</p>
      {!editing && (
        <div className="actions">
          <button type="button" className="btn" onClick={open} data-testid={`${testId}-change`}>
            {kind === "doors" && current === null ? t("saleTimes.set") : t("saleTimes.change")}
          </button>
          {kind === "doors" && current !== null && (
            <button type="button" className="btn" disabled={busy} onClick={() => void send(null)} data-testid="doors-remove">
              {t("saleTimes.remove")}
            </button>
          )}
        </div>
      )}
      {!editing && done !== null && (
        <p role="status" className="notice" data-testid={`${testId}-done`}>
          {t(done)}
        </p>
      )}
      {!editing && error !== null && <ErrorNotice error={error} testId={`${testId}-error`} />}

      {editing && (
        <div className="sale-time-editor" role="group" aria-label={label} data-testid={`${testId}-editor`}>
          <p>{t("saleTimes.presets")}</p>
          <div className="actions">
            {PRESETS[kind].map((p) => (
              <button
                key={p.key}
                type="button"
                className="btn"
                onClick={() => {
                  setProblem(null);
                  setDraft(draftFor(presetInstant(startAt, p.minutes), startAt, timezone));
                }}
                data-testid={`${testId}-preset-${p.key}`}
              >
                {t(`saleTimes.preset.${p.key}` as MessageKey)}
              </button>
            ))}
          </div>

          <label htmlFor={timeId}>{t("saleTimes.timeLabel")}</label>
          <input
            id={timeId}
            type="time"
            value={draft.time}
            onChange={(e) => {
              setProblem(null);
              setDraft({ ...draft, time: e.target.value });
            }}
            data-testid={timeId}
          />

          {draft.anotherDay ? (
            <>
              <DateField
                label={t("saleTimes.anotherDay")}
                value={draft.day}
                timezone={timezone}
                max={kind === "doors" ? sessionDay : undefined}
                onChange={(iso) => {
                  setProblem(null);
                  setDraft({ ...draft, day: iso });
                }}
              />
              <button
                type="button"
                className="btn"
                onClick={() => setDraft({ ...draft, anotherDay: false, day: sessionDay })}
                data-testid={`${testId}-same-day`}
              >
                {t("saleTimes.sameDay")}
              </button>
            </>
          ) : (
            <p>
              {t("saleTimes.sessionDay", { day: displayDate(sessionDay) })}{" "}
              <button
                type="button"
                className="btn btn-link"
                onClick={() => setDraft({ ...draft, anotherDay: true, day: draft.day ?? sessionDay })}
                data-testid={`${testId}-another-day`}
              >
                {t("saleTimes.anotherDay")}
              </button>
            </p>
          )}

          {problem !== null && (
            <p className="field-error" role="alert" data-testid={`${testId}-problem`}>
              {t(problem)}
            </p>
          )}
          {error !== null && <ErrorNotice error={error} testId={`${testId}-error`} />}

          <div className="actions">
            <button type="button" className="btn btn-primary" disabled={busy} onClick={save} data-testid={`${testId}-save`}>
              {busy ? t("saleTimes.saving") : t("saleTimes.save")}
            </button>
            <button type="button" className="btn" disabled={busy} onClick={() => setEditing(false)} data-testid={`${testId}-cancel`}>
              {t("saleTimes.cancel")}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
