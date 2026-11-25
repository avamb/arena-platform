/**
 * "Hold mode" of one event on the events screen (spec 67, section 7.2):
 * PUT /v1/admin/events/{event_id}/hold-profile with {profile: "standard" | "high_demand" | null}.
 * Only a platform superadmin may call it; the server answers 403 to anyone else and the message is shown.
 * The event answer carries no current mode, so the select starts at "do not change". Mock data: NONE.
 */
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError } from "@/lib/api/client";
import { putEventHoldProfile, type HoldProfileName } from "@/lib/api/holdPolicy";
import { useTranslation } from "@/lib/i18n/I18nContext";
import {
  errorStyle,
  fieldErrorStyle,
  hintStyle,
  inputStyle,
  noticeStyle,
  primaryButtonStyle,
  disabledButtonStyle,
} from "./onboardingUi";
import type { Translate } from "./analyticsUi";

/** What the select offers: nothing, one of the profiles, or "back to standard" (null on the wire). */
export type HoldModeChoice = "" | HoldProfileName | "reset";

/** The profile for the wire, or undefined when nothing is chosen. */
export function choiceToProfile(choice: HoldModeChoice): HoldProfileName | null | undefined {
  if (choice === "") return undefined;
  if (choice === "reset") return null;
  return choice;
}

export interface EventHoldProfileViewProps {
  eventId: string;
  choice: HoldModeChoice;
  reason: string;
  error: string | null;
  done: boolean;
  busy: boolean;
  t: Translate;
  onChoice: (c: HoldModeChoice) => void;
  onReason: (r: string) => void;
  onApply: () => void;
}

export function EventHoldProfileView(p: EventHoldProfileViewProps): JSX.Element {
  const { t } = p;
  return (
    <div data-testid="event-hold-profile">
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center" }}>
        <select
          style={{ ...inputStyle, width: "auto" }}
          value={p.choice}
          aria-label={t("holdPolicy.event.label")}
          data-testid="event-hold-profile-select"
          onChange={(e) => p.onChoice(e.target.value as HoldModeChoice)}
        >
          <option value="">{t("holdPolicy.event.none")}</option>
          <option value="standard">{t("holdPolicy.event.standard")}</option>
          <option value="high_demand">{t("holdPolicy.event.high_demand")}</option>
          <option value="reset">{t("holdPolicy.event.reset")}</option>
        </select>
        <input
          style={{ ...inputStyle, width: 220 }}
          type="text"
          value={p.reason}
          placeholder={t("holdPolicy.event.reason")}
          aria-label={t("holdPolicy.event.reason")}
          data-testid="event-hold-profile-reason"
          onChange={(e) => p.onReason(e.target.value)}
        />
        <button
          type="button"
          style={p.busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
          disabled={p.busy}
          data-testid="event-hold-profile-apply"
          onClick={p.onApply}
        >
          {p.busy ? t("holdPolicy.event.applying") : t("holdPolicy.event.apply")}
        </button>
      </div>
      <p style={hintStyle}>{t("holdPolicy.event.hint")}</p>
      {p.error !== null ? (
        <div style={p.done ? fieldErrorStyle : errorStyle} role="alert" data-testid="event-hold-profile-error">
          {p.error}
        </div>
      ) : null}
      {p.done ? (
        <div style={noticeStyle} role="status" data-testid="event-hold-profile-done">
          {t("holdPolicy.event.done")}
        </div>
      ) : null}
    </div>
  );
}

export function EventHoldProfile({ eventId }: { eventId: string }): JSX.Element {
  const { t } = useTranslation();
  const [choice, setChoice] = useState<HoldModeChoice>("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const save = useMutation({
    mutationFn: (v: { profile: HoldProfileName | null; reason: string }) =>
      putEventHoldProfile(eventId, v.profile, v.reason),
    onSuccess: () => {
      setDone(true);
      setError(null);
      setChoice("");
      setReason("");
    },
    onError: (e) => {
      setDone(false);
      const msg = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
      setError(t("holdPolicy.event.error", { message: msg }));
    },
  });

  const apply = (): void => {
    setDone(false);
    const profile = choiceToProfile(choice);
    if (profile === undefined) {
      setError(t("holdPolicy.event.needChoice"));
      return;
    }
    if (reason.trim() === "") {
      setError(t("holdPolicy.event.needReason"));
      return;
    }
    setError(null);
    save.mutate({ profile, reason: reason.trim() });
  };

  return (
    <EventHoldProfileView
      eventId={eventId}
      choice={choice}
      reason={reason}
      error={error}
      done={done}
      busy={save.isPending}
      t={t}
      onChoice={(c) => {
        setDone(false);
        setChoice(c);
      }}
      onReason={setReason}
      onApply={apply}
    />
  );
}
